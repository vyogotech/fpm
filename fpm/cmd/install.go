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
	"os/exec" // Added for running pip command

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
	Use:   "install <package_path>",
	Short: "Install a Frappe app from a local .fpm package",
	Long:  `Installs a Frappe application from a specified local .fpm package file into a Frappe bench.`,
	Args:  cobra.ExactArgs(1), // Ensures exactly one argument (the package path) is provided.
	RunE: func(cmd *cobra.Command, args []string) error {
		packagePath := args[0]

		benchPath, err := cmd.Flags().GetString("bench-path")
		if err != nil {
			return fmt.Errorf("error retrieving 'bench-path' flag: %w", err)
		}

		siteName, err := cmd.Flags().GetString("site") // Keep this for future use
		if err != nil {
			return fmt.Errorf("error retrieving 'site' flag: %w", err)
		}

		fmt.Printf("Starting installation of package: %s\n", packagePath)
		fmt.Printf("Target Bench Path: %s\n", benchPath)
		if siteName != "" {
			fmt.Printf("Target Site (for future use): %s\n", siteName)
		}

		// 1. Validate Package Path Argument
		fileInfo, err := os.Stat(packagePath)
		if os.IsNotExist(err) {
			return fmt.Errorf("package path '%s' does not exist", packagePath)
		}
		if err != nil {
			return fmt.Errorf("error checking package path '%s': %w", packagePath, err)
		}
		if fileInfo.IsDir() {
			return fmt.Errorf("package path '%s' is a directory, not a file", packagePath)
		}
		fmt.Printf("Package file '%s' found.\n", packagePath)

		// 2. Unzip .fpm Package to a Temporary Directory
		tmpDir, err := os.MkdirTemp("", "fpm-pkg-extract-*")
		if err != nil {
			return fmt.Errorf("failed to create temporary directory: %w", err)
		}
		defer os.RemoveAll(tmpDir) // Ensure cleanup
		fmt.Printf("Extracting package to temporary directory: %s\n", tmpDir)

		r, err := zip.OpenReader(packagePath)
		if err != nil {
			return fmt.Errorf("failed to open package %s: %w", packagePath, err)
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

		// 5. Basic Package Structure Validation
		appSourceDir := filepath.Join(tmpDir, "app_source")
		info, err := os.Stat(appSourceDir)
		if os.IsNotExist(err) {
			return fmt.Errorf("package structure validation failed: 'app_source' directory not found in package")
		}
		if err != nil {
			return fmt.Errorf("error checking 'app_source' directory: %w", err)
		}
		if !info.IsDir() {
			return fmt.Errorf("package structure validation failed: 'app_source' is not a directory")
		}
		fmt.Println("'app_source' directory found in package.")

		// Further validation for app_source/pkgMeta.AppName
		innerAppSourceDir := filepath.Join(appSourceDir, pkgMeta.AppName)
		info, err = os.Stat(innerAppSourceDir)
		if os.IsNotExist(err) {
		    return fmt.Errorf("package structure validation failed: app directory '%s' not found in 'app_source/'", pkgMeta.AppName)
		}
		if err != nil {
		    return fmt.Errorf("error checking app directory '%s' in 'app_source/': %w", pkgMeta.AppName, err)
		}
		if !info.IsDir() {
		    return fmt.Errorf("package structure validation failed: '%s' in 'app_source/' is not a directory", pkgMeta.AppName)
		}
		fmt.Printf("App directory '%s' found in 'app_source/'.\n", pkgMeta.AppName)


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

		// 3. Extract Package Contents (app_source) to Target Path
		sourceDirToCopy := filepath.Join(tmpDir, "app_source")
		fmt.Printf("Copying contents from %s to %s...\n", sourceDirToCopy, targetAppPath)

		if err := copyDirContents(sourceDirToCopy, targetAppPath); err != nil {
			return fmt.Errorf("failed to copy app contents from %s to %s: %w", sourceDirToCopy, targetAppPath, err)
		}
		fmt.Printf("App contents copied successfully to %s.\n", targetAppPath)

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

		// The actual app code (containing __init__.py, etc.) is inside targetAppPath/pkgMeta.AppName
		originalPath := filepath.Join(targetAppPath, pkgMeta.AppName)
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
