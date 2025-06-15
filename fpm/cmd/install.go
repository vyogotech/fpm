package cmd

import (
	"archive/zip"
	"encoding/json" // For readMetadataFromFPMFile helper
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort" // For "latest" version resolution from local store
	"strings"

	"fpm/internal/config"
	"fpm/internal/metadata"
	"fpm/internal/repository"
	"os/exec"

	"github.com/spf13/cobra"
)

// copyRegularFile copies a single regular file from src to dst.
// It creates the destination file with specified permissions.
// Note: This is duplicated from cmd/package.go. Consider moving to a shared utility if used more broadly.
func copyRegularFile(src, dst string, perm os.FileMode) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open source file %s: %w", src, err)
	}
	defer srcFile.Close()

	// Ensure destination directory exists
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil { // 0o755 for parent dirs
		return fmt.Errorf("failed to create parent directory for %s: %w", dst, err)
	}

	dstFile, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("failed to create destination file %s: %w", dst, err)
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	if err != nil {
		return fmt.Errorf("failed to copy from %s to %s: %w", src, dst, err)
	}
	return nil
}


// copyDirContents recursively copies contents from src to dst.
// Assumes dst directory already exists or can be created by MkdirAll for subdirectories.
func copyDirContents(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err // Propagate errors from Walk itself
		}

		// Construct the destination path
		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return fmt.Errorf("failed to get relative path for %s from %s: %w", path, src, err)
		}
		dstPath := filepath.Join(dst, relPath)

		if info.IsDir() {
			// Create the directory in the destination with the same permissions
			if err := os.MkdirAll(dstPath, info.Mode()); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", dstPath, err)
			}
			return nil // Directory processed, continue walking
		}

		// It's a file, so copy it
		// Ensure the destination directory for the file exists
		if err := os.MkdirAll(filepath.Dir(dstPath), os.ModePerm); err != nil { // Use ModePerm for parent dirs for simplicity
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
		packagePathArg := args[0]
		var appModulePathInFPMStore string // Path to the app module like ~/.fpm/apps/org/app/ver/app
		var appOrg, appName, appVersion string // Metadata extracted/resolved
		// var currentPkgMeta *metadata.AppMetadata // Not strictly needed if appOrg, appName, appVersion are correctly sourced for bench ops

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

		fmt.Printf("Attempting to install '%s'\n", packagePathArg)
		statInfo, statErr := os.Stat(packagePathArg)

		if statErr == nil && !statInfo.IsDir() { // Case 1: Argument is a local .fpm file
			fmt.Printf("Local package file found: %s\n", packagePathArg)
			localFpmMeta, err := readMetadataFromFPMFile(packagePathArg)
			if err != nil {
				return fmt.Errorf("failed to read metadata from local FPM file %s: %w", packagePathArg, err)
			}
			if localFpmMeta.Org == "" || localFpmMeta.AppName == "" || localFpmMeta.PackageVersion == "" {
				return fmt.Errorf("org, app_name, or package_version missing from metadata in %s", packagePathArg)
			}

			appOrg = localFpmMeta.Org
			appName = localFpmMeta.AppName
			appVersion = localFpmMeta.PackageVersion
			fmt.Printf("Installing from local file: %s/%s version %s\n", appOrg, appName, appVersion)

			targetAppVersionPathInStore := filepath.Join(cfg.AppsBasePath, appOrg, appName, appVersion)
			appModulePathInFPMStore = filepath.Join(targetAppVersionPathInStore, appName)

			fmt.Printf("Ensuring package is installed to local FPM store: %s\n", targetAppVersionPathInStore)
			if err := os.RemoveAll(targetAppVersionPathInStore); err != nil {
				return fmt.Errorf("failed to clear existing content at %s: %w", targetAppVersionPathInStore, err)
			}
			if err := os.MkdirAll(targetAppVersionPathInStore, 0o755); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", targetAppVersionPathInStore, err)
			}
			if err := extractFPMArchive(packagePathArg, targetAppVersionPathInStore); err != nil {
				return fmt.Errorf("failed to extract %s to %s: %w", packagePathArg, targetAppVersionPathInStore, err)
			}
			fmt.Printf("Package content installed to %s\n", targetAppVersionPathInStore)

			// Store the original .fpm file
			originalFpmFilename := filepath.Base(packagePathArg)
			storedFpmPath := filepath.Join(targetAppVersionPathInStore, "_"+originalFpmFilename)
			if err := copyRegularFile(packagePathArg, storedFpmPath, 0o644); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: failed to store original .fpm package from local file in FPM store at %s: %v\n", storedFpmPath, err)
			} else {
				fmt.Printf("Stored original .fpm package in FPM store: %s\n", storedFpmPath)
			}

		} else if os.IsNotExist(statErr) || (statInfo != nil && statInfo.IsDir()) { // Case 2: Remote identifier
			fmt.Printf("Package '%s' not found locally or is a directory. Attempting to resolve as remote identifier...\n", packagePathArg)
			var parsedGroupID, parsedArtifactID, parsedVersion string
			parts := strings.Split(packagePathArg, "/")
			if len(parts) == 2 {
				parsedGroupID = strings.TrimSpace(parts[0])
				appAndVersionParts := strings.Split(parts[1], "==")
				parsedArtifactID = strings.TrimSpace(appAndVersionParts[0])
				if len(appAndVersionParts) == 2 {
					parsedVersion = strings.TrimSpace(appAndVersionParts[1])
				}
			} else {
				return fmt.Errorf("invalid remote package identifier format: '%s'. Expected <group>/<artifact> or <group>/<artifact>==<version>", packagePathArg)
			}
			if parsedGroupID == "" || parsedArtifactID == "" {
				return fmt.Errorf("invalid remote package identifier: groupID ('%s') and artifactID ('%s') must be specified in '%s'", parsedGroupID, parsedArtifactID, packagePathArg)
			}

			appOrg = parsedGroupID
			appName = parsedArtifactID
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

				targetAppVersionPathInStore := filepath.Join(cfg.AppsBasePath, appOrg, appName, appVersion)
				appModulePathInFPMStore = filepath.Join(targetAppVersionPathInStore, appName)

				fmt.Printf("Installing downloaded package to local FPM store: %s\n", targetAppVersionPathInStore)
				if err := os.RemoveAll(targetAppVersionPathInStore); err != nil {
					return fmt.Errorf("failed to clear existing content at %s: %w", targetAppVersionPathInStore, err)
				}
				if err := os.MkdirAll(targetAppVersionPathInStore, 0o755); err != nil {
					return fmt.Errorf("failed to create directory %s: %w", targetAppVersionPathInStore, err)
				}
				if err := extractFPMArchive(downloadedPkgInfo.LocalPath, targetAppVersionPathInStore); err != nil {
					return fmt.Errorf("failed to extract %s to %s: %w", downloadedPkgInfo.LocalPath, targetAppVersionPathInStore, err)
				}
				fmt.Printf("Package content installed from FPM cache to FPM store at %s\n", targetAppVersionPathInStore)

				// Store the original (cached) .fpm file
				originalFpmFilename := filepath.Base(downloadedPkgInfo.LocalPath)
				storedFpmPath := filepath.Join(targetAppVersionPathInStore, "_"+originalFpmFilename)
				if err := copyRegularFile(downloadedPkgInfo.LocalPath, storedFpmPath, 0o644); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "Warning: failed to store original .fpm package from cache in FPM store at %s: %v\n", storedFpmPath, err)
				} else {
					fmt.Printf("Stored original .fpm package in FPM store: %s\n", storedFpmPath)
				}
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
			fmt.Printf("Target Site (for future use): %s\n", siteName)
		}

		absBenchPath, err := filepath.Abs(benchPath)
		if err != nil {
			return fmt.Errorf("failed to get absolute path for bench directory '%s': %w", benchPath, err)
		}

		originalPath := appModulePathInFPMStore
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
		pipCmdArgs := []string{"install", "-q", "-e", pipAppPath}
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

		fmt.Println("\nPlaceholder: Next steps: Running migrations for a site, etc.")
		return nil
	},
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
func readMetadataFromFPMStore(installedAppVersionPath string) (*metadata.AppMetadata, error) {
	metaFilePath := filepath.Join(installedAppVersionPath, "app_metadata.json")
	if _, err := os.Stat(metaFilePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("app_metadata.json not found in installed app path %s", installedAppVersionPath)
	}
	return metadata.LoadAppMetadata(installedAppVersionPath)
}

// Helper function to extract an FPM archive to a target directory
func extractFPMArchive(fpmPath string, targetDir string) error {
	r, err := zip.OpenReader(fpmPath)
	if err != nil {
		return fmt.Errorf("failed to open FPM package %s for extraction: %w", fpmPath, err)
	}
	defer r.Close()

	for _, f := range r.File {
		extractedFilePath := filepath.Join(targetDir, f.Name)
		if !strings.HasPrefix(extractedFilePath, filepath.Clean(targetDir)+string(os.PathSeparator)) {
			return fmt.Errorf("illegal file path in zip: '%s' (targets outside '%s')", f.Name, targetDir)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(extractedFilePath, f.Mode()); err != nil {
				return fmt.Errorf("failed to create directory %s during extraction: %w", extractedFilePath, err)
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(extractedFilePath), os.ModePerm); err != nil {
			return fmt.Errorf("failed to create parent directory for %s during extraction: %w", extractedFilePath, err)
		}

		outFile, err := os.OpenFile(extractedFilePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return fmt.Errorf("failed to open file for writing %s during extraction: %w", extractedFilePath, err)
		}

		rc, err := f.Open()
		if err != nil {
			outFile.Close()
			return fmt.Errorf("failed to open file in zip %s during extraction: %w", f.Name, err)
		}

		_, copyErr := io.Copy(outFile, rc)
		closeRcErr := rc.Close()
		closeOutFileErr := outFile.Close()

		if copyErr != nil {
			return fmt.Errorf("failed to copy content of %s to %s during extraction: %w", f.Name, extractedFilePath, copyErr)
		}
		if closeRcErr != nil {
			return fmt.Errorf("failed to close zip entry %s after copying: %w", f.Name, closeRcErr)
		}
		if closeOutFileErr != nil {
			return fmt.Errorf("failed to close output file %s after copying: %w", extractedFilePath, closeOutFileErr)
		}
	}
	return nil
}

func init() {
	installCmd.Flags().String("bench-path", "", "Path to the Frappe bench directory")
	if err := installCmd.MarkFlagRequired("bench-path"); err != nil {
		fmt.Fprintf(os.Stderr, "Error marking 'bench-path' flag required for install cmd: %v\n", err)
	}
	installCmd.Flags().String("site", "", "Name of the site to install the app to (optional)")
	rootCmd.AddCommand(installCmd)
}
