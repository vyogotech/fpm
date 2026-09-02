package cmd

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"fpm/internal/appstore"
	"fpm/internal/assets"
	"fpm/internal/config"
	"fpm/internal/frontend"
	"fpm/internal/metadata"
	"fpm/internal/repository"
	"fpm/internal/resolver"
	"fpm/internal/rollback"
	"fpm/internal/semver"
	"fpm/internal/snapshot"
	"fpm/internal/wheels"

	"github.com/spf13/cobra"
)

var (
	installDeps                   bool
	installNoDeps                 bool
	installRepo                   string
	installRollback               bool
	installNoRollback             bool
	installDryRun                 bool
	installVerbose                bool
	installSkipRequiredAppsCheck  bool
	installIgnorePlatformMismatch bool
	installNoSiteRepair           bool
)

// copyDirContents recursively copies contents from src to dst.
// Assumes dst directory already exists or can be created by MkdirAll for subdirectories.
func copyDirContents(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return fmt.Errorf("failed to get relative path for %s from %s: %w", path, src, err)
		}
		dstPath := filepath.Join(dst, relPath)
		if info.IsDir() {
			if err := os.MkdirAll(dstPath, info.Mode()); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", dstPath, err)
			}
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(dstPath), os.ModePerm); err != nil {
			return fmt.Errorf("failed to create parent directory for %s: %w", dstPath, err)
		}
		srcFile, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("failed to open source file %s: %w", path, err)
		}
		defer srcFile.Close()
		dstFile, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
		if err != nil {
			return fmt.Errorf("failed to create destination file %s: %w", dstPath, err)
		}
		defer dstFile.Close()
		if _, err = io.Copy(dstFile, srcFile); err != nil {
			return fmt.Errorf("failed to copy %s to %s: %w", path, dstPath, err)
		}
		return nil
	})
}

var installCmd = &cobra.Command{
	Use:   "install <package_path | package_identifier>",
	Short: "Install a Frappe app from a local .fpm package or remote repository",
	Long: `Installs a Frappe application into a Frappe bench.
The package can be a path to a local .fpm file or a remote package identifier
in the format <group>/<artifact> or <group>/<artifact>==<version>.
If the version is not specified for a remote package, 'latest' is assumed and resolved first
from the local FPM store, then from remote repositories.

By default, transitive dependencies (hooks.py required_apps) are resolved across all configured
repositories, downloaded into the local store, and installed in dependency order. Use --no-deps
to install only the target package.

With --site, the app is also installed onto that site. fpm clears the site cache first, because
it has just changed sites/apps.txt from the outside and frappe reads its app-to-modules map from
a cache; a stale one makes frappe sync none of the app's DocTypes and then fail in after_install.
After the install fpm checks the app's DocTypes actually reached the database, and repairs a site
left in that half-installed state (--no-site-repair to report it instead, exit code ` + fmt.Sprint(ExitSiteHalfInstalled) + `).`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, configErr := config.InitConfig()
		if configErr != nil {
			return fmt.Errorf("failed to initialize FPM configuration: %w", configErr)
		}

		benchPath, err := cmd.Flags().GetString("bench-path")
		if err != nil {
			return fmt.Errorf("error retrieving 'bench-path' flag: %w", err)
		}
		siteName, err := cmd.Flags().GetString("site")
		if err != nil {
			return fmt.Errorf("error retrieving 'site' flag: %w", err)
		}

		// A directory holding fpm-bundle.json is a dependency closure exported by
		// `fpm bundle` or `fpm package --with-deps`: every package in it is installed,
		// in dependency order, so the required-apps check passes at each step.
		if isBundleDir(args[0]) {
			return installBundle(cmd, args[0], benchPath, siteName, cfg)
		}
		return installCascade(cmd, args[0], benchPath, siteName, cfg)
	},
}

// resolvedPackageItem represents an app queued for installation.
type resolvedPackageItem struct {
	Org                    string
	AppName                string
	Version                string
	AppModulePathInStore   string
	BaseVersionPathInStore string
	Metadata               *metadata.AppMetadata
	RequiredBy             string
	SourceDesc             string
	ProvidedByBench        bool
}

func (r *resolvedPackageItem) Identifier() string {
	if r.Org != "" {
		return fmt.Sprintf("%s/%s==%s", r.Org, r.AppName, r.Version)
	}
	if r.Version != "" {
		return fmt.Sprintf("%s==%s", r.AppName, r.Version)
	}
	return r.AppName
}

func (r *resolvedPackageItem) RequiredByOrTarget() string {
	if r.RequiredBy != "" {
		return "required by " + r.RequiredBy
	}
	return "target package " + r.Identifier()
}

// resolvePackageToLocalStore resolves a package argument (local .fpm file or remote
// identifier) into the local store and returns its resolved info.
func resolvePackageToLocalStore(packagePathArg string, cfg *config.FPMConfig, repoName string) (*resolvedPackageItem, error) {
	statInfo, statErr := os.Stat(packagePathArg)
	if statErr == nil && !statInfo.IsDir() {
		// Local file
		resolvedOrg, resolvedAppName, resolvedVersion, baseInstallPath, appModuleDir, err := appstore.ManageAppInLocalStore(packagePathArg, cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to manage package %s in local FPM store: %w", packagePathArg, err)
		}
		meta, err := metadata.LoadAppMetadata(baseInstallPath)
		if err != nil {
			meta = &metadata.AppMetadata{Org: resolvedOrg, AppName: resolvedAppName, PackageVersion: resolvedVersion}
		}
		return &resolvedPackageItem{
			Org:                    resolvedOrg,
			AppName:                resolvedAppName,
			Version:                resolvedVersion,
			AppModulePathInStore:   appModuleDir,
			BaseVersionPathInStore: baseInstallPath,
			Metadata:               meta,
			SourceDesc:             "local file: " + packagePathArg,
		}, nil
	}

	if os.IsNotExist(statErr) || (statInfo != nil && statInfo.IsDir()) {
		// Remote identifier
		var parsedOrg, parsedAppName, parsedVersion string
		parts := strings.Split(packagePathArg, "/")
		if len(parts) == 2 {
			parsedOrg = strings.TrimSpace(parts[0])
			appAndVersionParts := strings.Split(parts[1], "==")
			parsedAppName = strings.TrimSpace(appAndVersionParts[0])
			if len(appAndVersionParts) == 2 {
				parsedVersion = strings.TrimSpace(appAndVersionParts[1])
			}
		} else {
			return nil, fmt.Errorf("invalid remote package identifier format: '%s'. Expected <org>/<appName> or <org>/<appName>==<version>", packagePathArg)
		}
		if parsedOrg == "" || parsedAppName == "" {
			return nil, fmt.Errorf("invalid remote package identifier: Org ('%s') and AppName ('%s') must be specified in '%s'", parsedOrg, parsedAppName, packagePathArg)
		}

		resolvedVersion := parsedVersion
		if resolvedVersion == "" || resolvedVersion == "latest" {
			versionsDir := filepath.Join(cfg.AppsBasePath, parsedOrg, parsedAppName)
			entries, readDirErr := os.ReadDir(versionsDir)
			foundLocally := false
			if readDirErr == nil {
				var availableVersions []string
				for _, entry := range entries {
					if entry.IsDir() && resolver.InStore(cfg.AppsBasePath, parsedOrg, parsedAppName, entry.Name()) {
						availableVersions = append(availableVersions, entry.Name())
					}
				}
				if len(availableVersions) > 0 {
					sorted := semver.Sort(availableVersions)
					resolvedVersion = sorted[len(sorted)-1]
					foundLocally = true
				}
			}
			if !foundLocally {
				resolvedVersion = "latest"
			}
		}

		// Check if already in local store
		if resolvedVersion != "" && resolvedVersion != "latest" && resolver.InStore(cfg.AppsBasePath, parsedOrg, parsedAppName, resolvedVersion) {
			baseVersionPath := filepath.Join(cfg.AppsBasePath, parsedOrg, parsedAppName, resolvedVersion)
			appModulePath := filepath.Join(baseVersionPath, parsedAppName)
			meta, _ := metadata.LoadAppMetadata(baseVersionPath)
			if meta == nil {
				meta = &metadata.AppMetadata{Org: parsedOrg, AppName: parsedAppName, PackageVersion: resolvedVersion}
			}
			return &resolvedPackageItem{
				Org:                    parsedOrg,
				AppName:                parsedAppName,
				Version:                resolvedVersion,
				AppModulePathInStore:   appModulePath,
				BaseVersionPathInStore: baseVersionPath,
				Metadata:               meta,
				SourceDesc:             "local store",
			}, nil
		}

		// Fetch from remote repositories
		fmt.Printf("Fetching package %s/%s version '%s' from repository...\n", parsedOrg, parsedAppName, resolvedVersion)
		var downloadedPkgInfo *repository.DownloadedPackageInfo
		var findErr error
		if repoName != "" {
			repo, ok := config.GetRepository(cfg, repoName)
			if !ok {
				return nil, fmt.Errorf("repository %q is not configured", repoName)
			}
			client, cerr := resolver.NewHTTPClient(repo, 0)
			if cerr != nil {
				return nil, cerr
			}
			downloadedPkgInfo, findErr = repository.FindPackageInRepoConfig(repo, parsedOrg, parsedAppName, resolvedVersion, client)
		} else {
			downloadedPkgInfo, findErr = repository.FindPackageInRepos(cfg, parsedOrg, parsedAppName, resolvedVersion)
		}
		if findErr != nil {
			return nil, fmt.Errorf("failed to find or download package '%s': %w", packagePathArg, findErr)
		}

		resolvedOrg, resolvedAppName, resolvedVersion, baseInstallPath, appModuleDir, storeErr := appstore.ManageAppInLocalStore(downloadedPkgInfo.LocalPath, cfg)
		if storeErr != nil {
			return nil, fmt.Errorf("failed to manage downloaded package %s in local FPM store: %w", downloadedPkgInfo.LocalPath, storeErr)
		}
		meta, _ := metadata.LoadAppMetadata(baseInstallPath)
		if meta == nil {
			meta = &metadata.AppMetadata{Org: resolvedOrg, AppName: resolvedAppName, PackageVersion: resolvedVersion}
		}
		return &resolvedPackageItem{
			Org:                    resolvedOrg,
			AppName:                resolvedAppName,
			Version:                resolvedVersion,
			AppModulePathInStore:   appModuleDir,
			BaseVersionPathInStore: baseInstallPath,
			Metadata:               meta,
			SourceDesc:             "repository '" + downloadedPkgInfo.RepositoryName + "'",
		}, nil
	}

	return nil, fmt.Errorf("error checking package path '%s': %w", packagePathArg, statErr)
}

// installCascade executes the full installation pipeline:
// 1. Ingest target package into local store.
// 2. Resolve transitive dependencies across all configured repos (if --deps).
// 3. Check dry-run mode.
// 4. Capture pre-install snapshot.
// 5. Check version conflicts.
// 6. Phase 3: Bench-level install in topological order with rollback journal.
// 7. Phase 4: Site-level install pass.
func installCascade(cmd *cobra.Command, packagePathArg, benchPath, siteName string, cfg *config.FPMConfig) error {
	absBenchPath, err := filepath.Abs(benchPath)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for bench directory '%s': %w", benchPath, err)
	}

	effectiveDeps := installDeps
	if cmd.Flags().Changed("no-deps") && installNoDeps {
		effectiveDeps = false
	} else if cmd.Flags().Changed("deps") && !installDeps {
		effectiveDeps = false
	}

	effectiveRollback := installRollback
	if cmd.Flags().Changed("no-rollback") && installNoRollback {
		effectiveRollback = false
	} else if cmd.Flags().Changed("rollback") && !installRollback {
		effectiveRollback = false
	}

	// 1. Resolve target package into local store
	rootItem, err := resolvePackageToLocalStore(packagePathArg, cfg, installRepo)
	if err != nil {
		return err
	}

	var queue []*resolvedPackageItem
	if effectiveDeps {
		fmt.Printf("Resolving dependencies for %s...\n", rootItem.Identifier())
		closure, cerr := resolveTransitiveDeps(cfg, rootItem.Metadata.RequiredApps, rootItem.Identifier(), absBenchPath, installRepo, len(cfg.Repositories) > 0)
		if cerr != nil {
			return cerr
		}
		for _, e := range closure {
			if e.ProvidedByBench {
				fmt.Printf("Required app %s provided by the bench (%s); not reinstalled\n", e.App.Identifier(), e.StorePath)
				queue = append(queue, &resolvedPackageItem{
					Org:             e.App.Org,
					AppName:         e.App.Name,
					Version:         e.App.Version,
					RequiredBy:      e.RequiredBy,
					ProvidedByBench: true,
					SourceDesc:      "provided by bench",
				})
				continue
			}
			fmt.Printf("Required app %s satisfied from local FPM store (%s)\n", e.App.Identifier(), e.StorePath)
			basePath := filepath.Join(cfg.AppsBasePath, e.App.Org, e.App.Name, e.App.Version)
			modulePath := filepath.Join(basePath, e.App.Name)
			meta, _ := metadata.LoadAppMetadata(basePath)
			if meta == nil {
				meta = &metadata.AppMetadata{Org: e.App.Org, AppName: e.App.Name, PackageVersion: e.App.Version}
			}
			queue = append(queue, &resolvedPackageItem{
				Org:                    e.App.Org,
				AppName:                e.App.Name,
				Version:                e.App.Version,
				AppModulePathInStore:   modulePath,
				BaseVersionPathInStore: basePath,
				Metadata:               meta,
				RequiredBy:             e.RequiredBy,
				SourceDesc:             "local store",
			})
		}
	} else if !installSkipRequiredAppsCheck {
		packageID := rootItem.Identifier()
		if err := checkRequiredApps(cfg, rootItem.Metadata, absBenchPath, packageID, siteName != "", false); err != nil {
			return err
		}
	}
	queue = append(queue, rootItem)

	// 2. Dry-Run Mode
	if installDryRun {
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "\nDry-run mode: %d package(s) would be installed in order:\n", len(queue))
		for i, item := range queue {
			by := ""
			if item.RequiredBy != "" {
				by = " (required by " + item.RequiredBy + ")"
			}
			if item.ProvidedByBench {
				fmt.Fprintf(out, "  %d. %s  [provided by bench, not reinstalled]%s\n", i+1, item.Identifier(), by)
			} else {
				fmt.Fprintf(out, "  %d. %s  (source: %s)%s\n", i+1, item.Identifier(), item.SourceDesc, by)
			}
		}
		if siteName != "" {
			fmt.Fprintf(out, "\nTarget site: %s (apps will be installed onto site in order after bench install)\n", siteName)
		}
		fmt.Fprintln(out)
		return nil
	}

	// 3. Pre-Install Snapshot
	snap, err := snapshot.Take(absBenchPath, siteName)
	if err != nil {
		return fmt.Errorf("failed to capture pre-install snapshot of bench '%s': %w", absBenchPath, err)
	}

	// 4. Version Conflict Check (for dependencies)
	for _, item := range queue {
		if item.RequiredBy != "" && snap.WasPresentInBench(item.AppName) {
			preVer := snap.PreExistingVersion(item.AppName)
			if item.Version != "" && preVer != "" && preVer != item.Version {
				return fmt.Errorf("%w: bench %s has %s version %q, but %s requires version %q",
					ErrVersionConflict, absBenchPath, item.AppName, preVer, item.RequiredByOrTarget(), item.Version)
			}
		}
	}

	// 5. Phase 3: Bench-Level Installation with Rollback Journal
	journal := rollback.NewJournal(snap)
	fmt.Printf("\n=== Bench Installation Phase (%d package(s)) ===\n", len(queue))
	for i, item := range queue {
		if item.ProvidedByBench {
			fmt.Printf("[%d/%d] %s — provided by the bench, skipping bench install\n", i+1, len(queue), item.Identifier())
			continue
		}
		if item.RequiredBy != "" && snap.WasPresentInBench(item.AppName) {
			fmt.Printf("[%d/%d] %s — already present in bench (%s), skipping bench install\n", i+1, len(queue), item.Identifier(), item.BaseVersionPathInStore)
			continue
		}

		fmt.Printf("\n[%d/%d] Installing %s into bench...\n", i+1, len(queue), item.Identifier())
		if err := installOneItemIntoBench(item, absBenchPath, journal, installVerbose); err != nil {
			if effectiveRollback {
				_ = journal.Rollback(cmd.OutOrStdout(), installVerbose)
				return fmt.Errorf("%w: %w", ErrRolledBack, err)
			}
			return err
		}
		fmt.Printf("[%d/%d] Successfully installed %s into bench.\n", i+1, len(queue), item.Identifier())
	}

	// 6. Phase 4: Site-Level Installation Pass
	if siteName != "" {
		fmt.Printf("\n=== Site Installation Phase: Installing onto site '%s' ===\n", siteName)
		for i, item := range queue {
			if snap.WasInstalledOnSite(item.AppName) {
				fmt.Printf("  [%d/%d] %s — already installed on site '%s', skipping\n", i+1, len(queue), item.AppName, siteName)
				continue
			}
			fmt.Printf("  [%d/%d] Installing app '%s' onto site '%s'...\n", i+1, len(queue), item.AppName, siteName)
			if err := installAppOnSite(absBenchPath, siteName, item.AppName); err != nil {
				return fmt.Errorf("site installation failed at %s (%d of %d): %w", item.AppName, i+1, len(queue), err)
			}
		}
		fmt.Printf("\nAll %d package(s) installed on site '%s'.\n", len(queue), siteName)
	} else {
		fmt.Printf("\nAll %d package(s) installed into bench %s.\nPass --site <site> to also install onto a site.\n", len(queue), absBenchPath)
	}

	return nil
}

// installOneItemIntoBench performs the bench-level mutations for a single package:
// symlink, asset deployment, pip install, apps.txt.
func installOneItemIntoBench(item *resolvedPackageItem, absBenchPath string, journal *rollback.Journal, verbose bool) error {
	baseVersionPath := item.BaseVersionPathInStore
	appModulePathInFPMStore := item.AppModulePathInStore
	appName := item.AppName

	// Pre-install wheel check
	wheelsInStore := vendoredWheelsDir(baseVersionPath)
	if wheelsInStore != "" {
		if err := checkWheelTarget(item.Metadata, benchPythonVersion(absBenchPath), installIgnorePlatformMismatch); err != nil {
			return err
		}
	}

	// 1. Symlink
	linkName := filepath.Join(absBenchPath, "apps", appName)
	linkDir := filepath.Dir(linkName)
	if err := os.MkdirAll(linkDir, os.ModePerm); err != nil {
		return fmt.Errorf("failed to create directory for symlink '%s': %w", linkDir, err)
	}
	if _, err := os.Lstat(linkName); err == nil {
		if err := os.RemoveAll(linkName); err != nil {
			return fmt.Errorf("failed to remove existing file/symlink at '%s': %w", linkName, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to check symlink path '%s': %w", linkName, err)
	}
	if err := os.Symlink(baseVersionPath, linkName); err != nil {
		return fmt.Errorf("failed to create symlink from '%s' to '%s': %w", baseVersionPath, linkName, err)
	}
	if journal != nil {
		journal.Record(&rollback.SymlinkAction{BenchPath: absBenchPath, App: appName, Snapshot: journal.Snapshot})
	}

	// 2. Deploy Packaged Assets
	if err := deployPackagedAssets(absBenchPath, appName, appModulePathInFPMStore, baseVersionPath); err != nil {
		return err
	}
	if journal != nil {
		journal.Record(&rollback.AssetDeployAction{BenchPath: absBenchPath, App: appName, Snapshot: journal.Snapshot})
	}

	// 3. Pip Install (using execCmd.Dir = absBenchPath, no process-global os.Chdir)
	pipAppPath := filepath.Join(absBenchPath, "apps", appName)
	if wheelsInStore != "" {
		fmt.Printf("Detected vendored wheels in package. Installing offline from %s\n", wheelsInStore)
	} else {
		fmt.Printf("No vendored wheels in package; resolving Python dependencies from the network.\n")
	}
	pipCmdArgs := buildPipInstallArgs(pipAppPath, wheelsInStore)
	fmt.Printf("Running pip install for '%s': ./env/bin/pip %s\n", appName, strings.Join(pipCmdArgs, " "))
	pipExecCmd := exec.Command(filepath.Join(absBenchPath, "env", "bin", "pip"), pipCmdArgs...)
	pipExecCmd.Dir = absBenchPath
	output, err := pipExecCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pip install for app '%s' failed:\n%s\nError: %w", appName, string(output), err)
	}
	if verbose {
		fmt.Printf("Pip install output:\n%s\n", string(output))
	}
	if journal != nil {
		journal.Record(&rollback.PipInstallAction{BenchPath: absBenchPath, App: appName, Snapshot: journal.Snapshot})
	}

	// 4. Update sites/apps.txt
	appsTxtPath := filepath.Join(absBenchPath, "sites", "apps.txt")
	if err := appendToAppsTxt(appsTxtPath, appName); err != nil {
		return err
	}
	if journal != nil {
		journal.Record(&rollback.AppsTxtAction{BenchPath: absBenchPath, App: appName, Snapshot: journal.Snapshot})
	}

	return nil
}

// appendToAppsTxt safely appends appName to apps.txt if not already listed.
func appendToAppsTxt(appsTxtPath, appName string) error {
	sitesDir := filepath.Dir(appsTxtPath)
	if err := os.MkdirAll(sitesDir, os.ModePerm); err != nil {
		return fmt.Errorf("failed to create sites directory '%s': %w", sitesDir, err)
	}
	fileContentBytes, err := os.ReadFile(appsTxtPath)
	var appsInFile []string
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("failed to read %s: %w", appsTxtPath, err)
		}
	} else {
		for _, a := range strings.Split(strings.TrimSpace(string(fileContentBytes)), "\n") {
			if trimmed := strings.TrimSpace(a); trimmed != "" {
				appsInFile = append(appsInFile, trimmed)
			}
		}
	}
	for _, existing := range appsInFile {
		if existing == appName {
			return nil
		}
	}
	appsInFile = append(appsInFile, appName)
	newContent := strings.Join(appsInFile, "\n") + "\n"
	return os.WriteFile(appsTxtPath, []byte(newContent), 0o644)
}

// installOne installs a single package without transitive dependency resolution.
// Used directly by installBundle.
func installOne(cmd *cobra.Command, packagePathArg, benchPath, siteName string, cfg *config.FPMConfig) error {
	absBenchPath, err := filepath.Abs(benchPath)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for bench directory '%s': %w", benchPath, err)
	}
	item, err := resolvePackageToLocalStore(packagePathArg, cfg, installRepo)
	if err != nil {
		return err
	}
	if err := checkRequiredApps(cfg, item.Metadata, absBenchPath, item.Identifier(), siteName != "", installSkipRequiredAppsCheck); err != nil {
		return err
	}
	if err := installOneItemIntoBench(item, absBenchPath, nil, installVerbose); err != nil {
		return err
	}
	if siteName != "" {
		if err := installAppOnSite(absBenchPath, siteName, item.AppName); err != nil {
			return err
		}
	}
	return nil
}

// benchPythonExecutable is the interpreter of a Frappe bench's virtualenv.
const benchPythonExecutable = "./env/bin/python"

// frappeCommand builds a frappe CLI invocation that runs out of the bench's own
// virtualenv, from <bench>/sites — the working directory bench itself uses — and
// without a process-global os.Chdir.
func frappeCommand(benchPath, siteName string, args ...string) *exec.Cmd {
	full := append([]string{"-m", "frappe.utils.bench_helper", "frappe", "--site", siteName}, args...)
	execCmd := exec.Command(filepath.Join(benchPath, "env", "bin", "python"), full...)
	execCmd.Dir = filepath.Join(benchPath, "sites")
	return execCmd
}

// clearSiteCache runs `bench --site <site> clear-cache`, reporting failure without
// treating it as fatal.
func clearSiteCache(benchPath, siteName, why string) {
	fmt.Printf("Clearing site cache (%s): (cd sites && frappe --site %s clear-cache)\n", why, siteName)
	if out, err := frappeCommand(benchPath, siteName, "clear-cache").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: clear-cache for site '%s' failed: %v\n%s\nRun 'bench --site %s clear-cache' yourself.\n",
			siteName, err, strings.TrimSpace(string(out)), siteName)
		return
	}
	fmt.Printf("Cleared cache for site '%s'.\n", siteName)
}

// installAppOnSite runs `frappe --site <site> install-app <app>` and makes sure the
// app's DocTypes actually reached the database.
//
// Frappe caches the app-to-modules map in redis and only rebuilds it from
// sites/apps.txt on a cache miss. fpm has just changed apps.txt from the outside,
// so a cache warmed before that install makes frappe's own `sync_for` iterate an
// empty module list — it syncs no DocTypes at all, silently — and the app's
// after_install hook then fails on its own missing DocTypes, leaving the site
// half-installed: app registered, tables absent (issue #13). Frappe grew a guard
// for this in develop (92136994b), but every released version still needs the
// cache cleared first, which is why that happens here before install-app rather
// than only after it.
func installAppOnSite(benchPath, siteName, appName string) error {
	pythonExe := filepath.Join(benchPath, "env", "bin", "python")
	if _, statErr := os.Stat(pythonExe); statErr != nil {
		return fmt.Errorf("cannot install app '%s' onto site '%s': %s not found in bench '%s'. "+
			"The app is installed in the bench; run 'bench --site %s install-app %s' yourself",
			appName, siteName, pythonExe, benchPath, siteName, appName)
	}

	clearSiteCache(benchPath, siteName, "apps.txt changed, so frappe's cached module map is stale")

	fmt.Printf("\nInstalling app '%s' onto site '%s': (cd sites && frappe --site %s install-app %s)\n",
		appName, siteName, siteName, appName)
	output, installErr := frappeCommand(benchPath, siteName, "install-app", appName).CombinedOutput()
	if installErr == nil {
		fmt.Printf("Successfully installed app '%s' onto site '%s'.\nOutput:\n%s\n", appName, siteName, string(output))
	}

	// Verify what the install claims: the app's own DocTypes have to exist now.
	if err := ensureAppDocTypesSynced(benchPath, siteName, appName, installErr, string(output)); err != nil {
		return err
	}

	clearSiteCache(benchPath, siteName, "so the running site picks up the new app")
	fmt.Printf("Note: running web/worker processes load an app when they start; restart them (bench restart) for '%s' to be served.\n", appName)
	return nil
}

// ensureAppDocTypesSynced checks that the DocTypes the app ships on disk are in the
// site's database, and repairs a half-installed site when they are not.
//
// installErr is the error `install-app` exited with, if any: a failure whose cause
// is the missing sync is repairable, while any other failure is returned as-is
// rather than papered over by a retry.
func ensureAppDocTypesSynced(benchPath, siteName, appName string, installErr error, installOutput string) error {
	failed := func(err error) error {
		return fmt.Errorf("failed to install app '%s' onto site '%s':\n%s\nError: %w",
			appName, siteName, installOutput, err)
	}

	expected := appDocTypeNames(benchPath, appName, doctypeVerificationLimit)
	if len(expected) == 0 {
		// The app ships no DocTypes, so there is nothing to verify either way.
		if installErr != nil {
			return failed(installErr)
		}
		return nil
	}

	present, countErr := countSiteDocTypes(benchPath, siteName, expected)
	if countErr != nil {
		// The site could not be queried at all — it does not exist, the database is
		// down, the frappe version has no such entry point. Nothing here can be
		// concluded about the sync, so the install's own outcome stands.
		if installErr == nil {
			fmt.Fprintf(os.Stderr, "Note: could not verify that %s's DocTypes reached site '%s': %v\n", appName, siteName, countErr)
			return nil
		}
		return failed(installErr)
	}
	if present > 0 {
		if installErr != nil {
			// The sync happened; install-app failed for its own reasons, which a
			// retry would not change.
			return failed(installErr)
		}
		return nil
	}

	// The exact half-installed state from issue #13: the app is registered on the
	// site, and not one of its DocTypes is in the database.
	fmt.Fprintf(os.Stderr, "\nWarning: app '%s' is registered on site '%s' but none of its %d DocType(s) reached the database — "+
		"frappe synced nothing for it (a stale module map).\n", appName, siteName, len(expected))
	if installNoSiteRepair {
		return fmt.Errorf("%w: site '%s' is half-installed for app '%s': the app is registered but its DocTypes are missing. "+
			"Run 'bench --site %s migrate' to finish it (--no-site-repair stopped fpm from doing that)",
			ErrSiteHalfInstalled, siteName, appName, siteName)
	}

	fmt.Fprintf(os.Stderr, "Repairing: clearing the site cache and re-running install-app --force, "+
		"which re-syncs the DocTypes and re-runs %s's install hooks in a fresh process.\n", appName)
	clearSiteCache(benchPath, siteName, "to rebuild frappe's module map from apps.txt")
	repairOut, repairErr := frappeCommand(benchPath, siteName, "install-app", "--force", appName).CombinedOutput()
	if repairErr == nil {
		if present, countErr = countSiteDocTypes(benchPath, siteName, expected); countErr == nil && present > 0 {
			fmt.Printf("Repaired: %d of %d checked DocType(s) of '%s' are now on site '%s'.\nOutput:\n%s\n",
				present, len(expected), appName, siteName, string(repairOut))
			return nil
		}
	}
	return fmt.Errorf("%w: site '%s' is half-installed for app '%s': the app is registered but its DocTypes are missing, "+
		"and re-running install-app did not fix it. Run 'bench --site %s migrate' to finish the install.\nFirst attempt:\n%s\nRepair attempt:\n%s",
		ErrSiteHalfInstalled, siteName, appName, siteName, installOutput, string(repairOut))
}

// doctypeVerificationLimit bounds how many DocType names are sent to the site in
// one query. The failure being checked for is all-or-nothing — frappe either
// synced the app or synced nothing — so a sample is as conclusive as the full set,
// and a bounded argument list keeps the command safe for an app shipping hundreds.
const doctypeVerificationLimit = 100

// appDocTypeNames lists the DocTypes an app ships, read from the doctype JSON
// files in the bench's copy of it (<bench>/apps/<app>, which fpm has just linked
// into the store). Names are sorted, so the sample is the same on every run.
func appDocTypeNames(benchPath, appName string, limit int) []string {
	matches, err := filepath.Glob(filepath.Join(benchPath, "apps", appName, appName, "*", "doctype", "*", "*.json"))
	if err != nil {
		return nil
	}
	var names []string
	for _, path := range matches {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		var doc struct {
			Name    string `json:"name"`
			DocType string `json:"doctype"`
		}
		if json.Unmarshal(data, &doc) != nil || doc.DocType != "DocType" || doc.Name == "" {
			continue
		}
		names = append(names, doc.Name)
	}
	sort.Strings(names)
	if limit > 0 && len(names) > limit {
		names = names[:limit]
	}
	return names
}

// countSiteDocTypes reports how many of the named DocTypes exist on the site.
// `frappe --site <site> execute` prints the returned value as JSON, and prints
// nothing at all when it is falsy — so no output, with a zero exit status, is a
// count of zero.
func countSiteDocTypes(benchPath, siteName string, names []string) (int, error) {
	filters, err := json.Marshal(map[string]any{"dt": "DocType", "filters": map[string]any{"name": []any{"in", names}}})
	if err != nil {
		return 0, err
	}
	out, err := frappeCommand(benchPath, siteName, "execute", "frappe.db.count", "--kwargs", string(filters)).CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("%v: %s", err, strings.TrimSpace(lastLines(string(out), 5)))
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if n, convErr := strconv.Atoi(strings.TrimSpace(lines[i])); convErr == nil {
			return n, nil
		}
	}
	// Frappe printed nothing it could parse as a number: the count was zero.
	return 0, nil
}

// lastLines returns at most n trailing lines of s, for error excerpts.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// Helper function to read metadata from an FPM file's app_metadata.json
func readMetadataFromFPMFile(fpmPath string) (*metadata.AppMetadata, error) {
	r, err := zip.OpenReader(fpmPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open FPM package %s: %w", fpmPath, err)
	}
	defer r.Close()

	var metaFile *zip.File
	for _, f := range r.File {
		if f.Name == "app_metadata.json" {
			metaFile = f
			break
		}
	}

	if metaFile == nil {
		return nil, fmt.Errorf("app_metadata.json not found in FPM package %s", fpmPath)
	}

	rc, err := metaFile.Open()
	if err != nil {
		return nil, fmt.Errorf("failed to open app_metadata.json in FPM package: %w", err)
	}
	defer rc.Close()

	metaBytes, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("failed to read app_metadata.json from FPM package: %w", err)
	}

	var appMeta metadata.AppMetadata
	if err := json.Unmarshal(metaBytes, &appMeta); err != nil {
		return nil, fmt.Errorf("failed to parse app_metadata.json from FPM package (%s): %w", fpmPath, err)
	}
	return &appMeta, nil
}

func vendoredWheelsDir(baseVersionPath string) string {
	dir := filepath.Join(baseVersionPath, wheels.DirName)
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		return dir
	}
	return ""
}

func buildPipInstallArgs(pipAppPath, wheelsDir string) []string {
	if wheelsDir == "" {
		return []string{"install", "-q", "-e", pipAppPath}
	}
	return []string{"install", "-q", "--no-index", "--find-links", wheelsDir, "-e", pipAppPath}
}

func checkWheelTarget(meta *metadata.AppMetadata, benchPython string, ignore bool) error {
	if meta == nil || meta.WheelPlatform == "" || meta.WheelPlatform == wheels.HostPlatformTag {
		return nil
	}
	var problems []string
	if !wheelPlatformMatchesHost(meta.WheelPlatform, runtime.GOOS, runtime.GOARCH) {
		problems = append(problems, fmt.Sprintf("wheels were vendored for '%s' but this host is %s/%s",
			meta.WheelPlatform, runtime.GOOS, runtime.GOARCH))
	}
	if meta.WheelPythonVersion != "" && benchPython != "" && meta.WheelPythonVersion != benchPython {
		problems = append(problems, fmt.Sprintf("wheels were vendored for Python %s but the bench's env/bin/python is %s",
			meta.WheelPythonVersion, benchPython))
	}
	if len(problems) == 0 {
		return nil
	}
	if ignore {
		fmt.Fprintf(os.Stderr, "Warning: %s. Continuing because --ignore-platform-mismatch was given; pip may reject the wheels.\n",
			strings.Join(problems, "; "))
		return nil
	}
	fix := "fpm package --platform <tag> --python-version <version>"
	if benchPython != "" {
		fix = fmt.Sprintf("fpm package --platform <tag> --python-version %s", benchPython)
	}
	return fmt.Errorf("%w: %s.\nThere is no network fallback once pip installs offline. "+
		"Repackage the app for this bench (%s), or pass --ignore-platform-mismatch to let pip decide",
		ErrPlatformMismatch, strings.Join(problems, "; "), fix)
}

func wheelPlatformMatchesHost(tag, goos, goarch string) bool {
	arches := []string{goArchToWheelArch(goarch), goarch}
	for _, platform := range wheels.ParseTag(tag) {
		p := strings.ToLower(platform)
		archMatches := false
		for _, a := range arches {
			if strings.Contains(p, a) {
				archMatches = true
			}
		}
		osMatches := strings.Contains(p, goos)
		if goos == "linux" {
			osMatches = strings.Contains(p, "linux")
		} else if goos == "darwin" {
			osMatches = strings.Contains(p, "macosx") || strings.Contains(p, "darwin")
		}
		if archMatches && osMatches {
			return true
		}
	}
	return false
}

func benchPythonVersion(benchPath string) string {
	python := filepath.Join(benchPath, "env", "bin", "python")
	out, err := exec.Command(python, "-c", "import sys; print('%d.%d' % sys.version_info[:2])").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func checkRequiredApps(cfg *config.FPMConfig, meta *metadata.AppMetadata, benchPath, packageID string, forSite, skip bool) error {
	if meta == nil || len(meta.RequiredApps) == 0 {
		return nil
	}
	closure, missing, err := resolver.CheckClosure(cfg.AppsBasePath, benchPath, meta.RequiredApps, packageID)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		if !skip {
			return resolver.MissingError(packageID, missing)
		}
		fmt.Fprintf(os.Stderr, "Warning: --skip-required-apps-check given; continuing without these required apps in the local store:\n")
		for _, m := range missing {
			fmt.Fprintf(os.Stderr, "  %s\n", m)
		}
	}

	benchApps := benchAppNames(benchPath)
	var notInBench []string
	for _, entry := range closure {
		if entry.ProvidedByBench {
			fmt.Printf("Required app %s provided by the bench (%s); not reinstalled\n", entry.App.Identifier(), entry.StorePath)
			continue
		}
		fmt.Printf("Required app %s satisfied from local FPM store (%s)\n", entry.App.Identifier(), entry.StorePath)
		if !benchApps[entry.App.Name] {
			notInBench = append(notInBench, entry.App.Identifier())
		}
	}
	if len(notInBench) > 0 {
		msg := fmt.Sprintf("required app(s) not yet installed in bench %s: %s. Install them first, in this order, with "+
			"'fpm install <org>/<app>==<version> --bench-path %s'", benchPath, strings.Join(notInBench, ", "), benchPath)
		if forSite && !skip {
			return fmt.Errorf("%w: %s (bench install-app needs them present in the bench)", resolver.ErrMissing, msg)
		}
		fmt.Fprintf(os.Stderr, "Warning: %s\n", msg)
	}
	return nil
}

func benchAppNames(benchPath string) map[string]bool {
	names := map[string]bool{}
	if data, err := os.ReadFile(filepath.Join(benchPath, "sites", "apps.txt")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				names[line] = true
			}
		}
	}
	if entries, err := os.ReadDir(filepath.Join(benchPath, "apps")); err == nil {
		for _, e := range entries {
			if _, err := os.Stat(filepath.Join(benchPath, "apps", e.Name(), e.Name(), "hooks.py")); err == nil {
				names[e.Name()] = true
			}
		}
	}
	return names
}

func deployPackagedAssets(benchPath, appName, appModulePath, baseVersionPath string) error {
	legacy := filepath.Join(baseVersionPath, "compiled_assets")
	if info, err := os.Stat(legacy); err == nil && info.IsDir() {
		publicDir := filepath.Join(appModulePath, "public")
		fmt.Printf("Detected legacy 'compiled_assets' in package; merging into %s\n", publicDir)
		if err := os.MkdirAll(publicDir, 0o755); err != nil {
			return fmt.Errorf("failed to create %s: %w", publicDir, err)
		}
		if err := copyDirContents(legacy, publicDir); err != nil {
			return fmt.Errorf("failed to merge compiled_assets into '%s': %w", publicDir, err)
		}
	}

	deployed, err := assets.Deploy(benchPath, appName, appModulePath)
	if err != nil {
		return fmt.Errorf("failed to deploy assets for app '%s': %w", appName, err)
	}
	if !deployed.Linked {
		fmt.Printf("App '%s' has no public/ directory; nothing to serve under /assets/%s.\n", appName, appName)
		return nil
	}
	fmt.Printf("Linked %s -> %s\n", filepath.Join(assets.AssetsDir(benchPath), appName), filepath.Join(appModulePath, "public"))

	// A packaged SPA (frappe/crm and friends) is served straight out of the symlink
	// above at /assets/<app>/frontend/ and needs no manifest entry, because frappe's
	// esbuild manifest only ever tracks *.bundle.* files. Report it separately, and
	// write the www route template if the package carries the SPA without one — that
	// is what crm's own `copy-html-entry` build step does, and a package built from a
	// checkout that skipped it would otherwise have no route to render the SPA at.
	spa, spaErr := frontend.Outputs(baseVersionPath, appName)
	if spaErr != nil {
		fmt.Fprintf(os.Stderr, "Note: could not inspect the packaged frontend for '%s': %v\n", appName, spaErr)
	}
	hasFrontend := spa.Any()
	if hasFrontend {
		written, err := frontend.EnsureWWWEntry(baseVersionPath, appName)
		if err != nil {
			return fmt.Errorf("failed to install the SPA route template for '%s': %w", appName, err)
		}
		for _, w := range written {
			fmt.Printf("Wrote the SPA route template %s (the package shipped none)\n", w)
		}
		spa.Routes = append(spa.Routes, written...)
		sort.Strings(spa.Routes)

		fmt.Printf("Frontend: %d file(s) in %s\n", spa.Files, strings.Join(spa.Dirs, ", "))
		for _, dir := range spa.Dirs {
			// public/<name> is reachable at /assets/<app>/<name>/ through the symlink
			// assets.Deploy just created.
			fmt.Printf("  %s -> /assets/%s/%s/\n", dir, appName, strings.TrimPrefix(dir, appName+"/public/"))
		}
		for _, route := range spa.Routes {
			fmt.Printf("  route: %s\n", route)
		}
		// A library-mode build has no index.html and needs no route; an SPA with one
		// and no template is not reachable at any URL, which is worth saying.
		if len(spa.Entries) > 0 && len(spa.Routes) == 0 {
			fmt.Fprintf(os.Stderr, "Note: '%s' ships a compiled frontend but no www route template, "+
				"and its hooks.py declares no website_route_rules to derive one from; "+
				"frappe will serve the assets but no page renders them.\n", appName)
		}
	}

	total := len(deployed.LTR) + len(deployed.RTL)
	if total == 0 {
		if hasFrontend {
			// An SPA-only app legitimately has no bundles; saying otherwise, and
			// telling the user to repackage with --bench-path, would send them after
			// a build that cannot produce anything for this app.
			fmt.Printf("No classic bundles (public/dist) in package, which is expected for an SPA-only app; %s left unchanged.\n",
				assets.ManifestFileName)
			return nil
		}
		fmt.Printf("No built bundles (public/dist) in package; %s left unchanged. "+
			"Package with 'fpm package --bench-path <bench>' to ship compiled assets.\n", assets.ManifestFileName)
		return nil
	}
	fmt.Printf("Recorded %d bundle(s) in sites/assets/%s and %s:\n", total, assets.ManifestFileName, assets.RTLManifestFileName)
	keys := make([]string, 0, total)
	for k := range deployed.LTR {
		keys = append(keys, k)
	}
	for k := range deployed.RTL {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v, ok := deployed.LTR[k]
		if !ok {
			v = deployed.RTL[k]
		}
		fmt.Printf("  %s -> %s\n", k, v)
	}

	if err := assets.InvalidateCache(benchPath); err != nil {
		fmt.Fprintf(os.Stderr, "Note: could not clear the assets_json cache in redis (%v); "+
			"a running site picks up the new bundles after 'bench --site <site> clear-cache' or a restart.\n", err)
	} else {
		fmt.Printf("Cleared assets_json cache in redis_cache.\n")
	}
	return nil
}

func goArchToWheelArch(goArch string) string {
	switch goArch {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	default:
		return goArch
	}
}

func readMetadataFromFPMStore(installedAppVersionPath string) (*metadata.AppMetadata, error) {
	metaFilePath := filepath.Join(installedAppVersionPath, "app_metadata.json")
	if _, err := os.Stat(metaFilePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("app_metadata.json not found in installed app path %s", installedAppVersionPath)
	}
	return metadata.LoadAppMetadata(installedAppVersionPath)
}

func init() {
	installCmd.Flags().String("bench-path", "", "Path to the Frappe bench directory")
	if err := installCmd.MarkFlagRequired("bench-path"); err != nil {
		fmt.Fprintf(os.Stderr, "Error marking 'bench-path' flag required for install cmd: %v\n", err)
	}
	installCmd.Flags().String("site", "", "Site to install the app onto after adding it to the bench (runs 'bench --site <site> install-app')")
	installCmd.Flags().BoolVar(&installDeps, "deps", true, "Resolve and install transitive dependencies (use --no-deps to install only the target package)")
	installCmd.Flags().BoolVar(&installNoDeps, "no-deps", false, "Install only the target package without resolving or installing dependencies")
	installCmd.Flags().StringVar(&installRepo, "repo", "", "Only fetch dependencies from this configured repository")
	installCmd.Flags().BoolVar(&installRollback, "rollback", true, "Automatically rollback bench changes if installation fails mid-flight")
	installCmd.Flags().BoolVar(&installNoRollback, "no-rollback", false, "Leave partial installation in place on failure")
	installCmd.Flags().BoolVar(&installDryRun, "dry-run", false, "Print the installation plan without modifying the bench")
	installCmd.Flags().BoolVarP(&installVerbose, "verbose", "v", false, "Show detailed output during installation and rollback")
	installCmd.Flags().BoolVar(&installSkipRequiredAppsCheck, "skip-required-apps-check", false, "Do not fail when a required app (hooks.py required_apps) is missing from the local FPM store or bench; for benches whose required apps were installed outside fpm")
	installCmd.Flags().BoolVar(&installNoSiteRepair, "no-site-repair", false, "Do not re-run 'install-app --force' when a site install leaves the app registered but its DocTypes unsynced; fail and leave the site as it is")
	installCmd.Flags().BoolVar(&installIgnorePlatformMismatch, "ignore-platform-mismatch", false, "Do not fail when the package's vendored wheels were built for another platform or Python version; pip then decides")
	rootCmd.AddCommand(installCmd)
}
