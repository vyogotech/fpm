package mirror

import (
	"fmt"
	"net/http"
	"strings"

	"fpm/internal/config"
	"fpm/internal/repository"
)

// BuildItem is one (app, version) the registry is missing.
type BuildItem struct {
	Slug       string `json:"slug"`
	AppName    string `json:"app_name,omitempty"` // catalog override, may be empty
	Repo       string `json:"repo"`
	Ref        string `json:"ref"` // git ref to check out: tag name, or branch name
	Version    string `json:"version"`
	BundleDeps bool   `json:"bundle_deps"`
	Reason     string `json:"reason"`
	// PipOverrides replace Python requirements the app declares, from the catalog.
	PipOverrides []string `json:"pip_overrides,omitempty"`

	buildScript string
	isBranch    bool
}

// SkipItem records an app or version the plan deliberately left out.
type SkipItem struct {
	Slug   string `json:"slug"`
	Detail string `json:"detail"`
}

// Plan is what a mirror run would build.
type Plan struct {
	Items   []BuildItem `json:"items"`
	Skipped []SkipItem  `json:"skipped,omitempty"`
}

// BuildPlan discovers desired versions for each app and drops the ones the
// registry already has. Discovery needs no clone (ls-remote) and no
// credentials (the read path is anonymous), so a dry run is cheap.
//
// now is the yyyymmdd stamp used in branch pseudo-versions.
func BuildPlan(apps []App, repoBaseURL string, client *http.Client, now string) (*Plan, error) {
	return BuildPlanForRepo(apps, config.RepositoryConfig{URL: repoBaseURL}, client, now)
}

// BuildPlanForRepo discovers desired versions against a repository config (HTTP or OCI).
func BuildPlanForRepo(apps []App, repo config.RepositoryConfig, client *http.Client, now string) (*Plan, error) {
	return BuildPlanForRepos(apps, []config.RepositoryConfig{repo}, client, now)
}

// PlanOption adjusts how a plan is built.
type PlanOption func(*planConfig)

type planConfig struct{ republish bool }

// Republish builds every discovered version even when the repositories already have it.
//
// Skipping what is published is what makes a nightly run cheap and idempotent, and a
// published version is an artifact others may have pinned — overwriting it hands them
// different bytes under the same name. So this is deliberately not the default. It is
// for the case the skip cannot see: the packaging itself changed, and a version already
// in the registry would now be built differently. A package built before fpm compiled
// app frontends, say, installs and serves a blank page, and no amount of re-running
// would replace it.
func Republish() PlanOption { return func(c *planConfig) { c.republish = true } }

func planConfigFrom(opts []PlanOption) planConfig {
	var c planConfig
	for _, opt := range opts {
		opt(&c)
	}
	return c
}

// BuildPlanForRepos discovers desired versions against every repository the run
// publishes to, of any mix of backends (HTTP and OCI).
//
// A version counts as published only when every repository already has it. Missing
// from any one of them makes it a build item, because the run's job is to leave all
// of them holding the same set — a version in GHCR but not in the HTTP registry still
// has to be built and pushed. Publishing is idempotent per repository, so the ones
// that already have it simply report it as such.
func BuildPlanForRepos(apps []App, repos []config.RepositoryConfig, client *http.Client, now string, opts ...PlanOption) (*Plan, error) {
	cfg := planConfigFrom(opts)
	plan := &Plan{}
	if len(repos) == 0 {
		return nil, fmt.Errorf("no repository to mirror to")
	}

	for _, app := range apps {
		published, err := publishedInAll(repos, app, client)
		if err != nil {
			return nil, err
		}

		if app.Track == TrackBranch {
			item, skip, err := planBranch(app, published, now, cfg.republish)
			if err != nil {
				return nil, err
			}
			if skip != nil {
				plan.Skipped = append(plan.Skipped, *skip)
			} else {
				plan.Items = append(plan.Items, item)
			}
			continue
		}

		tags, err := ListRemoteTags(app.Repo)
		if err != nil {
			return nil, err
		}
		wanted := LatestPerMajor(tags, app.Majors)
		if len(wanted) == 0 {
			plan.Skipped = append(plan.Skipped, SkipItem{
				Slug:   app.Slug,
				Detail: "no release tags match the catalog's version rules",
			})
			continue
		}

		for _, tag := range wanted {
			version := NormalizeVersion(tag.Name)
			if _, exists := published[version]; exists && !cfg.republish {
				plan.Skipped = append(plan.Skipped, SkipItem{
					Slug:   app.Slug,
					Detail: fmt.Sprintf("%s already published", version),
				})
				continue
			}
			plan.Items = append(plan.Items, BuildItem{
				Slug:         app.Slug,
				AppName:      app.AppName,
				Repo:         app.Repo,
				Ref:          tag.Name,
				Version:      version,
				BundleDeps:   app.BundleDeps,
				PipOverrides: app.PipOverrides,
				Reason:       fmt.Sprintf("latest of the %s line", majorLabel(version)),
				buildScript:  app.BuildScript,
			})
		}
	}

	return plan, nil
}

func planBranch(app App, published map[string]struct{}, now string, republish bool) (BuildItem, *SkipItem, error) {
	sha, err := ResolveRemoteBranch(app.Repo, app.Branch)
	if err != nil {
		return BuildItem{}, nil, err
	}

	// A pseudo-version carries the head commit, so finding one means this exact tree
	// is already in the registry.
	existing := ""
	for version := range published {
		if strings.Contains(version, "-git.") && strings.HasSuffix(version, "."+ShortSHA(sha)) {
			existing = version
			break
		}
	}
	if existing != "" && !republish {
		// An unchanged branch republishes nothing, which is what makes a nightly cheap.
		return BuildItem{}, &SkipItem{
			Slug:   app.Slug,
			Detail: fmt.Sprintf("branch %s head %s already published as %s", app.Branch, ShortSHA(sha), existing),
		}, nil
	}

	// Republishing rebuilds this tree because the packaging changed, not because the
	// source did. Minting a fresh pseudo-version would stamp today's date on an
	// identical commit — a duplicate that consumers see as an update and that moves
	// latest_version for no change — so the version it already has is reused.
	version := existing
	if version == "" {
		version = BranchPseudoVersion(app.BranchMajor, now, sha)
	}
	return BuildItem{
		Slug:         app.Slug,
		AppName:      app.AppName,
		Repo:         app.Repo,
		Ref:          app.Branch,
		Version:      version,
		BundleDeps:   app.BundleDeps,
		PipOverrides: app.PipOverrides,
		Reason:       branchReason(app.Branch, existing != ""),
		buildScript:  app.BuildScript,
		isBranch:     true,
	}, nil, nil
}

// branchReason distinguishes a new commit from a rebuild of one already published.
func branchReason(branch string, rebuilt bool) string {
	if rebuilt {
		return fmt.Sprintf("rebuilding the published head of branch %s", branch)
	}
	return fmt.Sprintf("tip of branch %s", branch)
}

func publishedVersions(repoBaseURL string, app App, client *http.Client) (map[string]struct{}, error) {
	return publishedVersionsForRepo(config.RepositoryConfig{URL: repoBaseURL}, app, client)
}

// publishedInAll returns the versions of app that every repository already holds,
// which is the intersection of their published sets.
func publishedInAll(repos []config.RepositoryConfig, app App, client *http.Client) (map[string]struct{}, error) {
	var common map[string]struct{}
	for _, repo := range repos {
		versions, err := publishedVersionsForRepo(repo, app, client)
		if err != nil {
			return nil, err
		}
		if common == nil {
			common = versions
			continue
		}
		for version := range common {
			if _, ok := versions[version]; !ok {
				delete(common, version)
			}
		}
	}
	if common == nil {
		common = map[string]struct{}{}
	}
	return common, nil
}

func publishedVersionsForRepo(repo config.RepositoryConfig, app App, client *http.Client) (map[string]struct{}, error) {
	meta, found, err := repository.FetchRemotePackageMetadataForRepo(repo, Org, app.MetadataName(), client)
	if err != nil {
		return nil, fmt.Errorf("checking published versions of %s: %w", app.Slug, err)
	}
	versions := map[string]struct{}{}
	if found && meta != nil {
		for version := range meta.Versions {
			versions[version] = struct{}{}
		}
	}
	return versions, nil
}

func majorLabel(version string) string {
	if i := strings.IndexByte(version, '.'); i > 0 {
		return "v" + version[:i]
	}
	return "v" + version
}

// SlugFromRepoURL derives a catalog-style slug from a git URL: the last path
// segment, without a .git suffix, lowercased. "https://github.com/acme/My-App.git"
// becomes "my-app".
func SlugFromRepoURL(repoURL string) string {
	trimmed := strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(repoURL), "/"), ".git")
	if i := strings.LastIndexAny(trimmed, "/:"); i >= 0 {
		trimmed = trimmed[i+1:]
	}
	return strings.ToLower(trimmed)
}

// PlanAdHoc builds a one-item plan for a repository that is not in the catalog, so a
// run can package an arbitrary checkout on demand rather than only curated apps.
//
// ref may be a tag, a branch, or empty for the repository's default branch. A ref that
// names a tag is packaged at that tag's version; anything else is packaged as a branch
// pseudo-version carrying the head commit, exactly as a catalog branch entry is, so
// re-running an unchanged branch republishes nothing.
//
// slug names the app in the registry; empty derives it from the URL.
func PlanAdHoc(repoURL, ref, slug, appName string, repos []config.RepositoryConfig, client *http.Client, now string, opts ...PlanOption) (*Plan, error) {
	cfg := planConfigFrom(opts)
	repoURL = strings.TrimSpace(repoURL)
	if repoURL == "" {
		return nil, fmt.Errorf("a git URL is required")
	}
	if slug == "" {
		slug = SlugFromRepoURL(repoURL)
	}
	if !slugPattern.MatchString(slug) {
		return nil, fmt.Errorf("derived slug %q is not usable; pass --slug", slug)
	}

	app := App{Slug: slug, AppName: appName, Repo: repoURL, BundleDeps: true, Enabled: true}
	published, err := publishedInAll(repos, app, client)
	if err != nil {
		return nil, err
	}
	if cfg.republish {
		published = map[string]struct{}{}
	}

	// A ref that matches a published tag is a release; everything else is a branch.
	if ref != "" {
		tags, tagErr := ListRemoteTags(repoURL)
		if tagErr != nil {
			return nil, tagErr
		}
		for _, tag := range tags {
			if tag.Name != ref {
				continue
			}
			version := NormalizeVersion(tag.Name)
			if _, exists := published[version]; exists && !cfg.republish {
				return &Plan{Skipped: []SkipItem{{Slug: slug, Detail: version + " already published"}}}, nil
			}
			return &Plan{Items: []BuildItem{{
				Slug: slug, AppName: appName, Repo: repoURL, Ref: tag.Name, Version: version,
				BundleDeps: true, Reason: "requested tag " + tag.Name,
			}}}, nil
		}
	}

	branch := ref
	if branch == "" {
		// ls-remote --symref is the only remote way to learn it; guessing "main" is
		// wrong for every frappe app tracked from "develop".
		branch, err = ResolveDefaultBranch(repoURL)
		if err != nil {
			return nil, err
		}
	}
	app.Branch = branch
	item, skip, err := planBranch(app, published, now, planConfigFrom(opts).republish)
	if err != nil {
		return nil, err
	}
	if skip != nil {
		return &Plan{Skipped: []SkipItem{*skip}}, nil
	}
	return &Plan{Items: []BuildItem{item}}, nil
}
