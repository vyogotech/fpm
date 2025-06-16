package cmd

import (
	"errors" // For errors.Unwrap
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"archive/zip" // For extracting the FPM file
	"io"          // For io.Copy

	"fpm/internal/apputils"
	"fpm/internal/archive"
	"fpm/internal/config"
	"fpm/internal/gitutils"
	"fpm/internal/metadata"
	"fpm/internal/utils" // Added for utils.CopyRegularFile

	"github.com/spf13/cobra"
)

// validateFrappeAppStructure checks if the source directory has a valid Frappe app structure.
func validateFrappeAppStructure(sourceDir string, appName string) error {
	// Check 1: Existence of directory sourceDir + "/" + appName
	innerAppPath := filepath.Join(sourceDir, appName)
	info, err := os.Stat(innerAppPath)
	if os.IsNotExist(err) {
		return fmt.Errorf("Frappe app validation failed: app directory '%s' not found", innerAppPath)
	}
	if err != nil {
		return fmt.Errorf("Frappe app validation failed: error checking app directory '%s': %w", innerAppPath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("Frappe app validation failed: '%s' is not a directory", innerAppPath)
	}

	// Check 2: Existence of file sourceDir + "/" + appName + "/__init__.py"
	initPyPath := filepath.Join(innerAppPath, "__init__.py")
	info, err = os.Stat(initPyPath)
	if os.IsNotExist(err) {
		return fmt.Errorf("Frappe app validation failed: file '%s' not found", initPyPath)
	}
	if err != nil {
		return fmt.Errorf("Frappe app validation failed: error checking file '%s': %w", initPyPath, err)
	}
	if info.IsDir() {
		return fmt.Errorf("Frappe app validation failed: '%s' is a directory, not a file", initPyPath)
	}

	// Check 3: Existence of file sourceDir + "/" + appName + "/hooks.py"
	hooksPyPath := filepath.Join(innerAppPath, "hooks.py")
	info, err = os.Stat(hooksPyPath)
	if os.IsNotExist(err) {
		return fmt.Errorf("Frappe app validation failed: file '%s' not found", hooksPyPath)
	}
	if err != nil {
		return fmt.Errorf("Frappe app validation failed: error checking file '%s': %w", hooksPyPath, err)
	}
	if info.IsDir() {
		return fmt.Errorf("Frappe app validation failed: '%s' is a directory, not a file", hooksPyPath)
	}

	// Check 4: Existence of file sourceDir + "/" + appName + "/modules.txt"
	modulesTxtPath := filepath.Join(innerAppPath, "modules.txt")
	info, err = os.Stat(modulesTxtPath)
	if os.IsNotExist(err) {
		return fmt.Errorf("Frappe app validation failed: file '%s' not found", modulesTxtPath)
	}
	if err != nil {
		return fmt.Errorf("Frappe app validation failed: error checking file '%s': %w", modulesTxtPath, err)
	}
	if info.IsDir() {
		return fmt.Errorf("Frappe app validation failed: '%s' is a directory, not a file", modulesTxtPath)
	}

	return nil // All checks passed
}

var (
	// packageSourcePath string // This was commented out in original, keeping it that way.
	packageOutputPath string
	packageVersion    string
	packageOverwrite      bool
	packageType           string
	packageSkipLocalInstall bool
)

var packageCmd = &cobra.Command{
	Use:   "package",
	Short: "Package a Frappe application into an .fpm file",
	Long: `Packages a Frappe application from a local development directory into an .fpm file.
It reads app metadata, collects source files, and bundles them into a versioned archive.
By default, it also installs the packaged app to the local FPM app store.`,
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

		meta, err := metadata.LoadAppMetadata(absSourcePath)
		if err != nil {
			// Consider this non-fatal or handle more gracefully if needed
			fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not load existing app_metadata.json: %v. Will generate new.\n", err)
		}
		if meta == nil || (meta.PackageName == "" && meta.AppName == "") { // meta can be nil if LoadAppMetadata returns error
			generatedMeta, genErr := metadata.GenerateAppMetadata(absSourcePath, versionFlagValue)
			if genErr != nil {
				return fmt.Errorf("failed to generate default app metadata: %w", genErr)
			}
			meta = generatedMeta
		}
		meta.PackageVersion = versionFlagValue

		orgFromGit, repoNameFromGit, errGit := gitutils.GetGitRemoteOriginInfo(absSourcePath)
		if errGit != nil {
			unwrappedErr := errors.Unwrap(errGit)
			if unwrappedErr == nil { unwrappedErr = errGit }
			if !strings.Contains(unwrappedErr.Error(), "not found") && !strings.Contains(unwrappedErr.Error(), "no such file or directory") {
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not determine org/repo from git: %v\n", errGit)
			} else {
				fmt.Fprintf(cmd.ErrOrStderr(), "Info: no git remote 'origin' found or not a git repo: %s\n", absSourcePath)
			}
		}

		derivedAppName := ""
		appModuleDirGuess := meta.AppName
		if appModuleDirGuess == "" {
			appModuleDirGuess = meta.PackageName
		}
		if appModuleDirGuess != "" {
			hooksFilePath := filepath.Join(absSourcePath, appModuleDirGuess, "hooks.py")
			appNameFromHooks, errHooks := apputils.GetAppNameFromHooks(hooksFilePath)
			if errHooks == nil && appNameFromHooks != "" {
				derivedAppName = appNameFromHooks
				fmt.Fprintf(cmd.OutOrStdout(), "Info: Inferred app_name '%s' from hooks.py\n", derivedAppName)
			} else if errHooks != nil {
				unwrappedErr := errors.Unwrap(errHooks)
				if unwrappedErr == nil { unwrappedErr = errHooks }
				if !strings.Contains(unwrappedErr.Error(), "not found") && !strings.Contains(unwrappedErr.Error(), "no such file or directory"){
					fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not determine app_name from %s: %v\n", hooksFilePath, errHooks)
				} else {
					fmt.Fprintf(cmd.ErrOrStderr(), "Info: hooks.py not found at %s or app_name not in it.\n", hooksFilePath)
				}
			}
		}
		if derivedAppName == "" && repoNameFromGit != "" {
			derivedAppName = repoNameFromGit
			fmt.Fprintf(cmd.OutOrStdout(), "Info: Using repository name '%s' as app_name (derived from git remote)\n", derivedAppName)
		}

		orgFlagValue, _ := cmd.Flags().GetString("org")
		appNameFlagValue, _ := cmd.Flags().GetString("app-name")

		finalOrg := meta.Org
		if orgFromGit != "" {
			finalOrg = orgFromGit
		}
		if orgFlagValue != "" {
			finalOrg = orgFlagValue
		}

		finalAppName := meta.AppName
		if derivedAppName != "" {
			finalAppName = derivedAppName
		}
		if appNameFlagValue != "" {
			finalAppName = appNameFlagValue
		}

		if finalAppName == "" {
			hooksPathForError := filepath.Join(absSourcePath, "[app_module_name]", "hooks.py")
			if appModuleDirGuess != "" {
				hooksPathForError = filepath.Join(absSourcePath, appModuleDirGuess, "hooks.py")
			}
			return fmt.Errorf("app_name could not be determined. Please provide --app-name flag, or ensure it's in '%s', or derivable from git remote name.", hooksPathForError)
		}

		meta.Org = finalOrg
		meta.AppName = finalAppName
		meta.PackageName = finalAppName

		fullGitURL, errGitURL := gitutils.GetFullGitRemoteOriginURL(absSourcePath)
		if errGitURL != nil {
			unwrappedErr := errors.Unwrap(errGitURL)
			if unwrappedErr == nil { unwrappedErr = errGitURL }
			if !strings.Contains(unwrappedErr.Error(), "not found") && !strings.Contains(unwrappedErr.Error(), "no such file or directory") {
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not determine full git remote URL: %v\n", errGitURL)
			}
		}
		meta.SourceControlURL = fullGitURL
		meta.PackageType = packageType

		if err := validateFrappeAppStructure(absSourcePath, meta.AppName); err != nil {
			return err
		}

		outputFileName := fmt.Sprintf("%s-%s.fpm", meta.AppName, meta.PackageVersion)
		absOutputPath, err := filepath.Abs(packageOutputPath)
		if err != nil {
			return fmt.Errorf("failed to get absolute output path: %w", err)
		}
		finalFpmFilePath := filepath.Join(absOutputPath, outputFileName)

		if _, err := os.Stat(finalFpmFilePath); err == nil && !packageOverwrite {
			return fmt.Errorf("output file '%s' already exists. Use --overwrite to replace it", finalFpmFilePath)
		}

		fmt.Printf("Packaging '%s' version '%s' from '%s'...\n", meta.PackageName, meta.PackageVersion, absSourcePath)
		err = archive.CreateFPMArchive(absSourcePath, absOutputPath, meta, meta.PackageVersion)
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

			targetAppPathInStore := filepath.Join(cfg.AppsBasePath, meta.Org, meta.AppName, meta.PackageVersion)
			fmt.Printf("Cleaning up existing local installation directory (if any): %s\n", targetAppPathInStore)
			if err := os.RemoveAll(targetAppPathInStore); err != nil {
				return fmt.Errorf("failed to remove existing directory in local store %s: %w", targetAppPathInStore, err)
			}
			if err := os.MkdirAll(targetAppPathInStore, 0o755); err != nil {
				return fmt.Errorf("failed to create directory in local store %s: %w", targetAppPathInStore, err)
			}

			fmt.Printf("Extracting package %s to local store %s...\n", finalFpmFilePath, targetAppPathInStore)
			r, zipErr := zip.OpenReader(finalFpmFilePath)
			if zipErr != nil {
				return fmt.Errorf("failed to open created FPM package for local install %s: %w", finalFpmFilePath, zipErr)
			}
			defer r.Close()

			for _, f := range r.File {
				extractedFilePath := filepath.Join(targetAppPathInStore, f.Name)
				if !strings.HasPrefix(extractedFilePath, filepath.Clean(targetAppPathInStore)+string(os.PathSeparator)) {
					return fmt.Errorf("illegal file path in FPM archive: '%s' (targets outside '%s')", f.Name, targetAppPathInStore)
				}
				if f.FileInfo().IsDir() {
					if err := os.MkdirAll(extractedFilePath, 0o755); err != nil { // Standardized permission
						return fmt.Errorf("failed to create directory structure %s during local install: %w", extractedFilePath, err)
					}
					continue
				}
				if err := os.MkdirAll(filepath.Dir(extractedFilePath), os.ModePerm); err != nil {
					return fmt.Errorf("failed to create parent directory for %s during local install: %w", extractedFilePath, err)
				}
				outFile, err := os.OpenFile(extractedFilePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644) // Standardized file permission
				if err != nil {
					return fmt.Errorf("failed to open file for writing %s during local install: %w", extractedFilePath, err)
				}
				rc, err := f.Open()
				if err != nil {
					outFile.Close()
					return fmt.Errorf("failed to open file in zip %s during local install: %w", f.Name, err)
				}
				_, err = io.Copy(outFile, rc)
				closeErrRC := rc.Close()
				closeErrOutFile := outFile.Close()
				if err != nil {
					return fmt.Errorf("failed to copy content of %s to %s during local install: %w", f.Name, extractedFilePath, err)
				}
				if closeErrRC != nil {
					return fmt.Errorf("failed to close zip entry %s during local install: %w", f.Name, closeErrRC)
				}
				if closeErrOutFile != nil {
					return fmt.Errorf("failed to close output file %s during local install: %w", extractedFilePath, closeErrOutFile)
				}
			}
			fmt.Printf("Successfully installed (extracted) package %s/%s version %s to local FPM store: %s\n", meta.Org, meta.AppName, meta.PackageVersion, targetAppPathInStore)

			originalFpmFilename := filepath.Base(finalFpmFilePath)
			storedFpmPath := filepath.Join(targetAppPathInStore, "_"+originalFpmFilename)
			if err := utils.CopyRegularFile(finalFpmFilePath, storedFpmPath, 0o644); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: failed to store original .fpm package in local store at %s: %v\n", storedFpmPath, err)
			} else {
				fmt.Printf("Stored original .fpm package in local store: %s\n", storedFpmPath)
			}
		} else {
			fmt.Println("Skipping installation to local FPM app store.")
		}
		return nil
	},
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
}
