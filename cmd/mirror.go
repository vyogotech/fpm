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
	mirrorCatalogPath     string
	mirrorRepoName        string
	mirrorApps            string
	mirrorDryRun          bool
	mirrorJSON            bool
	mirrorSkipPublish     bool
	mirrorOutputPath      string
	mirrorReportPath      string
	mirrorCacheDir        string
	mirrorNoClean         bool
	mirrorAllowThirdParty bool
	mirrorPythonVersion   string
	mirrorPlatforms       []string
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

	conf, err := config.LoadConfig()
	if err != nil {
		return err
	}
	repo, ok := config.GetRepository(conf, mirrorRepoName)
	if !ok {
		return fmt.Errorf("repository %q is not configured; add it with `fpm repo add %s <url> --username <name>`",
			mirrorRepoName, mirrorRepoName)
	}

	// Publishing must not stall on a password prompt halfway through a long
	// build, so resolve credentials up front and refuse to start without them.
	if !mirrorDryRun && !mirrorSkipPublish {
		if _, err := repository.ResolveCredentials(repo.Name, repo.Username, false); err != nil {
			return err
		}
	}

	now := time.Now().UTC().Format("20060102")
	plan, err := mirror.BuildPlanForRepo(apps, repo, nil, now)
	if err != nil {
		return err
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

	runner := &mirror.Runner{
		FPMBin:        fpmBin,
		Workspace:     workspace,
		OutputPath:    mirrorOutputPath,
		RepoName:      repo.Name,
		SkipPublish:   mirrorSkipPublish,
		PythonVersion: pyVer,
		Platforms:     platforms,
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
	mirrorCmd.Flags().StringVar(&mirrorRepoName, "repo", "", "Configured repository to publish to (see `fpm repo add`)")
	mirrorCmd.Flags().StringVar(&mirrorApps, "apps", "", "Comma-separated catalog slugs to restrict the run to")
	mirrorCmd.Flags().BoolVar(&mirrorDryRun, "dry-run", false, "Show what would be built and stop")
	mirrorCmd.Flags().BoolVar(&mirrorJSON, "json", false, "With --dry-run, emit the plan as JSON")
	mirrorCmd.Flags().BoolVar(&mirrorSkipPublish, "skip-publish", false, "Build artifacts into --output-path without publishing")
	mirrorCmd.Flags().StringVar(&mirrorOutputPath, "output-path", "dist", "Directory finished .fpm artifacts are stored in")
	mirrorCmd.Flags().StringVar(&mirrorReportPath, "report", "", "Write a JSON run report to this path")
	mirrorCmd.Flags().StringVar(&mirrorCacheDir, "cache-dir", "", "Persistent build cache (default ~/.fpm/build-cache)")
	mirrorCmd.Flags().BoolVar(&mirrorNoClean, "no-clean", false, "Keep checkout state between builds (debugging)")
	mirrorCmd.Flags().BoolVar(&mirrorAllowThirdParty, "allow-third-party", true, "Allow third-party / external git repositories in the catalog")
	mirrorCmd.Flags().StringVar(&mirrorPythonVersion, "python-version", "", "Target Python version for vendored wheels (e.g. 3.11, 3.12; defaults to host python version)")
	mirrorCmd.Flags().StringArrayVar(&mirrorPlatforms, "platform", nil, "Target wheel platform tags (defaults to "+wheels.DefaultProdPlatform+")")
	mirrorCmd.MarkFlagRequired("repo")
}
