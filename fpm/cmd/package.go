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

		// Initial version from flag (packageVersion is the global var bound to the flag)
		// This will be refined after appName is determined and hooks.py can be read.
		// versionFlagValue := packageVersion // Keep this to know if flag was explicitly set

		meta, err := metadata.LoadAppMetadata(absSourcePath) // Try to load existing metadata first
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not load existing app_metadata.json: %v. Will generate new.\n", err)
		}
		if meta == nil { // If loading failed or file doesn't exist
			// We need a temporary version for GenerateAppMetadata if version flag is not set yet.
			// However, GenerateAppMetadata's version arg is for initializing meta.PackageVersion,
			// which we will overwrite with resolvedVersion later. So, passing "" or a placeholder is fine.
			tempVersionForMetaGen := packageVersion
			if tempVersionForMetaGen == "" { tempVersionForMetaGen = "0.0.0-placeholder" }
			generatedMeta, genErr := metadata.GenerateAppMetadata(absSourcePath, tempVersionForMetaGen)
			if genErr != nil {
				return fmt.Errorf("failed to generate default app metadata: %w", genErr)
			}
			meta = generatedMeta
		}
		// meta.PackageVersion will be definitively set after full version resolution.

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
		meta.PackageName = finalAppName // PackageName is often same as AppName

		// Version resolution logic
		hooksFilePathForVersion := filepath.Join(absSourcePath, finalAppName, "hooks.py")
		appVersionFromHooks, errHooksVersion := apputils.GetAppVersionFromHooks(hooksFilePathForVersion)
		if errHooksVersion != nil {
			// Log a warning if hooks.py couldn't be read for version, but don't fail packaging yet.
			// It might not exist, or app_version might not be there.
			fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not determine version from %s: %v\n", hooksFilePathForVersion, errHooksVersion)
		}

		resolvedVersion := packageVersion // Value from --version flag

		if resolvedVersion == "" { // --version flag was not set
			if errHooksVersion == nil && appVersionFromHooks != "" {
				resolvedVersion = appVersionFromHooks
				fmt.Fprintf(cmd.OutOrStdout(), "Info: Using version '%s' from %s\n", resolvedVersion, hooksFilePathForVersion)
			} else {
				// If flag not set AND version not found/error in hooks.py
				return fmt.Errorf("package version must be provided via --version flag or defined as 'app_version' (or '__version__') in %s", hooksFilePathForVersion)
			}
		} else { // --version flag was set
			if appVersionFromHooks != "" && resolvedVersion != appVersionFromHooks {
				fmt.Fprintf(cmd.OutOrStdout(), "Info: Overriding version '%s' from %s with flag value '%s'\n", appVersionFromHooks, hooksFilePathForVersion, resolvedVersion)
			}
		}

		if resolvedVersion == "" { // Should be caught by above logic, but as a safeguard
		    return fmt.Errorf("failed to resolve package version. Provide --version or define in hooks.py")
		}

		meta.PackageVersion = resolvedVersion // Set the resolved version in metadata

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

		// Load FPM config to get default exclusions if needed
		cfgForExclusions, errCfg := config.InitConfig()
		if errCfg != nil {
			// Non-fatal, as dev builds might not need it. Prod builds will fail if cfg is nil and needed.
			fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not load FPM config for default exclusions: %v\n", errCfg)
		}

		var exclusionsForArchive []string
		if meta.PackageType == "prod" {
			if cfgForExclusions != nil {
				exclusionsForArchive = cfgForExclusions.DefaultPackageExclusions
				fmt.Fprintf(cmd.OutOrStdout(), "Info: Using default package exclusions for 'prod' build type.\n")
			} else {
				// This case implies InitConfig failed, but we might want to proceed with no default exclusions
				// or make it a hard error for prod builds. For now, log and proceed without them.
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: 'prod' package type specified, but FPM config could not be loaded. Proceeding without default exclusions.\n")
				exclusionsForArchive = nil
			}
		} else { // "dev" or other types
			exclusionsForArchive = nil // Only .fpmignore and minimal hardcoded patterns apply
			fmt.Fprintf(cmd.OutOrStdout(), "Info: Using 'dev' build type. Only .fpmignore and minimal hardcoded exclusions apply.\n")
		}

		fmt.Printf("Packaging '%s' version '%s' from '%s' (type: %s)...\n", meta.PackageName, meta.PackageVersion, absSourcePath, meta.PackageType)
		err = archive.CreateFPMArchive(absSourcePath, absOutputPath, meta, meta.PackageVersion, exclusionsForArchive)
		if err != nil {
			return fmt.Errorf("failed to create package: %w", err)
		}
		fmt.Printf("Successfully packaged: %s\n", finalFpmFilePath)

		if !packageSkipLocalInstall {
			// For local install, we use a separate config load if needed, or could reuse cfgForExclusions
			// The appstore.ManageAppInLocalStore function will handle its own config loading if needed,
			// or take a config object if we refactor it to do so.
			// For now, the existing logic inside the ManageAppInLocalStore which calls InitConfig is fine.
			fmt.Println("Attempting to install package to local FPM app store...")
			// The config for local install path is handled by ManageAppInLocalStore.
			// No need to pass cfg to ManageAppInLocalStore explicitly here, as it calls InitConfig.

			// We need to ensure meta is complete before ManageAppInLocalStore is implicitly called by package.go's logic for local installation
			// (which has been refactored into appstore.ManageAppInLocalStore already)
			// The current code for local install (below this block) uses a fresh cfg load.
			// The refactor to appstore.ManageAppInLocalStore should be in place in the code I'm modifying.
			// The existing code block for local install:
			cfgForLocalInstall, errLocalInstallCfg := config.InitConfig() // This is the existing pattern.
			if errLocalInstallCfg != nil {
				return fmt.Errorf("failed to initialize FPM configuration for local install: %w", errLocalInstallCfg)
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
			// The actual installation to local store is now handled by appstore.ManageAppInLocalStore
			// This part of the code in package.go should be updated if it's still directly managing the app store.
			// Based on previous refactoring, this section should have been replaced by a call to appstore.ManageAppInLocalStore.
			// Assuming that refactor is done, this explicit local install block might be redundant or needs adjustment.
			// Let's re-verify the state of local installation logic.
			// The current code from read_files shows a full manual extraction here, NOT a call to appstore.ManageAppInLocalStore.
			// This means a previous refactoring of `package.go` to use `appstore.ManageAppInLocalStore` was NOT applied or was reverted.
			// I will proceed with the original plan for *this* subtask, which is to update CreateFPMArchive call.
			// The local install block below will be addressed if it's part of a future subtask to use appstore.ManageAppInLocalStore here.
			// For now, I will ensure `cfgForLocalInstall` is used for `targetAppPathInStore` below.

			targetAppPathInStore := filepath.Join(cfgForLocalInstall.AppsBasePath, meta.Org, meta.AppName, meta.PackageVersion)
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
				rcData, err := f.Open() // Renamed variable to avoid conflict
				if err != nil {
					outFile.Close()
					return fmt.Errorf("failed to open file in zip %s during local install: %w", f.Name, err)
				}
				_, err = io.Copy(outFile, rcData)
				closeErrRC := rcData.Close()
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
