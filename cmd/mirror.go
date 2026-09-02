package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"fpm/internal/config"
	"fpm/internal/mirror"
	"fpm/internal/repository"
	"fpm/internal/wheels"

	"github.com/spf13/cobra"
)

var (
	mirrorCatalogPath        string
	mirrorRepoNames          []string
	mirrorListSlugs          bool
	mirrorTier               int
	mirrorListTiers          bool
	mirrorRepublish          bool
	mirrorGitURL             string
	mirrorGitRef             string
	mirrorSlug               string
	mirrorAppName            string
	mirrorApps               string
	mirrorDryRun             bool
	mirrorJSON               bool
	mirrorSkipPublish        bool
	mirrorOutputPath         string
	mirrorReportPath         string
	mirrorCacheDir           string
	mirrorNoClean            bool
	mirrorAllowThirdParty    bool
	mirrorPythonVersion      string
	mirrorFrappeRef          string
	mirrorAllowUnbuiltAssets bool
	mirrorPlatforms          []string
)

// Exit codes: 0 clean, 1 one or more apps failed, 2 configuration/catalog
// error. Distinct codes so automation can tell "fix the catalog" from
// "look at the report".
var mirrorCmd = &cobra.Command{
	Use:   "mirror",
	Short: "Bulk-build and publish official Frappe apps from the catalog",
	Long: "Mirror reads a curated CSV catalog of official frappe-org apps, discovers the\n" +
		"latest release tag of each major line, and packages and publishes the versions\n" +
		"the repository does not have yet. Re-runs are idempotent: published versions\n" +
		"are skipped. Wheel vendoring, npm/yarn asset builds, and git checkouts all\n" +
		"reuse a persistent cache across apps and runs.",
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := runMirror(); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			os.Exit(2)
		}
		return nil
	},
}

func detectPythonVersion() string {
	pythonExe, err := wheels.FindPython()
	if err != nil {
		return "3.11"
	}
	cmd := exec.Command(pythonExe, "-c", "import sys; print(f'{sys.version_info.major}.{sys.version_info.minor}')")
	out, err := cmd.Output()
	if err != nil {
		return "3.11"
	}
	return strings.TrimSpace(string(out))
}

func runMirror() error {
	catalog, err := mirror.LoadCatalogWithOptions(mirrorCatalogPath, mirror.CatalogOptions{
		AllowThirdParty: mirrorAllowThirdParty,
	})
	if err != nil {
		return err
	}

	var filter []string
	if mirrorApps != "" {
		filter = strings.Split(mirrorApps, ",")
	}
	apps, err := catalog.Enabled(filter)
	if err != nil {
		return err
	}

	// --list-slugs exists so CI can shard the catalog across runners without
	// re-implementing which apps are enabled and which third-party entries are
	// allowed. It answers from the catalog alone: no repository, no credentials and
	// no network, so it is safe to run before anything is configured.
	if mirrorListSlugs {
		listed := apps
		if mirrorTier >= 0 {
			listed = mirror.InTier(apps, mirrorTier)
		}
		slugs := make([]string, 0, len(listed))
		for _, app := range listed {
			slugs = append(slugs, app.Slug)
		}
		return json.NewEncoder(os.Stdout).Encode(slugs)
	}
	if mirrorListTiers {
		return json.NewEncoder(os.Stdout).Encode(mirror.Tiers(apps))
	}

	conf, err := config.LoadConfig()
	if err != nil {
		return err
	}
	// Every --repo is resolved before anything is built, so a typo in the third one
	// fails immediately rather than after an hour of building.
	repos := make([]config.RepositoryConfig, 0, len(mirrorRepoNames))
	repoNames := make([]string, 0, len(mirrorRepoNames))
	for _, name := range mirrorRepoNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		repo, ok := config.GetRepository(conf, name)
		if !ok {
			return fmt.Errorf("repository %q is not configured; add it with `fpm repo add %s <url> --username <name>`",
				name, name)
		}
		repos = append(repos, repo)
		repoNames = append(repoNames, repo.Name)
	}
	if len(repos) == 0 {
		return fmt.Errorf("--repo is required")
	}

	// Publishing must not stall on a password prompt halfway through a long
	// build, so resolve credentials up front and refuse to start without them.
	if !mirrorDryRun && !mirrorSkipPublish {
		for _, repo := range repos {
			if _, err := repository.ResolveCredentials(repo.Name, repo.Username, false); err != nil {
				return err
			}
		}
	}

	now := time.Now().UTC().Format("20060102")
	var planOpts []mirror.PlanOption
	if mirrorRepublish {
		planOpts = append(planOpts, mirror.Republish())
		fmt.Println("Republishing: versions already in the registry will be rebuilt and pushed over.")
	}
	var plan *mirror.Plan
	if mirrorGitURL != "" {
		// On demand: package a repository that is not in the catalog at all. Everything
		// downstream is the same — bench-shaped checkout, frontend build, build-time
		// dependencies, wheel vendoring, publish to every --repo — so an ad-hoc app
		// gets exactly the treatment a curated one does.
		plan, err = mirror.PlanAdHoc(mirrorGitURL, mirrorGitRef, mirrorSlug, mirrorAppName, repos, nil, now, planOpts...)
	} else {
		// A version is built when any repository is missing it, so a run leaves them all
		// holding the same set even if they started out of step.
		plan, err = mirror.BuildPlanForRepos(apps, repos, nil, now, planOpts...)
	}
	if err != nil {
		return err
	}
	if len(repos) > 1 {
		fmt.Printf("Mirroring to %d repositories: %s\n", len(repos), strings.Join(repoNames, ", "))
	}

	if mirrorDryRun {
		if mirrorJSON {
			return json.NewEncoder(os.Stdout).Encode(plan)
		}
		fmt.Print(mirror.RenderPlan(plan))
		return nil
	}

	fpmBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot locate the fpm binary for self-exec: %w", err)
	}
	cacheDir := mirrorCacheDir
	if cacheDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		cacheDir = filepath.Join(home, ".fpm", "build-cache")
	}
	workspace, err := mirror.NewWorkspace(cacheDir, mirrorNoClean)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(mirrorOutputPath, 0o755); err != nil {
		return err
	}

	pyVer := mirrorPythonVersion
	if pyVer == "" {
		pyVer = detectPythonVersion()
	}
	platforms := mirrorPlatforms
	if len(platforms) == 0 {
		platforms = []string{wheels.DefaultProdPlatform}
	}

	// Every catalog entry's repo, including the disabled ones: frappe is no longer
	// published but helpdesk's build still reads its source, so the mirror has to know
	// where to fetch it from.
	catalogRepos := map[string]string{}
	buildDepRefs := map[string]map[string]string{}
	for _, app := range catalog.Apps {
		catalogRepos[app.Slug] = app.Repo
		if len(app.BuildDeps) > 0 {
			buildDepRefs[app.Slug] = app.BuildDeps
		}
	}

	runner := &mirror.Runner{
		FPMBin:             fpmBin,
		Workspace:          workspace,
		OutputPath:         mirrorOutputPath,
		RepoNames:          repoNames,
		CatalogRepos:       catalogRepos,
		BuildDepRefs:       buildDepRefs,
		SkipPublish:        mirrorSkipPublish,
		Republish:          mirrorRepublish,
		PythonVersion:      pyVer,
		Platforms:          platforms,
		FrappeRef:          mirrorFrappeRef,
		AllowUnbuiltAssets: mirrorAllowUnbuiltAssets,
	}
	results := runner.Run(plan)

	fmt.Print(mirror.RenderResults(results))
	for _, skip := range plan.Skipped {
		fmt.Printf("skip %s: %s\n", skip.Slug, skip.Detail)
	}
	mirror.AppendStepSummary(results)
	if mirrorReportPath != "" {
		if err := mirror.WriteReport(mirrorReportPath, results); err != nil {
			return err
		}
	}

	if mirror.AnyFailed(results) {
		fmt.Fprintln(os.Stderr, "Error: one or more apps failed — see the report above")
		os.Exit(1)
	}
	return nil
}

func init() {
	rootCmd.AddCommand(mirrorCmd)
	mirrorCmd.Flags().StringVar(&mirrorCatalogPath, "catalog", "catalog/apps.csv", "Path to the catalog CSV")
	mirrorCmd.Flags().StringArrayVar(&mirrorRepoNames, "repo", nil, "Configured repository to publish to (see `fpm repo add`); repeat to mirror the same catalog into several backends at once, e.g. --repo ghcr --repo fpm-http. A version is built when any of them is missing it, and published to every one")
	mirrorCmd.Flags().StringVar(&mirrorApps, "apps", "", "Comma-separated catalog slugs to restrict the run to")
	mirrorCmd.Flags().BoolVar(&mirrorDryRun, "dry-run", false, "Show what would be built and stop")
	mirrorCmd.Flags().BoolVar(&mirrorJSON, "json", false, "With --dry-run, emit the plan as JSON")
	mirrorCmd.Flags().BoolVar(&mirrorSkipPublish, "skip-publish", false, "Build artifacts into --output-path without publishing")
	mirrorCmd.Flags().StringVar(&mirrorOutputPath, "output-path", "dist", "Directory finished .fpm artifacts are stored in")
	mirrorCmd.Flags().StringVar(&mirrorReportPath, "report", "", "Write a JSON run report to this path")
	mirrorCmd.Flags().StringVar(&mirrorCacheDir, "cache-dir", "", "Persistent build cache (default ~/.fpm/build-cache)")
	mirrorCmd.Flags().BoolVar(&mirrorNoClean, "no-clean", false, "Keep checkout state between builds (debugging)")
	mirrorCmd.Flags().BoolVar(&mirrorAllowThirdParty, "allow-third-party", false, "Also build catalog entries whose repository is outside the frappe GitHub organisation. Off by default: the mirror publishes the frappe org's own apps, and a third-party entry is reported as disabled rather than silently skipped")
	mirrorCmd.Flags().StringVar(&mirrorFrappeRef, "frappe-ref", mirror.DefaultFrappeRef, "The frappe branch or tag whose esbuild compiles the catalogue's desk assets. The catalog's build_deps column overrides it per app")
	mirrorCmd.Flags().BoolVar(&mirrorAllowUnbuiltAssets, "allow-unbuilt-assets", false, "Publish an app whose desk bundles could not be compiled. The package installs and its desk UI does not render until the destination bench runs its own build")
	mirrorCmd.Flags().StringVar(&mirrorPythonVersion, "python-version", "", "Target Python version for vendored wheels (e.g. 3.11, 3.12; defaults to host python version)")
	mirrorCmd.Flags().StringArrayVar(&mirrorPlatforms, "platform", nil, "Target wheel platform tags (defaults to "+wheels.DefaultProdPlatform+")")
	mirrorCmd.Flags().BoolVar(&mirrorListSlugs, "list-slugs", false, "Print the enabled catalog slugs as a JSON array and exit, for sharding a run across machines. Needs no repository and no network")
	mirrorCmd.Flags().IntVar(&mirrorTier, "tier", -1, "With --list-slugs, restrict the listing to one catalog tier. Every app in a lower tier is published before a higher one starts, so an app whose required_apps name other catalog entries can resolve them")
	mirrorCmd.Flags().BoolVar(&mirrorRepublish, "republish", false, "Rebuild and push versions the repositories already have. Off by default: skipping them is what makes a re-run cheap, and a published version is an artifact others may have pinned. Use it when the packaging itself changed and what is published would now be built differently")
	mirrorCmd.Flags().StringVar(&mirrorGitURL, "git-url", "", "Package a repository that is not in the catalog. Everything else is unchanged: bench-shaped checkout, frontend build, build-time dependencies, wheel vendoring and publishing to every --repo")
	mirrorCmd.Flags().StringVar(&mirrorGitRef, "git-ref", "", "With --git-url, the tag or branch to package (default: the repository's default branch). A tag is packaged at its version; anything else as a branch pseudo-version carrying the head commit")
	mirrorCmd.Flags().StringVar(&mirrorSlug, "slug", "", "With --git-url, the name to publish under (default: derived from the URL's last path segment)")
	mirrorCmd.Flags().StringVar(&mirrorAppName, "app-name", "", "With --git-url, the Frappe app module name when it differs from the slug")
	mirrorCmd.Flags().BoolVar(&mirrorListTiers, "list-tiers", false, "Print the catalog's distinct tiers as a JSON array and exit, so a runner can drive one wave per tier")
	// --repo is not marked required at the flag level because --list-slugs answers
	// from the catalog alone; the run path validates it instead.
}
