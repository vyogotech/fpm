package cmd

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"fpm/internal/config" // Added import for FPM config
	"fpm/internal/metadata"
	"fpm/internal/repository" // Added for repository-based fetching
	"os/exec"                 // Added for running pip command

	"github.com/spf13/cobra"
)

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
	Short: "Install a Frappe app from a local .fpm package or a remote repository",
	Long: `Installs a Frappe application into a Frappe bench.
The package can be a path to a local .fpm file or a remote package identifier
in the format <group>/<artifact> or <group>/<artifact>==<version>.
If the version is not specified for a remote package, the latest version is assumed.`,
	Args: cobra.ExactArgs(1), // Ensures exactly one argument is provided.
	RunE: func(cmd *cobra.Command, args []string) error {
		packagePathArg := args[0] // Original argument, could be path or identifier
		var finalPackagePath string   // This will be the path to the .fpm file to install

		benchPath, err := cmd.Flags().GetString("bench-path")
		if err != nil {
			return fmt.Errorf("error retrieving 'bench-path' flag: %w", err)
		}

		siteName, err := cmd.Flags().GetString("site") // Keep this for future use
		if err != nil {
			return fmt.Errorf("error retrieving 'site' flag: %w", err)
		}

		// Try to stat the path first. If it's a valid local file, use it.
		// Otherwise, assume it's a remote package identifier.
		statInfo, statErr := os.Stat(packagePathArg)
		if statErr == nil && !statInfo.IsDir() {
			fmt.Printf("Local package file found: %s\n", packagePathArg)
			finalPackagePath = packagePathArg
		} else if os.IsNotExist(statErr) || (statInfo != nil && statInfo.IsDir()) {
			// Path doesn't exist as a file, or it's a directory; treat as remote identifier
			fmt.Printf("Package '%s' not found locally or is a directory. Attempting to resolve from repositories...\n", packagePathArg)

			var groupID, artifactID, version string
			parts := strings.Split(packagePathArg, "/")
			if len(parts) == 2 {
				groupID = strings.TrimSpace(parts[0])
				appAndVersion := strings.Split(parts[1], "==")
				artifactID = strings.TrimSpace(appAndVersion[0])
				if len(appAndVersion) == 2 {
					version = strings.TrimSpace(appAndVersion[1])
				}
			} else {
				return fmt.Errorf("invalid remote package identifier format: '%s'. Expected <group>/<artifact> or <group>/<artifact>==<version>", packagePathArg)
			}

			if groupID == "" || artifactID == "" {
				return fmt.Errorf("invalid remote package identifier: groupID ('%s') and artifactID ('%s') must be specified in '%s'", groupID, artifactID, packagePathArg)
			}

			cfg, configErr := config.InitConfig()
			if configErr != nil {
				return fmt.Errorf("failed to initialize FPM configuration: %w", configErr)
			}

			fmt.Printf("Attempting to find package %s/%s (version: '%s') in configured repositories...\n", groupID, artifactID, version)
			downloadedPkg, findErr := repository.FindPackageInRepos(cfg, groupID, artifactID, version)
			if findErr != nil {
				return fmt.Errorf("failed to find or download package '%s': %w", packagePathArg, findErr)
			}

			fmt.Printf("Package successfully resolved from repository '%s'. Using cached file: %s\n", downloadedPkg.RepositoryName, downloadedPkg.LocalPath)
			finalPackagePath = downloadedPkg.LocalPath
		} else if statErr != nil { // Other error from os.Stat
			return fmt.Errorf("error checking package path '%s': %w", packagePathArg, statErr)
		}

		// Validate finalPackagePath before proceeding
		fileInfoForInstall, err := os.Stat(finalPackagePath)
		if err != nil {
			return fmt.Errorf("critical error: resolved package path '%s' is not accessible: %w", finalPackagePath, err)
		}
		if fileInfoForInstall.IsDir() {
			return fmt.Errorf("critical error: resolved package path '%s' is a directory, should be a .fpm file", finalPackagePath)
		}

		fmt.Printf("Starting installation of package: %s\n", finalPackagePath)
		fmt.Printf("Target Bench Path: %s\n", benchPath)
		if siteName != "" {
			fmt.Printf("Target Site (for future use): %s\n", siteName)
		}

		// 2. Unzip .fpm Package to a Temporary Directory
		tmpDir, err := os.MkdirTemp("", "fpm-pkg-extract-*")
		if err != nil {
			return fmt.Errorf("failed to create temporary directory: %w", err)
		}
		defer os.RemoveAll(tmpDir) // Ensure cleanup
		fmt.Printf("Extracting package to temporary directory: %s\n", tmpDir)

		r, err := zip.OpenReader(finalPackagePath)
		if err != nil {
			return fmt.Errorf("failed to open package %s: %w", finalPackagePath, err)
		}
		defer r.Close()

		for _, f := range r.File {
			fpath := filepath.Join(tmpDir, f.Name)

			// Path traversal protection
			if !strings.HasPrefix(fpath, filepath.Clean(tmpDir)+string(os.PathSeparator)) {
				return fmt.Errorf("illegal file path in zip: %s", f.Name)
			}

			if f.FileInfo().IsDir() {
				if err := os.MkdirAll(fpath, os.ModePerm); err != nil {
					return fmt.Errorf("failed to create directory structure %s: %w", fpath, err)
				}
				continue
			}

			if err := os.MkdirAll(filepath.Dir(fpath), os.ModePerm); err != nil {
				return fmt.Errorf("failed to create directory for %s: %w", fpath, err)
			}

			outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
			if err != nil {
				return fmt.Errorf("failed to open file for writing %s: %w", fpath, err)
			}

			rc, err := f.Open()
			if err != nil {
				outFile.Close()
				return fmt.Errorf("failed to open file in zip %s: %w", f.Name, err)
			}

			_, err = io.Copy(outFile, rc)
			outFile.Close() // Close before checking io.Copy error
			rc.Close()

			if err != nil {
				return fmt.Errorf("failed to copy content of %s: %w", f.Name, err)
			}
		}
		fmt.Println("Package extracted successfully.")

		// 3. Read app_metadata.json
		// Assuming LoadAppMetadata expects the path to the directory containing app_metadata.json
		pkgMeta, err := metadata.LoadAppMetadata(tmpDir)
		if err != nil {
			return fmt.Errorf("failed to load package metadata from %s: %w", tmpDir, err)
		}
		fmt.Printf("Package metadata loaded: AppName='%s', Version='%s', Org='%s'\n", pkgMeta.AppName, pkgMeta.PackageVersion, pkgMeta.Org)

		// 4. Validate Required Metadata Fields
		if pkgMeta.AppName == "" {
			return fmt.Errorf("package metadata validation failed: 'app_name' is missing or empty")
		}
		if pkgMeta.PackageVersion == "" {
			return fmt.Errorf("package metadata validation failed: 'package_version' is missing or empty")
		}
		if pkgMeta.Org == "" { // Org is now considered required for published packages
			return fmt.Errorf("package metadata validation failed: 'org' is missing or empty")
		}
		fmt.Println("Package metadata validated.")

		// 5. New Package Structure Validation (app module at root of package)
		appModulePathInTmp := filepath.Join(tmpDir, pkgMeta.AppName)
		info, err := os.Stat(appModulePathInTmp)
		if os.IsNotExist(err) {
			return fmt.Errorf("package structure validation failed: app module directory '%s' not found at the root of the package", pkgMeta.AppName)
		}
		if err != nil {
			return fmt.Errorf("error checking app module directory '%s': %w", appModulePathInTmp, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("package structure validation failed: app module '%s' at the root is not a directory", pkgMeta.AppName)
		}
		fmt.Printf("App module directory '%s' found at package root.\n", pkgMeta.AppName)

		// Load FPM configuration (needed for AppsBasePath)
		fpmConfig, err := config.LoadConfig()
		if err != nil {
			// For actual installation, this might be a fatal error or prompt for path.
			// For now, if config fails, we can't determine AppsBasePath.
			return fmt.Errorf("failed to load FPM configuration: %w. Cannot determine installation path", err)
		}
		fmt.Printf("Loaded FPM Config: AppsBasePath is '%s'\n", fpmConfig.AppsBasePath)


		// 1. Determine Target Extraction Path
		targetAppPath := filepath.Join(fpmConfig.AppsBasePath, pkgMeta.Org, pkgMeta.AppName, pkgMeta.PackageVersion)
		fmt.Printf("Target installation directory: %s\n", targetAppPath)

		// 2. Create Target Directory Structure
		// Check if targetAppPath already exists and is not empty? For now, MkdirAll will ensure it exists.
		// Consider adding a --force flag or cleanup logic if it exists from a previous failed install.
		if err := os.MkdirAll(targetAppPath, os.ModePerm); err != nil {
			return fmt.Errorf("failed to create target app path %s: %w", targetAppPath, err)
		}
		fmt.Printf("Target directory %s created/ensured.\n", targetAppPath)

		// 3. Copy Package Contents to Target Path
		fmt.Println("Copying package contents to target installation path...")

		// Copy the app module directory (e.g., tmpDir/my_app -> targetAppPath/my_app)
		srcAppModuleDir := filepath.Join(tmpDir, pkgMeta.AppName)
		destAppModuleDir := filepath.Join(targetAppPath, pkgMeta.AppName)

		fmt.Printf("Copying app module from %s to %s...\n", srcAppModuleDir, destAppModuleDir)
		// Ensure parent of destAppModuleDir (i.e. targetAppPath) exists for the app module.
		// MkdirAll on targetAppPath earlier ensures targetAppPath exists.
		// copyDirContents copies the *contents* of srcAppModuleDir into destAppModuleDir.
		// So, we need to ensure destAppModuleDir itself exists.
		if err := os.MkdirAll(destAppModuleDir, os.ModePerm); err != nil {
			return fmt.Errorf("failed to create destination app module directory %s: %w", destAppModuleDir, err)
		}
		if err := copyDirContents(srcAppModuleDir, destAppModuleDir); err != nil {
			return fmt.Errorf("failed to copy app module from %s to %s: %w", srcAppModuleDir, destAppModuleDir, err)
		}
		fmt.Printf("App module copied successfully to %s.\n", destAppModuleDir)

		// Copy other root files/directories from tmpDir to targetAppPath
		otherPackageItems, err := os.ReadDir(tmpDir)
		if err != nil {
			return fmt.Errorf("failed to read contents of temporary package directory %s: %w", tmpDir, err)
		}

		for _, item := range otherPackageItems {
			itemName := item.Name()
			// Skip the app module (already copied) and app_metadata.json (not part of app code to be copied to this level)
			if itemName == pkgMeta.AppName || itemName == "app_metadata.json" {
				continue
			}

			srcItemPath := filepath.Join(tmpDir, itemName)
			dstItemPath := filepath.Join(targetAppPath, itemName)

			fmt.Printf("Processing package item: %s\n", itemName)
			if item.IsDir() {
				fmt.Printf("Copying directory from %s to %s...\n", srcItemPath, dstItemPath)
				// Ensure specific destination directory for this item exists before copying contents.
				if err := os.MkdirAll(dstItemPath, os.ModePerm); err != nil {
					return fmt.Errorf("failed to create destination directory %s: %w", dstItemPath, err)
				}
				if err := copyDirContents(srcItemPath, dstItemPath); err != nil {
					return fmt.Errorf("failed to copy directory item %s: %w", itemName, err)
				}
			} else { // It's a file
				fmt.Printf("Copying file from %s to %s...\n", srcItemPath, dstItemPath)
				// Parent directory (targetAppPath) for dstItemPath is already created.

				srcF, openErr := os.Open(srcItemPath)
				if openErr != nil {
					return fmt.Errorf("failed to open source file %s: %w", srcItemPath, openErr)
				}

				// Get original file mode
				fileInfo, statErr := srcF.Stat()
				if statErr != nil {
					srcF.Close()
					return fmt.Errorf("failed to stat source file %s: %w", srcItemPath, statErr)
				}

				dstF, createErr := os.OpenFile(dstItemPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fileInfo.Mode())
				if createErr != nil {
					srcF.Close()
					return fmt.Errorf("failed to create destination file %s: %w", dstItemPath, createErr)
				}

				_, copyErr := io.Copy(dstF, srcF)

				closeErrSrc := srcF.Close()
				closeErrDst := dstF.Close()

				if copyErr != nil {
					return fmt.Errorf("failed to copy file item %s: %w", itemName, copyErr)
				}
				if closeErrSrc != nil {
					return fmt.Errorf("failed to close source file %s: %w", srcItemPath, closeErrSrc)
				}
				if closeErrDst != nil {
					return fmt.Errorf("failed to close destination file %s: %w", dstItemPath, closeErrDst)
				}
			}
			fmt.Printf("Successfully copied %s.\n", itemName)
		}
		fmt.Println("All package contents processed.")

		// TODO: Copy compiled_assets directory if it exists tmpDir/compiled_assets to targetAppPath/compiled_assets
		// for _, fileName := range otherFilesToCopy {
		//	srcFilePath := filepath.Join(tmpDir, fileName)
		//	dstFilePath := filepath.Join(targetAppPath, fileName)
		//	if _, statErr := os.Stat(srcFilePath); statErr == nil { // File exists in package
		//		// Basic file copy logic here (os.Open, os.Create, io.Copy)
		//      // Ensure to handle errors for each file copy
		//		fmt.Printf("Copying %s to %s\n", fileName, dstFilePath)
		//	}
		// }
		// TODO: Copy other files like install_hooks.py, requirements.txt from tmpDir to targetAppPath if they exist.
		// For example:
		// otherFilesToCopy := []string{"install_hooks.py", "requirements.txt", "package.json"}
		// for _, fileName := range otherFilesToCopy {
		//	srcFilePath := filepath.Join(tmpDir, fileName)
		//	dstFilePath := filepath.Join(targetAppPath, fileName)
		//	if _, statErr := os.Stat(srcFilePath); statErr == nil { // File exists in package
		//		// Basic file copy logic here (os.Open, os.Create, io.Copy)
		//      // Ensure to handle errors for each file copy
		//		fmt.Printf("Copying %s to %s\n", fileName, dstFilePath)
		//	}
		// }
		// TODO: Copy compiled_assets directory if it exists tmpDir/compiled_assets to targetAppPath/compiled_assets


		// --- Symlinking App to Bench ---
		absBenchPath, err := filepath.Abs(benchPath)
		if err != nil {
			return fmt.Errorf("failed to get absolute path for bench directory '%s': %w", benchPath, err)
		}

		// The actual app code (containing __init__.py, etc.) is now directly in targetAppPath/pkgMeta.AppName
		// So, originalPath for symlink is correct.
		originalPath := filepath.Join(targetAppPath, pkgMeta.AppName) // This is correct: <apps_base>/<org>/<app>/<ver>/<app_module_name>
		linkName := filepath.Join(absBenchPath, "apps", pkgMeta.AppName)

		fmt.Printf("Preparing to symlink app '%s' from '%s' to '%s'\n", pkgMeta.AppName, originalPath, linkName)

		// Ensure parent directory for symlink exists (e.g., bench/apps/)
		linkDir := filepath.Dir(linkName)
		if err := os.MkdirAll(linkDir, os.ModePerm); err != nil {
			return fmt.Errorf("failed to create directory for symlink '%s': %w", linkDir, err)
		}

		// Check if linkName already exists and remove if it does
		// os.Lstat does not follow the link if it is one.
		if _, err := os.Lstat(linkName); err == nil {
			fmt.Printf("Removing existing file/symlink at '%s'\n", linkName)
			if err := os.Remove(linkName); err != nil {
				// If it's a non-empty directory, os.Remove will fail.
				// For robust handling, could use os.RemoveAll, but that's riskier if path is wrong.
				// Sticking to os.Remove for safety; if it's a populated dir, this should fail.
				return fmt.Errorf("failed to remove existing file/symlink at '%s': %w", linkName, err)
			}
		} else if !os.IsNotExist(err) {
			// Some other error occurred during Lstat (permissions, etc.)
			return fmt.Errorf("failed to check symlink path '%s': %w", linkName, err)
		}

		// Create the symlink
		if err := os.Symlink(originalPath, linkName); err != nil {
			return fmt.Errorf("failed to create symlink from '%s' to '%s': %w", originalPath, linkName, err)
		}
		fmt.Printf("Successfully symlinked app '%s' into bench.\n", pkgMeta.AppName)

		// --- Running Pip Install ---
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
				// Log this error but don't override a primary error from pip execution
				fmt.Fprintf(os.Stderr, "Warning: failed to change directory back to '%s': %v\n", currentWD, err)
			}
		}()

		pipAppPath := filepath.Join("./apps", pkgMeta.AppName) // Relative to bench path
		pipCmdArgs := []string{"install", "-q", "-e", pipAppPath}
		fmt.Printf("Running pip install for '%s': ./env/bin/pip %s\n", pkgMeta.AppName, strings.Join(pipCmdArgs, " "))

		pipExecCmd := exec.Command("./env/bin/pip", pipCmdArgs...)
		// No need to set pipExecCmd.Dir as we have already Chdir'd

		output, err := pipExecCmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("pip install for app '%s' failed:\n%s\nError: %w", pkgMeta.AppName, string(output), err)
		}
		fmt.Printf("Pip install for app '%s' successful.\nOutput:\n%s\n", pkgMeta.AppName, string(output))

		// --- Updating apps.txt ---
		appsTxtPath := filepath.Join(absBenchPath, "sites", "apps.txt")
		appNameString := pkgMeta.AppName // Use the one from metadata, which is validated
		logMessagePrefix := fmt.Sprintf("apps.txt (%s):", appsTxtPath)

		// Ensure the sites directory exists, as apps.txt is inside it.
		sitesDir := filepath.Dir(appsTxtPath)
		if err := os.MkdirAll(sitesDir, os.ModePerm); err != nil {
			return fmt.Errorf("%s Failed to create sites directory '%s': %w", logMessagePrefix, sitesDir, err)
		}

		fileContentBytes, err := os.ReadFile(appsTxtPath)
		var appsInFile []string
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Printf("%s File does not exist, will create it with app '%s'.\n", logMessagePrefix, appNameString)
				appsInFile = []string{} // Start with an empty list
			} else {
				return fmt.Errorf("%s Failed to read: %w", logMessagePrefix, err)
			}
		} else {
			fileContent := string(fileContentBytes)
			// Split and clean existing apps
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
			appsInFile = append(appsInFile, appNameString) // Add the new app

			// Join the possibly updated list back into a string for writing
			newContent := strings.Join(appsInFile, "\n")
			if len(appsInFile) > 0 { // Add a trailing newline if there's content
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

func init() {
	// Add flags to installCmd
	installCmd.Flags().String("bench-path", "", "Path to the Frappe bench directory")
	if err := installCmd.MarkFlagRequired("bench-path"); err != nil {
		// This error is typically a developer error (e.g., flag name typo)
		// Cobra itself will handle the error if the user doesn't provide the required flag.
		fmt.Fprintf(os.Stderr, "Error marking 'bench-path' flag required for install cmd: %v\n", err)
	}

	installCmd.Flags().String("site", "", "Name of the site to install the app to (optional)")

	// Add installCmd to rootCmd
	// This assumes rootCmd is a package-level var in the cmd package (defined in root.go)
	rootCmd.AddCommand(installCmd)
}
