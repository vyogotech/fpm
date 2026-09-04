package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"fpm/internal/appstore"
	"fpm/internal/apputils"
	"fpm/internal/archive"
	"fpm/internal/assetbench"
	"fpm/internal/assets"
	"fpm/internal/benchbuild"
	"fpm/internal/config"
	"fpm/internal/frontend"
	"fpm/internal/gitutils"
	"fpm/internal/metadata"
	"fpm/internal/resolver"
	"fpm/internal/semver"
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
	packageOutputPath         string
	packageVersion            string
	packageOverwrite          bool
	packageType               string
	packageSkipLocalInstall   bool
	packageBundleDeps         bool
	packagePlatforms          []string
	packagePythonVersion      string
	packageImplementation     string
	packageABIs               []string
	packageBenchPath          string
	packageBuildVerbose       bool
	packageRepos              []string
	packageRequires           []string
	packageRequiresFromStore  bool
	packageExactRequires      bool
	packageAllowUnbuiltAssets bool
	packageOverrideDeps       []string
	packageWithDeps           bool
	packageBuildFrontend      bool
	packageFrontendTimeout    time.Duration

	packageFrontendSiteConfig string
	packageNoBenchScaffold    bool
	packageFrappeRef          string
	packageBuildAssets        bool
)

var packageCmd = &cobra.Command{
	Use:   "package [source-path]",
	Short: "Package a Frappe application into an .fpm file",
	Long: `Packages a Frappe application from a local development directory into an .fpm file.

The source is validated as a Frappe app before anything else happens, so a checkout
that is not a Frappe app is rejected immediately with exit code ` + fmt.Sprint(ExitNotFrappeApp) + `.

The package records the exact git commit it was built from, resolves the app's
required_apps (hooks.py) to pinned packages, vendors Python dependencies as wheels
for the destination platform (--platform/--python-version), and compiles the app's
assets so the package ships them: the Vite SPA an app such as frappe/crm builds into
<app>/public/frontend, and the classic *.bundle.* desk entry points frappe's esbuild
compiles into <app>/public/dist. The desk bundles need frappe's asset pipeline, which
fpm fetches and caches itself; --bench-path builds them in a bench you already have
instead, and --no-bench-scaffold refuses to build either without one.

required_apps are pinned from a source you name — --requires, --repo, or the bench
given by --bench-path — because the packaging host's own FPM store holds whatever
was packaged on this machine, whenever that happened; a prod package pinned from it
is not reproducible. Pass --requires-from-local-store to use it anyway. Each pin is
recorded as the release line of the version it resolved to (>=16.0.0-0,<17.0.0), so
a patch upgrade of a dependency does not invalidate this package and two apps
needing the same dependency stay co-installable; --requires-exact pins one version.

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

		// Extract metadata from hooks.py
		hooksMeta, errHooks := apputils.GetAppMetadataFromHooks(hooksFilePath)
		if errHooks == nil && hooksMeta != nil {
			if meta.Title == "" && hooksMeta.AppTitle != "" {
				meta.Title = hooksMeta.AppTitle
			}
			if meta.Description == "" && hooksMeta.AppDescription != "" {
				meta.Description = hooksMeta.AppDescription
			}
			if meta.Publisher == "" && hooksMeta.AppPublisher != "" {
				meta.Publisher = hooksMeta.AppPublisher
			}
			if meta.Author == "" && hooksMeta.AppPublisher != "" {
				meta.Author = hooksMeta.AppPublisher
			}
			if meta.Email == "" && hooksMeta.AppEmail != "" {
				meta.Email = hooksMeta.AppEmail
			}
			if meta.License == "" && hooksMeta.AppLicense != "" {
				meta.License = hooksMeta.AppLicense
			}
		}

		if meta.Icon == "" {
			meta.Icon = detectAppIcon(absSourcePath, appModule, hooksMeta)
		}

		// Fallback to pyproject.toml if present
		pyprojectPath := filepath.Join(absSourcePath, wheels.PyProjectFileName)
		if pyMeta, errPy := wheels.ExtractPyProjectMetadata(pyprojectPath); errPy == nil && pyMeta != nil {
			if meta.Description == "" && pyMeta.Description != "" {
				meta.Description = pyMeta.Description
			}
			if meta.Author == "" && pyMeta.AuthorName != "" {
				meta.Author = pyMeta.AuthorName
			}
			if meta.Publisher == "" && pyMeta.AuthorName != "" {
				meta.Publisher = pyMeta.AuthorName
			}
			if meta.Email == "" && pyMeta.AuthorEmail != "" {
				meta.Email = pyMeta.AuthorEmail
			}
			if meta.License == "" && pyMeta.License != "" {
				meta.License = pyMeta.License
			}
		}

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
			overrides, ovErr := parseRequiresOverrides(packageRequires)
			if ovErr != nil {
				return ovErr
			}
			if unused := resolver.UnmatchedOverrides(requiredEntries, overrides); len(unused) > 0 {
				return fmt.Errorf("--requires %s names an app this checkout does not require; "+
					"%s declares required_apps = %v", strings.Join(unused, ", "), hooksFilePath, requiredEntries)
			}
			useLocalStore, srcErr := requiresSourcePolicy(cfg, meta.PackageType, requiredEntries, overrides)
			if srcErr != nil {
				return srcErr
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Resolving required_apps from hooks.py: %s\n", strings.Join(requiredEntries, ", "))
			pins, resolveErr := resolver.ResolveRequiredApps(requiredEntries, resolver.Options{
				Cfg: cfg, Remote: true, Repos: packageRepos, BenchPath: packageBenchPath,
				Overrides:      overrides,
				SkipLocalStore: !useLocalStore,
				// Record the release line rather than one exact version, so a patch
				// upgrade of a dependency does not invalidate this package and two
				// packages needing the same dependency stay co-installable.
				ReleaseLine: !packageExactRequires,
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
				constraint := pin.VersionSpec
				if constraint == "" {
					constraint = pin.Version
				}
				meta.Dependencies[pin.Org+"/"+pin.Name] = constraint
				fmt.Fprintf(cmd.OutOrStdout(), "  %s -> %s (%s)\n", pin.Requirement, pin.Describe(), pin.ResolvedFrom)
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

		meta.AssetsBuilt = false
		meta.AssetBundles = nil
		packageFrom := absSourcePath

		// --- Step 6b: settle on a bench, and stage into it before anything is built ---
		// Both builds want to run at <bench>/apps/<app>. The frontend build wants it
		// because an app may reach out of its own tree: helpdesk's desk/package.json
		// declares `"@framework/ui": "link:../../frappe/ui"`, which only resolves when
		// the checkout has frappe as a sibling. The asset build wants it because that
		// is where frappe's esbuild globs for entry points.
		//
		// Whether a bench is needed at all is decided from what the checkout declares
		// before either build: esbuild entry points that are already committed, or a
		// build script that reads a sibling app. An app that generates its entry points
		// during its own build (frappe/wiki) is caught after the frontend build instead,
		// in step 7b — there is no way to know earlier, and staging the built tree then
		// costs one copy rather than a wasted frappe fetch for every SPA-only app.
		assetBenchPath := packageBenchPath
		if assetBenchPath == "" {
			// A checkout that already sits at <bench>/apps/<app> brings its own bench,
			// and using it beats both staging a copy elsewhere and fetching a second
			// frappe. Every `fpm mirror` checkout is such a tree, which is what keeps a
			// catalogue run from scaffolding anything.
			assetBenchPath = enclosingBench(absSourcePath)
		}
		if assetBenchPath == "" && packageBuildAssets && !packageNoBenchScaffold && hasBundleSources(absSourcePath, meta.AppName) {
			assetBenchPath = ensureAssetBench(cmd, meta.AppName, frappeRefForApp(cmd, meta.GitRef))
		}
		if assetBenchPath != "" {
			staged, stageErr := benchbuild.Stage(assetBenchPath, meta.AppName, absSourcePath, cmd.OutOrStdout())
			if stageErr != nil {
				return stageErr
			}
			defer staged.Cleanup()
			packageFrom = staged.BuildRoot
		}
		warnAboutSiblings(cmd, packageFrom, meta.AppName)

		// --- Step 7: build the app's JavaScript frontend ---
		// Apps like frappe/crm, frappe/helpdesk and frappe/insights ship a Vite SPA
		// whose output — <app>/public/frontend and the <app>/www/<app>.html route it
		// is rendered from — is listed in the app's own .gitignore and is never
		// produced by frappe's esbuild, which only globs *.bundle.*. Without this step
		// the package installs cleanly and then serves a blank page.
		//
		// It runs in the checkout itself. A frappe-ui frontend resolves the bench from
		// its own physical path (../../../../sites); internal/frontend satisfies that
		// where the checkout stands rather than moving it, and --bench-path's own site
		// config is handed to it below so the values compiled in are that bench's.
		meta.FrontendBuilt = false
		meta.FrontendDirs, meta.FrontendRoutes, meta.FrontendSource = nil, nil, ""
		if packageBuildFrontend {
			fe, feErr := frontend.Build(frontend.BuildOptions{
				SourcePath:     packageFrom,
				AppName:        meta.AppName,
				Verbose:        packageBuildVerbose,
				Stdout:         cmd.OutOrStdout(),
				Timeout:        packageFrontendTimeout,
				SiteConfigPath: frontendSiteConfig(packageFrontendSiteConfig, packageBenchPath),
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

		// --- Step 7b: compile the classic desk bundles ---
		// This runs after the frontend build, not before, because an app's esbuild entry
		// points are not always checked in: frappe/wiki generates
		// wiki/public/js/wiki-highlight.bundle.js from its own `yarn build` (it is in
		// wiki's .gitignore). Compiling first globbed an entry-point set that did not
		// exist yet, so the bundle was shipped as source and the package's code
		// highlighting never loaded — while the error told the user to package against a
		// bench, which is what they had just done.
		//
		// The bench is fetched rather than demanded. --bench-path still wins, and names
		// the bench whose frappe (and whose site config) the package is compiled against;
		// without it fpm materialises the little that frappe's esbuild needs, cached
		// under the fpm build cache. Before this, an app with bundle sources and no bench
		// to hand could only be packaged with --allow-unbuilt-assets, which ships exactly
		// the unusable artifact that flag's own help text warns about.
		// An app whose entry points its own build wrote (frappe/wiki) only becomes a
		// candidate for a bench now. Staging happens inside benchbuild.Build below, and
		// carries the frontend output along with it.
		if assetBenchPath == "" && packageBuildAssets && !packageNoBenchScaffold && hasBundleSources(packageFrom, meta.AppName) {
			assetBenchPath = ensureAssetBench(cmd, meta.AppName, frappeRefForApp(cmd, meta.GitRef))
		}
		// Only when there is something to compile. An app with no *.bundle.* entry
		// points — lms 2.62 and every SPA-only app — has nothing for frappe's esbuild to
		// do, and running it anyway cost a yarn install and left the package claiming
		// assets_built with an empty bundle map, which reads as "the desk assets were
		// built" when what happened is that there were none.
		if packageBuildAssets && assetBenchPath != "" && hasBundleSources(packageFrom, meta.AppName) {
			// The build runs in <bench>/apps/<app> (the source itself when it already
			// lives there, otherwise a staged copy), and the package is created from
			// that tree so it carries everything the build produced.
			result, buildErr := benchbuild.Build(benchbuild.Options{
				BenchPath:  assetBenchPath,
				AppName:    meta.AppName,
				SourcePath: packageFrom,
				Verbose:    packageBuildVerbose,
				Stdout:     cmd.OutOrStdout(),
			})
			if buildErr != nil {
				return buildErr
			}
			defer result.Cleanup()
			meta.AssetsBuilt = true
			meta.AssetBundles = result.Bundles
			meta.AssetBuildFrappeCommit = assetbench.FrappeCommit(assetBenchPath)
			if packageBenchPath == "" {
				// Only a bench fpm resolved has a ref to name; one the caller supplied is
				// whatever they checked out, and the commit says it exactly.
				meta.AssetBuildFrappeRef = frappeRefForApp(cmd, meta.GitRef)
			}
			packageFrom = result.BuildRoot
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

		// --- Step 7d: refuse to ship a desk UI that was never compiled ---
		// An app with esbuild entry points and no built bundles installs cleanly and
		// then renders nothing, because there is no runtime build on a bench that
		// installs from a package. That is what made every front-end package in the
		// published catalogue unusable (issue #9), and it was only visible as a line
		// in the install log.
		if err := checkAssetsBuilt(cmd, meta, packageFrom); err != nil {
			return err
		}

		// --- Step 8: archive ---
		fmt.Printf("Packaging '%s' version '%s' from '%s'...\n", meta.PackageName, meta.PackageVersion, packageFrom)
		err = archive.CreateFPMArchive(packageFrom, absOutputPath, meta, meta.PackageVersion, archive.Options{
			BundleDeps:          bundleDeps,
			WheelTarget:         target,
			DependencyOverrides: packageOverrideDeps,
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
			manifest, err := exportBundle(finalFpmFilePath, bundleDir, cfg, true, firstOrEmpty(packageRepos), packageBenchPath)
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

// parseRequiresOverrides turns `--requires org/app==16.30.0` (or a range, or a
// bare name) into pins that resolution honours before consulting any source.
func parseRequiresOverrides(values []string) ([]metadata.RequiredApp, error) {
	var out []metadata.RequiredApp
	seen := map[string]string{}
	for _, raw := range values {
		name, spec := semver.SplitRequirement(raw)
		if name == "" {
			return nil, fmt.Errorf("--requires %q names no app; expected [<org>/]<app>[==<version> | <range>]", raw)
		}
		org := ""
		if parts := strings.SplitN(name, "/", 2); len(parts) == 2 {
			org, name = strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		}
		if name == "" {
			return nil, fmt.Errorf("--requires %q names no app after the org", raw)
		}
		if previous, dup := seen[name]; dup {
			return nil, fmt.Errorf("--requires names %s twice (%q and %q); it can only be pinned once", name, previous, raw)
		}
		seen[name] = raw

		constraint, err := semver.ParseConstraint(spec)
		if err != nil {
			return nil, fmt.Errorf("--requires %q: %w", raw, err)
		}
		pin := metadata.RequiredApp{Name: name, Org: org}
		if exact, isExact := constraint.ExactVersion(); isExact {
			pin.Version = exact
		} else if !constraint.Any() {
			pin.VersionSpec = constraint.String()
		}
		out = append(out, pin)
	}
	return out, nil
}

// requiresSourcePolicy decides whether the packaging host's own FPM store may
// answer a requirement, and refuses to guess for a production package.
//
// The store holds whatever happens to have been packaged on this machine, so a
// prod package that pins from it is not reproducible: the same source, built on
// another machine or on another day, produces a package demanding a different
// version of its dependency — and a bench holds one copy of each app, so two
// such packages cannot be co-installed. A production build therefore has to name
// its source: a repository, the bench it targets, or the pin itself.
func requiresSourcePolicy(cfg *config.FPMConfig, packageType string, entries []string, overrides []metadata.RequiredApp) (useLocalStore bool, err error) {
	if packageRequiresFromStore {
		return true, nil
	}
	if packageType != "prod" {
		// A dev package is a local iteration artifact; the local store is the point.
		return true, nil
	}
	if len(packageRepos) > 0 || packageBenchPath != "" {
		return false, nil
	}

	// Every requirement pinned by hand needs no source at all.
	pinned := map[string]bool{}
	for _, ov := range overrides {
		pinned[ov.Name] = true
	}
	var unpinned []string
	for _, entry := range entries {
		name := apputils.ParseRequiredAppName(entry)
		if name == "" || name == resolver.FrappeAppName || pinned[name] {
			continue
		}
		// Suggest the qualified name when hooks.py gave one, so the --requires in
		// the message can be pasted as it stands.
		if org := apputils.ParseRequiredAppOrg(entry); org != "" {
			name = org + "/" + name
		}
		unpinned = append(unpinned, name)
	}
	if len(unpinned) == 0 {
		return false, nil
	}

	if len(config.ListRepositories(cfg)) > 0 {
		// A configured repository is a shared, auditable source, and the pin it
		// yields is recorded with the repository it came from.
		return false, nil
	}

	choices := [][2]string{
		{fmt.Sprintf("--requires %s==<version>", unpinned[0]), "pin it exactly"},
		{fmt.Sprintf("--requires '%s>=16.0.0,<17.0.0'", unpinned[0]), "accept a release line"},
		{"--repo <name>", "resolve against a configured repository ('fpm repo add' first)"},
		{"--bench-path <bench>", "pin to the bench this package is built against"},
		{"--requires-from-local-store", "use this host's store anyway (not reproducible)"},
		{"--package-type dev", "a development package, where the local store is the point"},
	}
	width := 0
	for _, c := range choices {
		if len(c[0]) > width {
			width = len(c[0])
		}
	}
	lines := make([]string, 0, len(choices))
	for _, c := range choices {
		lines = append(lines, fmt.Sprintf("  %-*s  %s", width, c[0], c[1]))
	}

	return false, fmt.Errorf("cannot pin required_apps (%s) for a prod package without a resolution source.\n"+
		"The packaging host's local FPM store is ambient state — what it holds depends on when and where this build ran — "+
		"so a package pinned from it is not reproducible and may not co-install with other packages needing the same app.\n"+
		"Choose one:\n%s",
		strings.Join(unpinned, ", "), strings.Join(lines, "\n"))
}

// checkAssetsBuilt fails a production package whose app declares esbuild entry points
// that nothing compiled.
//
// `fpm package` only builds assets when it is given a bench (--bench-path), and
// without one the package is created from whatever the checkout holds — which for a
// fresh clone is sources and no dist. Such a package installs, links its assets
// directory, and serves nothing: Frappe's desk loads the bundles named in
// sites/assets/assets.json, and this package contributes none. A development package
// is a different matter — it is iterated on inside a bench that can build — so it only
// warns.
func checkAssetsBuilt(cmd *cobra.Command, meta *metadata.AppMetadata, packageFrom string) error {
	if len(meta.AssetBundles) > 0 {
		return nil
	}
	moduleDir := filepath.Join(packageFrom, meta.AppName)
	sources, err := benchbuild.BundleSources(moduleDir)
	if err != nil || len(sources) == 0 {
		return nil
	}

	shown := sources
	if len(shown) > 3 {
		shown = shown[:3]
	}
	summary := fmt.Sprintf("'%s' declares %d esbuild entry point(s) (%s) but the package carries no compiled bundles in %s/public/dist",
		meta.AppName, len(sources), strings.Join(shown, ", "), meta.AppName)

	if meta.PackageType != "prod" || packageAllowUnbuiltAssets {
		fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %s. Installing it will not render the app's desk UI until 'bench build --app %s' is run on the bench.\n",
			summary, meta.AppName)
		return nil
	}
	// The remedy depends on why nothing compiled them. Telling someone who passed
	// --bench-path to pass --bench-path is what this error used to do.
	remedy := "Build them by letting fpm fetch frappe's asset pipeline (drop --no-bench-scaffold), " +
		"by packaging against a bench (--bench-path <bench>), " +
		"or pass --allow-unbuilt-assets to publish a package whose desk UI has to be built on the destination"
	switch {
	case packageBenchPath != "":
		remedy = fmt.Sprintf("The bench at %s ran frappe's asset build and produced none of them, "+
			"which usually means its frappe is too old for this app's bundle sources or the build failed silently. "+
			"Package with --build-verbose to see it, or pass --allow-unbuilt-assets to publish a package "+
			"whose desk UI has to be built on the destination", packageBenchPath)
	case packageNoBenchScaffold:
		remedy = "Drop --no-bench-scaffold to let fpm fetch frappe's asset pipeline and compile them, " +
			"pass --bench-path <bench> to build against a bench you already have, " +
			"or pass --allow-unbuilt-assets to publish a package whose desk UI has to be built on the destination"
	}
	return fmt.Errorf("%w: %s.\n"+
		"A bench that installs from a package does not build assets, so this package would install and then serve nothing.\n"+
		"%s",
		benchbuild.ErrBuildFailed, summary, remedy)
}

// firstOrEmpty is the first element of a repeatable flag's values, for the callees
// that address one repository at a time.
func firstOrEmpty(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
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

// detectAppIcon checks hooks metadata and filesystem conventions for an app icon.
func detectAppIcon(sourceDir, appModule string, hooksMeta *apputils.HooksMetadata) string {
	if hooksMeta != nil {
		if hooksMeta.AppIcon != "" {
			return hooksMeta.AppIcon
		}
		if hooksMeta.AppLogoUrl != "" {
			return hooksMeta.AppLogoUrl
		}
	}

	candidates := []struct {
		relPath   string
		assetPath string
	}{
		{filepath.Join(appModule, "public", "images", appModule+".svg"), fmt.Sprintf("/assets/%s/images/%s.svg", appModule, appModule)},
		{filepath.Join(appModule, "public", "images", appModule+".png"), fmt.Sprintf("/assets/%s/images/%s.png", appModule, appModule)},
		{filepath.Join(appModule, "public", "images", "icon.svg"), fmt.Sprintf("/assets/%s/images/icon.svg", appModule)},
		{filepath.Join(appModule, "public", "images", "icon.png"), fmt.Sprintf("/assets/%s/images/icon.png", appModule)},
		{filepath.Join(appModule, "public", "images", "logo.svg"), fmt.Sprintf("/assets/%s/images/logo.svg", appModule)},
		{filepath.Join(appModule, "public", "images", "logo.png"), fmt.Sprintf("/assets/%s/images/logo.png", appModule)},
		{filepath.Join(appModule, "public", "icon.svg"), fmt.Sprintf("/assets/%s/icon.svg", appModule)},
		{filepath.Join(appModule, "public", "icon.png"), fmt.Sprintf("/assets/%s/icon.png", appModule)},
		{filepath.Join(appModule, "public", "logo.svg"), fmt.Sprintf("/assets/%s/logo.svg", appModule)},
		{filepath.Join(appModule, "public", "logo.png"), fmt.Sprintf("/assets/%s/logo.png", appModule)},
	}

	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(sourceDir, c.relPath)); err == nil {
			return c.assetPath
		}
	}
	return ""
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
	packageCmd.Flags().StringVar(&packageBenchPath, "bench-path", "", "Path to a Frappe bench with node/yarn available: runs 'bench build --app <app> --production' so the package ships compiled JS/CSS. Without it, an app that declares esbuild entry points gets frappe's asset pipeline fetched into "+defaultBuildCacheHint()+" and compiled there")
	packageCmd.Flags().StringVar(&packageFrappeRef, "frappe-ref", assetbench.DefaultFrappeRef, "The frappe ref whose esbuild compiles this app's desk bundles when fpm fetches the asset pipeline itself. When this is not set and the checkout is on a frappe release line (a version-NN branch, as erpnext's and hrms's are), that line is used instead of the default, because it is the frappe the app is written against. Ignored with --bench-path, which uses that bench's frappe. Either way the frappe used is recorded in the package as asset_build_frappe_ref/_commit")
	packageCmd.Flags().BoolVar(&packageBuildAssets, "build-assets", true, "Compile the app's classic *.bundle.* desk entry points with frappe's esbuild. Use --build-assets=false to package without them — the counterpart to --build-frontend=false, and what a caller passes when it already knows the asset build fails for this version and has accepted that with --allow-unbuilt-assets. Staging and the frontend build are unaffected")
	packageCmd.Flags().BoolVar(&packageBuildVerbose, "build-verbose", false, "Stream the asset and frontend build output instead of showing it only on failure")
	packageCmd.Flags().BoolVar(&packageBuildFrontend, "build-frontend", true, "Compile the app's JavaScript frontend (the Vite SPA that apps like frappe/crm build into <app>/public/frontend) when the checkout declares one. Use --build-frontend=false to package without it")
	packageCmd.Flags().DurationVar(&packageFrontendTimeout, "frontend-timeout", frontend.DefaultTimeout, "Time limit for the frontend dependency install and for the frontend build, each")
	packageCmd.Flags().StringVar(&packageFrontendSiteConfig, "frontend-site-config", "", "A bench's sites/common_site_config.json to build the frontend against. Apps like frappe/crm compile socketio_port into their bundle; without this a default bench config is synthesized (see --help output during the build)")
	packageCmd.Flags().BoolVar(&packageNoBenchScaffold, "no-bench-scaffold", false, "Never build a bench for this package: fail instead of building a bench-resolving frontend in a temporary bench, and do not fetch frappe's asset pipeline to compile esbuild entry points. Use when the package must be built only against a real bench (--bench-path or a checkout already at <bench>/apps/<app>), or on a host with no network")
	packageCmd.Flags().StringArrayVar(&packageRepos, "repo", nil, "Configured repository to resolve required_apps against, exclusively: neither this host's FPM store nor the bench answers a requirement when it is set. Repeatable, tried in order — name every backend the build publishes to")
	packageCmd.Flags().StringArrayVar(&packageOverrideDeps, "override-dependency", nil, "Replace a Python requirement the app declares, e.g. --override-dependency 'pycrdt>=0.14.4'. The staged copy's manifest is rewritten before wheels are vendored — the source tree is untouched — so the package and its wheels agree. Repeatable; recorded in app_metadata.json as dependency_overrides. For repackaging an app whose upstream pin cannot be satisfied for the target")
	packageCmd.Flags().BoolVar(&packageAllowUnbuiltAssets, "allow-unbuilt-assets", false, "Package a prod app that declares esbuild entry points even though nothing compiled them. The package installs but its desk UI does not render until the bench runs 'bench build'")
	packageCmd.Flags().StringArrayVar(&packageRequires, "requires", nil, "Pin a required app outright instead of resolving it, e.g. --requires frappe/erpnext==16.30.0 or --requires 'frappe/erpnext>=16.0.0,<17.0.0'; repeatable")
	packageCmd.Flags().BoolVar(&packageRequiresFromStore, "requires-from-local-store", false, "Allow a prod package to pin required_apps from this host's FPM store. The store is ambient state, so the resulting package is not reproducible; prod builds otherwise have to name a source (--requires/--repo/--bench-path)")
	packageCmd.Flags().BoolVar(&packageExactRequires, "requires-exact", false, "Record each resolved required app as the one exact version it resolved to, instead of that version's release line (>=16.0.0-0,<17.0.0). Exact pins break co-installation when two apps need the same dependency")
	packageCmd.Flags().BoolVar(&packageWithDeps, "with-deps", false, "Also write <output-path>/<app>-<version>-bundle/: this package plus every package it transitively requires (each once) with an install-order manifest, for 'fpm install <dir>' on an offline bench")
}

// frontendSiteConfig is the sites/common_site_config.json an app's frontend is compiled
// against: what --frontend-site-config names, else the bench --bench-path names.
//
// The frontend build no longer runs inside that bench — it runs in the checkout, before
// the desk bundles are compiled, because an app's entry points may be generated by it —
// so the bench's config is passed in explicitly rather than found by walking up from
// the build directory. A bench without one is not an error: internal/frontend
// synthesizes frappe's defaults, which is what it did here before.
func frontendSiteConfig(explicit, benchPath string) string {
	if explicit != "" || benchPath == "" {
		return explicit
	}
	candidate := filepath.Join(benchPath, "sites", "common_site_config.json")
	if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
		return candidate
	}
	return ""
}

// defaultBuildCacheHint names the build cache in --help without failing when the home
// directory cannot be resolved, which is not a reason to refuse to print usage.
func defaultBuildCacheHint() string {
	if dir, err := assetbench.DefaultCacheDir(); err == nil {
		return dir
	}
	return "the fpm build cache"
}

// frappeReleaseLine matches frappe's release-line branch names, which its apps mirror:
// erpnext and hrms keep a version-15 and a version-16 branch alongside frappe's.
var frappeReleaseLine = regexp.MustCompile(`^version-[0-9]+$`)

// frappeRefForApp is the frappe whose esbuild compiles this app's desk bundles.
//
// --frappe-ref wins outright. Otherwise, when the checkout is itself on a frappe release
// line, that is the frappe the app is written against, and compiling it with a different
// major is a mismatch nobody would catch: the bundles build, install, and then misbehave
// against a bench of the other major. Anything else — a tag, a feature branch, develop —
// has no reliable mapping to a frappe ref, so the default stands rather than being
// guessed at.
func frappeRefForApp(cmd *cobra.Command, gitRef string) string {
	if cmd.Flags().Changed("frappe-ref") && packageFrappeRef != "" {
		return packageFrappeRef
	}
	if frappeReleaseLine.MatchString(strings.TrimSpace(gitRef)) {
		return gitRef
	}
	if packageFrappeRef != "" {
		return packageFrappeRef
	}
	return assetbench.DefaultFrappeRef
}

// hasBundleSources reports whether the app has esbuild entry points on disk.
func hasBundleSources(root, appName string) bool {
	sources, err := benchbuild.BundleSources(filepath.Join(root, appName))
	return err == nil && len(sources) > 0
}

// enclosingBench is the bench a checkout already lives in, when that bench can compile
// assets. Empty when the checkout is somewhere else, or when the bench has no frappe.
func enclosingBench(root string) string {
	if !frontend.InsideBench(root) {
		return ""
	}
	bench := filepath.Clean(filepath.Join(root, "..", ".."))
	if _, err := os.Stat(filepath.Join(bench, "apps", "frappe", "esbuild", "esbuild.js")); err != nil {
		return ""
	}
	return bench
}

// warnAboutSiblings says so when a build reads another bench app off disk and that app
// is not there.
//
// It warns rather than fetches. The one app in the catalogue that needs this — helpdesk,
// whose desk/package.json declares `"@framework/ui": "link:../../frappe/ui"` — is
// disabled there precisely because fetching the sibling was tried and did not work:
// vite resolves frappe/ui's peerDependencies from the vite root, which only holds in a
// bench-wide hoisted install. Guessing at machinery that is known not to work would
// replace a clear failure with a confusing one, so the build proceeds and this explains
// the error it is about to produce.
func warnAboutSiblings(cmd *cobra.Command, root, appName string) {
	siblings, err := frontend.SiblingApps(root, appName)
	if err != nil || len(siblings) == 0 {
		return
	}
	apps := filepath.Dir(root)
	var missing []string
	for _, ref := range siblings {
		if _, statErr := os.Stat(filepath.Join(apps, filepath.FromSlash(ref))); statErr != nil {
			missing = append(missing, ref)
		}
	}
	if len(missing) == 0 {
		return
	}
	fmt.Fprintf(cmd.ErrOrStderr(),
		"Note: '%s' builds against %s, which it reads off disk as a sibling app, and %s not at %s. "+
			"The build will fail there. Package from a bench that has %s checked out beside '%s' "+
			"(--bench-path <bench>, or a checkout already at <bench>/apps/%s).\n",
		appName, strings.Join(missing, ", "), pluralAre(len(missing)), apps,
		strings.Join(missing, ", "), appName, appName)
}

func pluralAre(n int) string {
	if n == 1 {
		return "it is"
	}
	return "they are"
}

// ensureAssetBench materialises the bench to compile in, or returns "" after saying why
// it could not.
//
// Failure is deliberately not fatal. It only means the bundles are not compiled here,
// which is the state fpm was always in before this existed, and checkAssetsBuilt already
// decides what that is worth: a prod package is refused, a dev one warns. Failing
// outright would break packaging on a host that is offline and never needed this.
func ensureAssetBench(cmd *cobra.Command, appName, frappeRef string) string {
	fmt.Fprintf(cmd.OutOrStdout(), "Compiling '%s' desk assets with frappe %s\n", appName, frappeRef)
	bench, err := assetbench.Ensure(assetbench.Options{
		FrappeRef: frappeRef,
		Stdout:    cmd.OutOrStdout(),
	})
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %v\n", err)
		return ""
	}
	return bench
}
