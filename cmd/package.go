package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fpm/internal/appstore"
	"fpm/internal/apputils"
	"fpm/internal/archive"
	"fpm/internal/assets"
	"fpm/internal/benchbuild"
	"fpm/internal/config"
	"fpm/internal/frontend"
	"fpm/internal/gitutils"
	"fpm/internal/metadata"
	"fpm/internal/resolver"
	"fpm/internal/wheels"

	"github.com/spf13/cobra"
)

// validateFrappeAppStructure checks if the source directory has a valid Frappe app
// structure. It is the package-level name for apputils.ValidateFrappeApp, whose error
// wraps apputils.ErrNotFrappeApp.
func validateFrappeAppStructure(sourceDir string, appName string) error {
	return apputils.ValidateFrappeApp(sourceDir, appName)
}

var (
	packageOutputPath       string
	packageVersion          string
	packageOverwrite        bool
	packageType             string
	packageSkipLocalInstall bool
	packageBundleDeps       bool
	packagePlatforms        []string
	packagePythonVersion    string
	packageImplementation   string
	packageABIs             []string
	packageBenchPath        string
	packageBuildVerbose     bool
	packageRepo             string
	packageWithDeps         bool
	packageBuildFrontend    bool
	packageFrontendTimeout  time.Duration

	packageFrontendSiteConfig string
	packageNoBenchScaffold    bool
)

var packageCmd = &cobra.Command{
	Use:   "package [source-path]",
	Short: "Package a Frappe application into an .fpm file",
	Long: `Packages a Frappe application from a local development directory into an .fpm file.

The source is validated as a Frappe app before anything else happens, so a checkout
that is not a Frappe app is rejected immediately with exit code ` + fmt.Sprint(ExitNotFrappeApp) + `.

The package records the exact git commit it was built from, resolves the app's
required_apps (hooks.py) to pinned packages, vendors Python dependencies as wheels
for the destination platform (--platform/--python-version), and — with --bench-path —
runs Frappe's own asset build so the package ships compiled JS/CSS.

When the checkout declares a JavaScript frontend — the Vite SPA that apps such as
frappe/crm, frappe/helpdesk and frappe/insights build into <app>/public/frontend and
render from <app>/www/<app>.html — that frontend is compiled too and its output is
packaged. Those files are gitignored build products that Frappe's own esbuild never
produces, so a package built without them installs cleanly and then serves a blank
page. Pass --build-frontend=false to skip it.
By default, it also installs the packaged app to the local FPM app store.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		sourcePath := "."
		if len(args) > 0 {
			sourcePath = args[0]
		}
		absSourcePath, err := filepath.Abs(sourcePath)
		if err != nil {
			return fmt.Errorf("failed to get absolute source path for '%s': %w", sourcePath, err)
		}
		if _, err := os.Stat(absSourcePath); os.IsNotExist(err) {
			return fmt.Errorf("source path '%s' does not exist", absSourcePath)
		}

		versionFlagValue := packageVersion
		if versionFlagValue == "" {
			return fmt.Errorf("--version flag is required")
		}

		orgFlagValue, _ := cmd.Flags().GetString("org")
		appNameFlagValue, _ := cmd.Flags().GetString("app-name")

		// --- Step 1: is this a Frappe app at all? ---
		// Runs before metadata, git, dependency resolution or any build, on nothing
		// but the directory layout, so a caller feeding arbitrary checkouts learns
		// "not a Frappe app" in milliseconds and can tell it from every other failure.
		hint := appNameFlagValue
		if hint == "" {
			hint = filepath.Base(absSourcePath)
		}
		appModule, err := apputils.DetectAppModule(absSourcePath, hint)
		if err != nil {
			return err
		}
		if err := validateFrappeAppStructure(absSourcePath, appModule); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Validated Frappe app module '%s' in %s\n", appModule, absSourcePath)

		hooksFilePath := filepath.Join(absSourcePath, appModule, "hooks.py")
		if appNameFromHooks, errHooks := apputils.GetAppNameFromHooks(hooksFilePath); errHooks == nil && appNameFromHooks != "" && appNameFromHooks != appModule {
			fmt.Fprintf(cmd.ErrOrStderr(), "Warning: hooks.py declares app_name = %q but the app module directory is %q; Frappe imports the module by app_name, so these should match. Using %q.\n",
				appNameFromHooks, appModule, appModule)
		}

		// --- Step 2: metadata ---
		meta, err := metadata.LoadAppMetadata(absSourcePath)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not load existing app_metadata.json: %v. Will generate new.\n", err)
		}
		if meta == nil || (meta.PackageName == "" && meta.AppName == "") {
			generatedMeta, genErr := metadata.GenerateAppMetadata(absSourcePath, versionFlagValue)
			if genErr != nil {
				return fmt.Errorf("failed to generate default app metadata: %w", genErr)
			}
			meta = generatedMeta
		}
		meta.PackageVersion = versionFlagValue

		// --- Step 3: git introspection (org, remote URL, exact commit) ---
		orgFromGit, _, errGit := gitutils.GetGitRemoteOriginInfo(absSourcePath)
		if errGit != nil {
			if isNotFoundErr(errGit) {
				fmt.Fprintf(cmd.ErrOrStderr(), "Info: no git remote 'origin' found or not a git repo: %s\n", absSourcePath)
			} else {
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not determine org/repo from git: %v\n", errGit)
			}
		}

		finalOrg := meta.Org
		if orgFromGit != "" {
			finalOrg = orgFromGit
		}
		if orgFlagValue != "" {
			finalOrg = orgFlagValue
		}
		meta.Org = finalOrg
		meta.AppName = appModule
		meta.PackageName = appModule

		fullGitURL, errGitURL := gitutils.GetFullGitRemoteOriginURL(absSourcePath)
		if errGitURL != nil && !isNotFoundErr(errGitURL) {
			fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not determine full git remote URL: %v\n", errGitURL)
		}
		meta.SourceControlURL = fullGitURL
		meta.PackageType = packageType

		if commit, errCommit := gitutils.ResolveHeadCommit(absSourcePath); errCommit == nil {
			meta.CommitSHA = commit.SHA
			meta.GitRef = commit.Ref
			meta.GitDirty = commit.Dirty
			state := ""
			if commit.Dirty {
				state = " (working tree has uncommitted changes)"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Source commit: %s%s\n", commit.SHA, state)
		} else {
			// A checkout whose commit cannot be resolved still packages; the package
			// simply carries no commit identity, which `fpm exists --commit` reports as
			// "no commit recorded" rather than matching anything.
			meta.CommitSHA, meta.GitRef, meta.GitDirty = "", "", false
			if isNotFoundErr(errCommit) {
				fmt.Fprintf(cmd.ErrOrStderr(), "Info: not a git checkout; no commit SHA will be recorded.\n")
			} else {
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not resolve the source commit; no commit SHA will be recorded: %v\n", errCommit)
			}
		}

		// --- Step 4: required_apps → pinned package dependencies ---
		requiredEntries, err := apputils.GetRequiredAppsFromHooks(hooksFilePath)
		if err != nil {
			return fmt.Errorf("failed to read required_apps from %s: %w", hooksFilePath, err)
		}
		meta.RequiredApps = nil
		if len(requiredEntries) > 0 {
			cfg, cfgErr := config.InitConfig()
			if cfgErr != nil {
				return fmt.Errorf("failed to initialize FPM configuration to resolve required_apps: %w", cfgErr)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Resolving required_apps from hooks.py: %s\n", strings.Join(requiredEntries, ", "))
			// Resolved against the local store, then the build bench (an app already
			// there — from an image, say — pins to the version it carries), then
			// repositories.
			pins, resolveErr := resolver.ResolveRequiredApps(requiredEntries, resolver.Options{
				Cfg: cfg, Remote: true, Repo: packageRepo, BenchPath: packageBenchPath,
				// An OCI registry publishes no index, so an unqualified entry like
				// "erpnext" has nothing to resolve the org from. This package's own org
				// is the right assumption.
				DefaultOrg: meta.Org,
			})
			if resolveErr != nil {
				return resolveErr
			}
			meta.RequiredApps = pins
			if meta.Dependencies == nil {
				meta.Dependencies = map[string]string{}
			}
			for _, pin := range pins {
				meta.Dependencies[pin.Org+"/"+pin.Name] = pin.Version
				fmt.Fprintf(cmd.OutOrStdout(), "  %s -> %s (%s)\n", pin.Requirement, pin.Identifier(), pin.ResolvedFrom)
			}
		}

		// --- Step 5: output path ---
		outputFileName := fmt.Sprintf("%s-%s.fpm", meta.AppName, meta.PackageVersion)
		absOutputPath, err := filepath.Abs(packageOutputPath)
		if err != nil {
			return fmt.Errorf("failed to get absolute output path: %w", err)
		}
		finalFpmFilePath := filepath.Join(absOutputPath, outputFileName)

		if _, err := os.Stat(finalFpmFilePath); err == nil && !packageOverwrite {
			return fmt.Errorf("output file '%s' already exists. Use --overwrite to replace it", finalFpmFilePath)
		}

		// --- Step 6: wheel target ---
		// A production package is a deployment artifact, so it bundles its dependencies
		// by default and installs without network access. Development packages are for
		// local iteration, where bundling only slows the loop, so they default to off.
		// An explicit --bundle-deps=true/false overrides either default.
		bundleDeps := packageBundleDeps
		if !cmd.Flags().Changed("bundle-deps") {
			bundleDeps = wheels.DefaultBundleForPackageType(meta.PackageType)
		}
		target, err := wheelTargetFromFlags(cmd, meta.PackageType)
		if err != nil {
			return err
		}

		// --- Step 7: build assets inside a bench ---
		// The build runs in <bench>/apps/<app> (the source itself when it already lives
		// there, otherwise a staged copy), and the package is created from that tree so
		// it carries everything the build produced.
		meta.AssetsBuilt = false
		meta.AssetBundles = nil
		packageFrom := absSourcePath
		if packageBenchPath != "" {
			result, buildErr := benchbuild.Build(benchbuild.Options{
				BenchPath:  packageBenchPath,
				AppName:    meta.AppName,
				SourcePath: absSourcePath,
				Verbose:    packageBuildVerbose,
				Stdout:     cmd.OutOrStdout(),
			})
			if buildErr != nil {
				return buildErr
			}
			defer result.Cleanup()
			meta.AssetsBuilt = true
			meta.AssetBundles = result.Bundles
			packageFrom = result.BuildRoot
		}

		// --- Step 7b: build the app's JavaScript frontend ---
		// Apps like frappe/crm, frappe/helpdesk and frappe/insights ship a Vite SPA
		// whose output — <app>/public/frontend and the <app>/www/<app>.html route it
		// is rendered from — is listed in the app's own .gitignore and is never
		// produced by frappe's esbuild, which only globs *.bundle.*. Without this step
		// the package installs cleanly and then serves a blank page.
		//
		// It runs in packageFrom, which with --bench-path is the staged copy inside
		// <bench>/apps/<app>: a frappe-ui frontend resolves the bench from its own
		// physical path (../../../sites), which only holds there.
		meta.FrontendBuilt = false
		meta.FrontendDirs, meta.FrontendRoutes, meta.FrontendSource = nil, nil, ""
		if packageBuildFrontend {
			fe, feErr := frontend.Build(frontend.BuildOptions{
				SourcePath:     packageFrom,
				AppName:        meta.AppName,
				Verbose:        packageBuildVerbose,
				Stdout:         cmd.OutOrStdout(),
				Timeout:        packageFrontendTimeout,
				SiteConfigPath: packageFrontendSiteConfig,
				NoScaffold:     packageNoBenchScaffold,
			})
			if fe.Cleanup != nil {
				defer fe.Cleanup()
			}
			if feErr != nil {
				return feErr
			}
			if fe.Built {
				meta.FrontendBuilt = true
				meta.FrontendDirs = fe.Output.Dirs
				meta.FrontendRoutes = fe.Output.Routes
				if fe.Project != nil {
					meta.FrontendSource = fe.Project.Rel
				}
				// A frontend that needed a bench was built in a staged copy, and that
				// copy is where its output landed. The package must be created from
				// there or it would carry none of it.
				if fe.BuildRoot != "" {
					packageFrom = fe.BuildRoot
				}
			}
		} else if project, detectErr := frontend.Detect(packageFrom, meta.AppName); detectErr == nil && project != nil {
			fmt.Fprintf(cmd.OutOrStdout(),
				"Skipping the frontend build in %s (--build-frontend=false): the package will not carry %s/public/frontend "+
					"and the app will serve a blank page unless the checkout already holds a compiled frontend.\n",
				project.Rel, meta.AppName)
		}

		// --- Step 7c: record whatever classic bundles are on disk ---
		// Covers a checkout that was built by hand outside fpm, and a frontend build
		// that also emitted *.bundle.* files into <app>/public/dist.
		if len(meta.AssetBundles) == 0 {
			appModuleDir := filepath.Join(packageFrom, meta.AppName)
			if _, statErr := os.Stat(filepath.Join(appModuleDir, "public", "dist")); os.IsNotExist(statErr) {
				if _, rootDistErr := os.Stat(filepath.Join(packageFrom, "public", "dist")); rootDistErr == nil {
					appModuleDir = packageFrom
				}
			}
			ltr, rtl, scanErr := assets.Bundles(appModuleDir, meta.AppName)
			if scanErr == nil && (len(ltr) > 0 || len(rtl) > 0) {
				bundles := make(map[string]string, len(ltr)+len(rtl))
				for k, v := range ltr {
					bundles[k] = v
				}
				for k, v := range rtl {
					bundles[k] = v
				}
				meta.AssetsBuilt = true
				meta.AssetBundles = bundles
				fmt.Fprintf(cmd.OutOrStdout(), "Discovered %d prebuilt asset bundle(s) in %s/public/dist\n", len(bundles), meta.AppName)
			}
		}

		// --- Step 8: archive ---
		fmt.Printf("Packaging '%s' version '%s' from '%s'...\n", meta.PackageName, meta.PackageVersion, packageFrom)
		err = archive.CreateFPMArchive(packageFrom, absOutputPath, meta, meta.PackageVersion, archive.Options{
			BundleDeps:  bundleDeps,
			WheelTarget: target,
		})
		if err != nil {
			return fmt.Errorf("failed to create package: %w", err)
		}
		fmt.Printf("Successfully packaged: %s\n", finalFpmFilePath)

		if !packageSkipLocalInstall {
			fmt.Println("Attempting to install package to local FPM app store...")
			cfg, err := config.InitConfig()
			if err != nil {
				return fmt.Errorf("failed to initialize FPM configuration for local install: %w", err)
			}
			if meta.Org == "" || meta.AppName == "" || meta.PackageVersion == "" {
				return fmt.Errorf("metadata (Org, AppName, Version) incomplete for local store install. Org: '%s', AppName: '%s', Version: '%s'", meta.Org, meta.AppName, meta.PackageVersion)
			}
			org, name, version, storePath, _, storeErr := appstore.ManageAppInLocalStore(finalFpmFilePath, cfg)
			if storeErr != nil {
				return fmt.Errorf("failed to install package to local FPM store: %w", storeErr)
			}
			fmt.Printf("Successfully installed (extracted) package %s/%s version %s to local FPM store: %s\n", org, name, version, storePath)
		} else {
			fmt.Println("Skipping installation to local FPM app store.")
		}

		// --with-deps: also export the package with every app it transitively requires,
		// each once, into <output>/<app>-<version>-bundle/, ready to copy to an offline
		// bench and `fpm install` as one unit.
		if packageWithDeps {
			cfg, err := config.InitConfig()
			if err != nil {
				return fmt.Errorf("failed to initialize FPM configuration for --with-deps: %w", err)
			}
			bundleDir := filepath.Join(absOutputPath, fmt.Sprintf("%s-%s-bundle", meta.AppName, meta.PackageVersion))
			manifest, err := exportBundle(finalFpmFilePath, bundleDir, cfg, true, packageRepo, packageBenchPath)
			if err != nil {
				return fmt.Errorf("failed to export dependency bundle: %w", err)
			}
			printBundle(cmd, bundleDir, manifest)
		}
		return nil
	},
}

// wheelTargetFromFlags builds the wheel target. Production packages target amd64
// Linux by default, which is rarely the packaging machine, so they cross-build
// unless --platform says otherwise; other package types build for the host.
// A cross-build's interpreter version is never guessed from the host: when the
// app has dependencies to vendor, --python-version is required (enforced by the
// bundler, so an app with nothing to vendor needs no flag).
func wheelTargetFromFlags(cmd *cobra.Command, packageType string) (wheels.Target, error) {
	target := wheels.Target{
		Platforms:      append([]string(nil), packagePlatforms...),
		PythonVersion:  packagePythonVersion,
		Implementation: packageImplementation,
		ABIs:           append([]string(nil), packageABIs...),
	}
	if len(target.Platforms) == 0 {
		if p := wheels.PlatformForPackageType(packageType); p != "" {
			target.Platforms = []string{p}
		}
	}
	// "--platform host" is the explicit way to ask for a host build of a prod package.
	if len(target.Platforms) == 1 && target.Platforms[0] == wheels.HostPlatformTag {
		target.Platforms = nil
	}
	if target.IsHost() && target.PythonVersion != "" {
		return wheels.Target{}, fmt.Errorf("--python-version only applies with --platform: a host build uses the packaging host's own interpreter")
	}
	if !target.IsHost() && target.PythonVersion != "" {
		if err := target.Validate(); err != nil {
			return wheels.Target{}, err
		}
	}
	return target, nil
}

// isNotFoundErr reports whether a git/hooks lookup failed because the file simply
// is not there, which is informational, as opposed to a real failure.
func isNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "not found") || strings.Contains(msg, "no such file or directory")
}

func init() {
	rootCmd.AddCommand(packageCmd)
	packageCmd.Flags().StringVarP(&packageOutputPath, "output-path", "o", ".", "Directory to save the .fpm file")
	packageCmd.Flags().StringVarP(&packageVersion, "version", "v", "", "Package version (e.g., 1.0.0) (required)")
	packageCmd.Flags().BoolVar(&packageOverwrite, "overwrite", false, "Overwrite if .fpm file already exists")
	packageCmd.Flags().BoolVar(&packageSkipLocalInstall, "skip-local-install", false, "Skip installing the package to the local FPM app store after packaging.")
	packageCmd.Flags().String("org", "", "GitHub organization or similar identifier for the app (overrides auto-detection)")
	packageCmd.Flags().String("app-name", "", "Actual Frappe app name (e.g., erpnext, my_custom_app) (overrides auto-detection)")
	packageCmd.Flags().StringVar(&packageType, "package-type", "prod", "Package type (prod|dev)")
	packageCmd.Flags().BoolVar(&packageBundleDeps, "bundle-deps", true, "Bundle Python dependencies from requirements.txt/pyproject.toml into the package so it installs without network access (default true for prod packages, false for dev; use --bundle-deps=false to disable)")
	packageCmd.Flags().StringArrayVar(&packagePlatforms, "platform", nil, "Wheel platform tag of the destination bench, e.g. manylinux2014_x86_64 or manylinux_2_28_aarch64; repeat to accept several (default: "+wheels.DefaultProdPlatform+" for prod packages, packaging host otherwise; pass 'host' to build for the packaging host)")
	packageCmd.Flags().StringVar(&packagePythonVersion, "python-version", "", "Python version of the destination bench, e.g. 3.11. Required with --platform when the app has dependencies to vendor; never guessed from the packaging host")
	packageCmd.Flags().StringVar(&packageImplementation, "implementation", wheels.DefaultImplementation, "Python implementation tag of the destination bench (cp for CPython)")
	packageCmd.Flags().StringArrayVar(&packageABIs, "abi", nil, "Restrict vendored wheels to these ABI tags (e.g. cp311, abi3); repeatable. Default: derived from --python-version by pip")
	packageCmd.Flags().StringVar(&packageBenchPath, "bench-path", "", "Path to a Frappe bench with node/yarn available: runs 'bench build --app <app> --production' so the package ships compiled JS/CSS")
	packageCmd.Flags().BoolVar(&packageBuildVerbose, "build-verbose", false, "Stream the asset and frontend build output instead of showing it only on failure")
	packageCmd.Flags().BoolVar(&packageBuildFrontend, "build-frontend", true, "Compile the app's JavaScript frontend (the Vite SPA that apps like frappe/crm build into <app>/public/frontend) when the checkout declares one. Use --build-frontend=false to package without it")
	packageCmd.Flags().DurationVar(&packageFrontendTimeout, "frontend-timeout", frontend.DefaultTimeout, "Time limit for the frontend dependency install and for the frontend build, each")
	packageCmd.Flags().StringVar(&packageFrontendSiteConfig, "frontend-site-config", "", "A bench's sites/common_site_config.json to build the frontend against. Apps like frappe/crm compile socketio_port into their bundle; without this a default bench config is synthesized (see --help output during the build)")
	packageCmd.Flags().BoolVar(&packageNoBenchScaffold, "no-bench-scaffold", false, "Fail instead of building a bench-resolving frontend in a temporary bench. Use when the package must be built only against a real bench (--bench-path or a checkout already at <bench>/apps/<app>)")
	packageCmd.Flags().StringVar(&packageRepo, "repo", "", "Configured repository to resolve required_apps against (default: the local store, then every configured repository by priority)")
	packageCmd.Flags().BoolVar(&packageWithDeps, "with-deps", false, "Also write <output-path>/<app>-<version>-bundle/: this package plus every package it transitively requires (each once) with an install-order manifest, for 'fpm install <dir>' on an offline bench")
}
