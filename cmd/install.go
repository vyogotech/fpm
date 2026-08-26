package cmd

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"fpm/internal/appstore" // Added for app store management
	"fpm/internal/assets"
	"fpm/internal/config"
	"fpm/internal/metadata"
	"fpm/internal/repository"
	"fpm/internal/resolver"
	"fpm/internal/wheels" // For locating vendored Python dependencies
	"os/exec"

	"github.com/spf13/cobra"
)

var (
	installSkipRequiredAppsCheck  bool
	installIgnorePlatformMismatch bool
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
			// For copyDirContents, we preserve the source directory's mode for subdirectories it creates.
			// This is different from extractFPMArchive where we want to standardize.
			if err := os.MkdirAll(dstPath, info.Mode()); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", dstPath, err)
			}
			return nil
		}
		// For files, ensure parent dir exists, then copy with original mode.
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
from the local FPM store, then from remote repositories.`,
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
		return installOne(cmd, args[0], benchPath, siteName, cfg)
	},
}

// installOne installs a single package — a local .fpm file or an
// <org>/<app>[==<version>] identifier — into the bench, and onto siteName when given.
func installOne(cmd *cobra.Command, packagePathArg, benchPath, siteName string, cfg *config.FPMConfig) error {
	{
		var appModulePathInFPMStore string
		var appOrg, appName, appVersion string

		fmt.Printf("Attempting to install '%s'\n", packagePathArg)
		statInfo, statErr := os.Stat(packagePathArg)

		if statErr == nil && !statInfo.IsDir() {
			fmt.Printf("Local package file found: %s\n", packagePathArg)
			localFpmMeta, err := readMetadataFromFPMFile(packagePathArg)
			if err != nil {
				return fmt.Errorf("failed to read metadata from local FPM file %s: %w", packagePathArg, err)
			}
			// Use .Org and .AppName from metadata which are now the correct fields
			if localFpmMeta.Org == "" || localFpmMeta.AppName == "" || localFpmMeta.PackageVersion == "" {
				return fmt.Errorf("Org, AppName, or PackageVersion missing from metadata in %s", packagePathArg)
			}

			appOrg = localFpmMeta.Org
			appName = localFpmMeta.AppName
			appVersion = localFpmMeta.PackageVersion
			fmt.Printf("Installing from local file: %s/%s version %s\n", appOrg, appName, appVersion)

			// Use appstore.ManageAppInLocalStore
			fmt.Printf("Ensuring package '%s' is installed to local FPM store...\n", packagePathArg)
			resolvedOrg, resolvedAppName, resolvedVersion, _, resolvedAppModulePathInStore, storeErr := appstore.ManageAppInLocalStore(packagePathArg, cfg)
			if storeErr != nil {
				return fmt.Errorf("failed to manage package %s in local FPM store: %w", packagePathArg, storeErr)
			}
			// Update appOrg, appName, appVersion based on what ManageAppInLocalStore resolved from metadata
			appOrg = resolvedOrg
			appName = resolvedAppName
			appVersion = resolvedVersion
			appModulePathInFPMStore = resolvedAppModulePathInStore // This is the path to the app module, e.g. .../apporg/appname/version/appname
			fmt.Printf("Package %s/%s version %s successfully managed in local store. App module at: %s\n", appOrg, appName, appVersion, appModulePathInFPMStore)

		} else if os.IsNotExist(statErr) || (statInfo != nil && statInfo.IsDir()) {
			fmt.Printf("Package '%s' not found locally or is a directory. Attempting to resolve as remote identifier...\n", packagePathArg)
			var parsedOrg, parsedAppName, parsedVersion string // Renamed variables
			parts := strings.Split(packagePathArg, "/")
			if len(parts) == 2 {
				parsedOrg = strings.TrimSpace(parts[0]) // Renamed variable
				appAndVersionParts := strings.Split(parts[1], "==")
				parsedAppName = strings.TrimSpace(appAndVersionParts[0]) // Renamed variable
				if len(appAndVersionParts) == 2 {
					parsedVersion = strings.TrimSpace(appAndVersionParts[1])
				}
			} else {
				return fmt.Errorf("invalid remote package identifier format: '%s'. Expected <org>/<appName> or <org>/<appName>==<version>", packagePathArg)
			}
			if parsedOrg == "" || parsedAppName == "" { // Renamed variables
				return fmt.Errorf("invalid remote package identifier: Org ('%s') and AppName ('%s') must be specified in '%s'", parsedOrg, parsedAppName, packagePathArg)
			}

			appOrg = parsedOrg      // Use renamed variables
			appName = parsedAppName // Use renamed variables
			initialRequestedVersion := parsedVersion

			fmt.Printf("Attempting to install %s/%s (requested version: '%s')\n", appOrg, appName, initialRequestedVersion)

			resolvedVersion := initialRequestedVersion
			if resolvedVersion == "" || resolvedVersion == "latest" {
				fmt.Println("Resolving latest version from local FPM store...")
				versionsDir := filepath.Join(cfg.AppsBasePath, appOrg, appName)
				entries, readDirErr := os.ReadDir(versionsDir)
				foundLocally := false
				if readDirErr == nil {
					var availableVersions []string
					for _, entry := range entries {
						if entry.IsDir() {
							availableVersions = append(availableVersions, entry.Name())
						}
					}
					if len(availableVersions) > 0 {
						sort.Strings(availableVersions)
						resolvedVersion = availableVersions[len(availableVersions)-1]
						fmt.Printf("Latest version found in local store for %s/%s: %s\n", appOrg, appName, resolvedVersion)
						foundLocally = true
					}
				} else if !os.IsNotExist(readDirErr) {
					fmt.Fprintf(os.Stderr, "Warning: could not read local versions for %s/%s: %v\n", appOrg, appName, readDirErr)
				}
				if !foundLocally {
					fmt.Printf("No suitable version for %s/%s found in local store. Will try remote repositories with version hint '%s'.\n", appOrg, appName, initialRequestedVersion)
				}
			}
			appVersion = resolvedVersion

			if appVersion != "" && appVersion != "latest" {
				targetAppVersionPathInStore := filepath.Join(cfg.AppsBasePath, appOrg, appName, appVersion)
				potentialAppModulePath := filepath.Join(targetAppVersionPathInStore, appName)
				if _, hooksStatErr := os.Stat(filepath.Join(potentialAppModulePath, "hooks.py")); hooksStatErr == nil {
					fmt.Printf("Found valid installation of %s/%s version %s in local FPM store: %s\n", appOrg, appName, appVersion, potentialAppModulePath)
					appModulePathInFPMStore = potentialAppModulePath
				} else {
					fmt.Printf("Version %s for %s/%s found in local store path %s, but seems incomplete. Will try remote.\n", appVersion, appOrg, appName, targetAppVersionPathInStore)
				}
			}

			if appModulePathInFPMStore == "" {
				fmt.Printf("Package %s/%s version '%s' not found or incomplete in local FPM store. Trying remote repositories...\n", appOrg, appName, initialRequestedVersion)

				searchVersionForRemote := initialRequestedVersion
				if initialRequestedVersion == "" {
					searchVersionForRemote = "latest"
				}

				downloadedPkgInfo, findErr := repository.FindPackageInRepos(cfg, appOrg, appName, searchVersionForRemote)
				if findErr != nil {
					return fmt.Errorf("failed to find or download package '%s': %w", packagePathArg, findErr)
				}
				fmt.Printf("Package successfully resolved from repository '%s'. Cached file: %s\n", downloadedPkgInfo.RepositoryName, downloadedPkgInfo.LocalPath)

				fpmMeta, err := readMetadataFromFPMFile(downloadedPkgInfo.LocalPath)
				if err != nil {
					return fmt.Errorf("failed to read metadata from downloaded/cached FPM file %s: %w", downloadedPkgInfo.LocalPath, err)
				}
				appOrg = fpmMeta.Org
				appName = fpmMeta.AppName
				appVersion = fpmMeta.PackageVersion

				if appOrg == "" || appName == "" || appVersion == "" {
					return fmt.Errorf("org, app_name, or package_version missing from metadata in downloaded package %s", downloadedPkgInfo.LocalPath)
				}

				// Use appstore.ManageAppInLocalStore for the downloaded/cached file
				fmt.Printf("Ensuring downloaded package '%s' is installed to local FPM store...\n", downloadedPkgInfo.LocalPath)
				resolvedOrg, resolvedAppName, resolvedVersion, _, resolvedAppModulePathInStore, storeErr := appstore.ManageAppInLocalStore(downloadedPkgInfo.LocalPath, cfg)
				if storeErr != nil {
					return fmt.Errorf("failed to manage downloaded package %s in local FPM store: %w", downloadedPkgInfo.LocalPath, storeErr)
				}
				// Update appOrg, appName, appVersion based on what ManageAppInLocalStore resolved
				appOrg = resolvedOrg
				appName = resolvedAppName
				appVersion = resolvedVersion
				appModulePathInFPMStore = resolvedAppModulePathInStore
				fmt.Printf("Package %s/%s version %s (from remote) successfully managed in local store. App module at: %s\n", appOrg, appName, appVersion, appModulePathInFPMStore)
			}
		} else if statErr != nil {
			return fmt.Errorf("error checking package path '%s': %w", packagePathArg, statErr)
		}

		if appModulePathInFPMStore == "" {
			return fmt.Errorf("could not determine source application module path for installation")
		}
		if appOrg == "" || appName == "" || appVersion == "" {
			return fmt.Errorf("internal error: app metadata (org, name, version) not resolved before bench operations. Org: '%s', AppName: '%s', Version: '%s'", appOrg, appName, appVersion)
		}

		fmt.Printf("Proceeding with bench operations for %s/%s version %s using source: %s\n", appOrg, appName, appVersion, appModulePathInFPMStore)
		fmt.Printf("Target Bench Path: %s\n", benchPath)
		if siteName != "" {
			fmt.Printf("Target Site: %s\n", siteName)
		}

		absBenchPath, err := filepath.Abs(benchPath)
		if err != nil {
			return fmt.Errorf("failed to get absolute path for bench directory '%s': %w", benchPath, err)
		}

		// --- Pre-install checks ---
		// The target bench has no network, so anything the install would need to fetch
		// has to be refused now, before the bench is touched.
		baseVersionPath := filepath.Dir(appModulePathInFPMStore)
		storeMeta, metaErr := readMetadataFromFPMStore(baseVersionPath)
		if metaErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not read package metadata from local store: %v\n", metaErr)
			storeMeta = &metadata.AppMetadata{}
		}
		packageID := fmt.Sprintf("%s/%s==%s", appOrg, appName, appVersion)
		if err := checkRequiredApps(cfg, storeMeta, absBenchPath, packageID, siteName != "", installSkipRequiredAppsCheck); err != nil {
			return err
		}
		wheelsInStore := vendoredWheelsDir(baseVersionPath)
		if wheelsInStore != "" {
			if err := checkWheelTarget(storeMeta, benchPythonVersion(absBenchPath), installIgnorePlatformMismatch); err != nil {
				return err
			}
		}

		// <bench>/apps/<app> must be the app's *package root* — the directory holding
		// pyproject.toml / requirements.txt with the Python module <app>/ inside it —
		// which is what `pip install -e`, Frappe (`apps/<app>/<app>/public` for assets)
		// and bench all expect. In the local store that is the version directory, whose
		// extra files (app_metadata.json, wheels/, the stored .fpm) are ignored by all of
		// them.
		originalPath := baseVersionPath
		linkName := filepath.Join(absBenchPath, "apps", appName)

		fmt.Printf("Preparing to symlink app '%s' from '%s' to '%s'\n", appName, originalPath, linkName)
		linkDir := filepath.Dir(linkName)
		if err := os.MkdirAll(linkDir, os.ModePerm); err != nil {
			return fmt.Errorf("failed to create directory for symlink '%s': %w", linkDir, err)
		}
		if _, err := os.Lstat(linkName); err == nil {
			fmt.Printf("Removing existing file/symlink at '%s'\n", linkName)
			if err := os.RemoveAll(linkName); err != nil {
				return fmt.Errorf("failed to remove existing file/symlink at '%s': %w", linkName, err)
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("failed to check symlink path '%s': %w", linkName, err)
		}
		if err := os.Symlink(originalPath, linkName); err != nil {
			return fmt.Errorf("failed to create symlink from '%s' to '%s': %w", originalPath, linkName, err)
		}
		fmt.Printf("Successfully symlinked app '%s' into bench.\n", appName)

		// --- Asset deployment ---
		// Exactly what `bench build --app <app> --using-cached` does with prebuilt output:
		// link sites/assets/<app> to the app's public/ directory and merge the app's
		// built bundles into sites/assets/assets.json and assets-rtl.json. Nothing is
		// compiled here; the package ships <app>/public/dist from `fpm package --bench-path`.
		if err := deployPackagedAssets(absBenchPath, appName, appModulePathInFPMStore, baseVersionPath); err != nil {
			return err
		}

		currentWD, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get current working directory: %w", err)
		}
		fmt.Printf("Changing working directory to bench path: %s\n", absBenchPath)
		if err := os.Chdir(absBenchPath); err != nil {
			return fmt.Errorf("failed to change working directory to bench path '%s': %w", absBenchPath, err)
		}
		defer func() {
			fmt.Printf("Changing working directory back to: %s\n", currentWD)
			if err := os.Chdir(currentWD); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to change directory back to '%s': %v\n", currentWD, err)
			}
		}()

		pipAppPath := filepath.Join("./apps", appName)
		// A package that bundles wheels/ resolves its Python dependencies from that
		// directory instead of PyPI, so the install works without network access.
		// Packages without vendored wheels keep the original online behaviour.
		if wheelsInStore != "" {
			fmt.Printf("Detected vendored wheels in package. Installing offline from %s\n", wheelsInStore)
		} else {
			fmt.Printf("No vendored wheels in package; resolving Python dependencies from the network.\n")
		}
		pipCmdArgs := buildPipInstallArgs(pipAppPath, wheelsInStore)
		fmt.Printf("Running pip install for '%s': ./env/bin/pip %s\n", appName, strings.Join(pipCmdArgs, " "))
		pipExecCmd := exec.Command("./env/bin/pip", pipCmdArgs...)
		output, err := pipExecCmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("pip install for app '%s' failed:\n%s\nError: %w", appName, string(output), err)
		}
		fmt.Printf("Pip install for app '%s' successful.\nOutput:\n%s\n", appName, string(output))

		appsTxtPath := filepath.Join(absBenchPath, "sites", "apps.txt")
		appNameString := appName
		logMessagePrefix := fmt.Sprintf("apps.txt (%s):", appsTxtPath)
		sitesDir := filepath.Dir(appsTxtPath)
		if err := os.MkdirAll(sitesDir, os.ModePerm); err != nil {
			return fmt.Errorf("%s Failed to create sites directory '%s': %w", logMessagePrefix, sitesDir, err)
		}
		fileContentBytes, err := os.ReadFile(appsTxtPath)
		var appsInFile []string
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Printf("%s File does not exist, will create it with app '%s'.\n", logMessagePrefix, appNameString)
				appsInFile = []string{}
			} else {
				return fmt.Errorf("%s Failed to read: %w", logMessagePrefix, err)
			}
		} else {
			fileContent := string(fileContentBytes)
			rawApps := strings.Split(strings.TrimSpace(fileContent), "\n")
			for _, a := range rawApps {
				trimmedApp := strings.TrimSpace(a)
				if trimmedApp != "" {
					appsInFile = append(appsInFile, trimmedApp)
				}
			}
		}
		found := false
		for _, existingApp := range appsInFile {
			if existingApp == appNameString {
				found = true
				break
			}
		}
		if found {
			fmt.Printf("%s App '%s' already listed.\n", logMessagePrefix, appNameString)
		} else {
			fmt.Printf("%s App '%s' not found, adding it.\n", logMessagePrefix, appNameString)
			appsInFile = append(appsInFile, appNameString)
			newContent := strings.Join(appsInFile, "\n")
			if len(appsInFile) > 0 {
				newContent += "\n"
			}
			if err := os.WriteFile(appsTxtPath, []byte(newContent), 0644); err != nil {
				return fmt.Errorf("%s Failed to write: %w", logMessagePrefix, err)
			}
			fmt.Printf("%s Successfully updated with app '%s'.\n", logMessagePrefix, appNameString)
		}

		// Adding the app to the bench makes it available; installing it onto a site is a
		// separate step that only the site's own bench can perform.
		if siteName != "" {
			if err := installAppOnSite(absBenchPath, siteName, appName); err != nil {
				return err
			}
		} else {
			fmt.Printf("\nApp '%s' is installed in the bench. "+
				"Pass --site <site> to also install it onto a site.\n", appName)
		}
		return nil
	}
}

// benchPythonExecutable is the interpreter of a Frappe bench's virtualenv, relative to
// the bench root — the same place the pip used for the app install comes from.
const benchPythonExecutable = "./env/bin/python"

// installAppOnSite runs `bench --site <site> install-app <app>`, which is what actually
// makes an app active on a site: creating its DocTypes and running its patches.
//
// It is deliberately delegated to Frappe rather than reimplemented. Site installation
// touches the database and runs the app's own migrations, and Frappe is the only thing
// that knows how to do that correctly for a given version.
//
// The `bench` CLI is not required to be installed, in the virtualenv or anywhere: for
// any frappe command, bench merely chdirs into <bench>/sites and execs
// `env/bin/python -m frappe.utils.bench_helper frappe <args>` (bench/cli.py,
// frappe_cmd). That is invoked directly, so an install works in benches where bench
// lives outside the virtualenv (a user or system install, container images).
func installAppOnSite(benchPath, siteName, appName string) error {
	currentWD, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to determine current directory: %w", err)
	}
	if err := os.Chdir(benchPath); err != nil {
		return fmt.Errorf("failed to change directory to bench path '%s': %w", benchPath, err)
	}
	defer func() {
		if err := os.Chdir(currentWD); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to change directory back to '%s': %v\n", currentWD, err)
		}
	}()

	if _, statErr := os.Stat(benchPythonExecutable); statErr != nil {
		return fmt.Errorf("cannot install app '%s' onto site '%s': %s not found in bench '%s'. "+
			"The app is installed in the bench; run 'bench --site %s install-app %s' yourself",
			appName, siteName, benchPythonExecutable, benchPath, siteName, appName)
	}

	args := []string{"-m", "frappe.utils.bench_helper", "frappe", "--site", siteName, "install-app", appName}
	fmt.Printf("\nInstalling app '%s' onto site '%s': (cd sites && %s %s)\n",
		appName, siteName, benchPythonExecutable, strings.Join(args, " "))

	python, err := filepath.Abs(benchPythonExecutable)
	if err != nil {
		return err
	}
	execCmd := exec.Command(python, args...)
	execCmd.Dir = filepath.Join(benchPath, "sites")
	output, err := execCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to install app '%s' onto site '%s':\n%s\nError: %w",
			appName, siteName, string(output), err)
	}

	fmt.Printf("Successfully installed app '%s' onto site '%s'.\nOutput:\n%s\n",
		appName, siteName, string(output))

	// Frappe's installer clears the site cache itself, but make it explicit rather
	// than rely on that: the site's cached boot info, hooks and website pages must not
	// outlive the app set they were computed for. Not fatal — the app is installed.
	clearArgs := []string{"-m", "frappe.utils.bench_helper", "frappe", "--site", siteName, "clear-cache"}
	fmt.Printf("Clearing site cache: (cd sites && %s %s)\n", benchPythonExecutable, strings.Join(clearArgs, " "))
	clearCmd := exec.Command(python, clearArgs...)
	clearCmd.Dir = filepath.Join(benchPath, "sites")
	if out, err := clearCmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: clear-cache for site '%s' failed: %v\n%s\nRun 'bench --site %s clear-cache' yourself.\n",
			siteName, err, strings.TrimSpace(string(out)), siteName)
	} else {
		fmt.Printf("Cleared cache for site '%s'.\n", siteName)
	}
	fmt.Printf("Note: running web/worker processes load an app when they start; restart them (bench restart) for '%s' to be served.\n", appName)
	return nil
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

// Helper function to read metadata from an installed FPM app directory's app_metadata.json
// vendoredWheelsDir returns the package's vendored wheels directory, or "" when the
// package bundles none and its dependencies must be resolved from the network.
func vendoredWheelsDir(baseVersionPath string) string {
	dir := filepath.Join(baseVersionPath, wheels.DirName)
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		return dir
	}
	return ""
}

// buildPipInstallArgs returns the pip arguments for installing the app into the bench.
// An empty wheelsDir keeps the original network-resolving behaviour; otherwise pip is
// pinned to the vendored wheels so the install never reaches PyPI.
func buildPipInstallArgs(pipAppPath, wheelsDir string) []string {
	if wheelsDir == "" {
		return []string{"install", "-q", "-e", pipAppPath}
	}
	return []string{"install", "-q", "--no-index", "--find-links", wheelsDir, "-e", pipAppPath}
}

// checkWheelTarget refuses to start an offline install whose vendored wheels cannot
// match this bench. Once pip runs with --no-index there is no network fallback, so a
// platform or interpreter mismatch is caught here, where the fix is actionable, not
// as a pip resolution error halfway through. The check is skipped for wheels built
// for the packaging host, whose exact tag is unknown; pip decides those.
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

// wheelPlatformMatchesHost reports whether any of the vendored platform tags names
// the given OS and architecture. Tags name both, e.g. manylinux2014_x86_64.
func wheelPlatformMatchesHost(tag, goos, goarch string) bool {
	// Linux tags spell the architecture the uname way (x86_64, aarch64); macOS tags
	// use Apple's (x86_64, arm64). Accept either spelling.
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

// benchPythonVersion reports the MAJOR.MINOR version of the bench's virtualenv
// interpreter, or "" when it cannot be determined (the check is then skipped).
func benchPythonVersion(benchPath string) string {
	python := filepath.Join(benchPath, "env", "bin", "python")
	out, err := exec.Command(python, "-c", "import sys; print('%d.%d' % sys.version_info[:2])").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// checkRequiredApps enforces the offline contract for hooks.py required_apps: every
// pinned requirement, transitively, must already be in the local FPM store — or
// already be present in the bench itself (installed by other means: bench get-app, or
// baked into an image such as the ERPNext single-node one) at the version the
// package was built against. Nothing is fetched. When the app is also being installed
// onto a site, each store-provided requirement must already be in the bench too,
// since `bench install-app` installs required apps from the bench's own apps directory.
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

// benchAppNames lists the apps a bench has: those in sites/apps.txt plus any
// directory under apps/ that holds a Frappe app module.
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

// deployPackagedAssets makes the package's assets servable, the way bench does it.
func deployPackagedAssets(benchPath, appName, appModulePath, baseVersionPath string) error {
	// Packages built before fpm ran the asset build itself ship a compiled_assets/
	// directory mirroring what is served at /assets/<app>/. Those files belong in the
	// app's public/ directory, which is what sites/assets/<app> links to.
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

	total := len(deployed.LTR) + len(deployed.RTL)
	if total == 0 {
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

	// Frappe caches the manifest in redis_cache; bench build deletes that key after
	// writing the file, and so does this. A bench whose redis is not running (or
	// not configured yet) just serves the new file when it next starts.
	if err := assets.InvalidateCache(benchPath); err != nil {
		fmt.Fprintf(os.Stderr, "Note: could not clear the assets_json cache in redis (%v); "+
			"a running site picks up the new bundles after 'bench --site <site> clear-cache' or a restart.\n", err)
	} else {
		fmt.Printf("Cleared assets_json cache in redis_cache.\n")
	}
	return nil
}

// goArchToWheelArch maps a Go architecture name onto the spelling used in wheel tags.
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

// extractFPMArchive function removed as its functionality is now in appstore.ManageAppInLocalStore

func init() {
	installCmd.Flags().String("bench-path", "", "Path to the Frappe bench directory")
	if err := installCmd.MarkFlagRequired("bench-path"); err != nil {
		fmt.Fprintf(os.Stderr, "Error marking 'bench-path' flag required for install cmd: %v\n", err)
	}
	installCmd.Flags().String("site", "", "Site to install the app onto after adding it to the bench (runs 'bench --site <site> install-app')")
	installCmd.Flags().BoolVar(&installSkipRequiredAppsCheck, "skip-required-apps-check", false, "Do not fail when a required app (hooks.py required_apps) is missing from the local FPM store or bench; for benches whose required apps were installed outside fpm")
	installCmd.Flags().BoolVar(&installIgnorePlatformMismatch, "ignore-platform-mismatch", false, "Do not fail when the package's vendored wheels were built for another platform or Python version; pip then decides")
	rootCmd.AddCommand(installCmd)
}
