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

// BuildPlanForRepos discovers desired versions against every repository the run
// publishes to, of any mix of backends (HTTP and OCI).
//
// A version counts as published only when every repository already has it. Missing
// from any one of them makes it a build item, because the run's job is to leave all
// of them holding the same set — a version in GHCR but not in the HTTP registry still
// has to be built and pushed. Publishing is idempotent per repository, so the ones
// that already have it simply report it as such.
func BuildPlanForRepos(apps []App, repos []config.RepositoryConfig, client *http.Client, now string) (*Plan, error) {
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
			item, skip, err := planBranch(app, published, now)
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
			if _, exists := published[version]; exists {
				plan.Skipped = append(plan.Skipped, SkipItem{
					Slug:   app.Slug,
					Detail: fmt.Sprintf("%s already published", version),
				})
				continue
			}
			plan.Items = append(plan.Items, BuildItem{
				Slug:        app.Slug,
				AppName:     app.AppName,
				Repo:        app.Repo,
				Ref:         tag.Name,
				Version:     version,
				BundleDeps:  app.BundleDeps,
				Reason:      fmt.Sprintf("latest of the %s line", majorLabel(version)),
				buildScript: app.BuildScript,
			})
		}
	}

	return plan, nil
}

func planBranch(app App, published map[string]struct{}, now string) (BuildItem, *SkipItem, error) {
	sha, err := ResolveRemoteBranch(app.Repo, app.Branch)
	if err != nil {
		return BuildItem{}, nil, err
	}

	// An unchanged branch republishes nothing: the head commit is embedded in
	// every pseudo-version, so its presence in any published version means
	// this exact tree is already in the registry.
	for version := range published {
		if strings.Contains(version, "-git.") && strings.HasSuffix(version, "."+ShortSHA(sha)) {
			return BuildItem{}, &SkipItem{
				Slug:   app.Slug,
				Detail: fmt.Sprintf("branch %s head %s already published as %s", app.Branch, ShortSHA(sha), version),
			}, nil
		}
	}

	version := BranchPseudoVersion(app.BranchMajor, now, sha)
	return BuildItem{
		Slug:        app.Slug,
		AppName:     app.AppName,
		Repo:        app.Repo,
		Ref:         app.Branch,
		Version:     version,
		BundleDeps:  app.BundleDeps,
		Reason:      fmt.Sprintf("tip of branch %s", app.Branch),
		buildScript: app.BuildScript,
		isBranch:    true,
	}, nil, nil
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
