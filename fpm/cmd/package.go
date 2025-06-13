package cmd

import (
	"errors" // For errors.Unwrap
	"fmt"
	"os"
	"path/filepath"
	"strings" // Added missing import

	"fpm/internal/apputils"
	"fpm/internal/archive"
	"fpm/internal/gitutils"
	"fpm/internal/metadata"

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
	packageSourcePath string
	packageOutputPath string
	packageVersion    string
	packageOverwrite  bool
)

var packageCmd = &cobra.Command{
	Use:   "package",
	Short: "Package a Frappe application into an .fpm file",
	Long: `Packages a Frappe application from a local development directory into an .fpm file.
It reads app metadata, collects source files, and bundles them into a versioned archive.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Source path is now an optional argument, defaults to "."
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

		// Version flag is still required
		versionFlagValue := packageVersion // packageVersion is the global var bound to the flag
		if versionFlagValue == "" {
			return fmt.Errorf("--version flag is required")
		}

		// 1. Initial metadata load/generation
		// This provides an initial meta object, potentially with app_name, org from an existing app_metadata.json
		// or with PackageName inferred from absSourcePath if generating.
		meta, err := metadata.LoadAppMetadata(absSourcePath)
		if err != nil {
			// Attempt to generate if loading failed or if specific "not found" type error (LoadAppMetadata current returns empty struct if not found)
			// For now, let's assume LoadAppMetadata returns an empty struct and no error if not found.
			// So, we check if meta.PackageName is empty (or AppName, if LoadAppMetadata sets it)
		}
		if meta.PackageName == "" && meta.AppName == "" { // If no useful name info loaded
			generatedMeta, genErr := metadata.GenerateAppMetadata(absSourcePath, versionFlagValue)
			if genErr != nil {
				return fmt.Errorf("failed to generate default app metadata: %w", genErr)
			}
			meta = generatedMeta // Use the generated one
		}
		meta.PackageVersion = versionFlagValue // Version flag is authoritative

		// 2. Derive Org from Git
		orgFromGit, repoNameFromGit, errGit := gitutils.GetGitRemoteOriginInfo(absSourcePath)
		if errGit != nil {
			// Check if the error is more serious than just .git/config not found or origin missing
			// os.IsNotExist is tricky with wrapped errors. A custom error type in gitutils might be better.
			// For now, log non-critical "not found" types of errors.
			unwrappedErr := errors.Unwrap(errGit)
			if unwrappedErr == nil { unwrappedErr = errGit } // Use original error if not wrapped by fmt.Errorf %w

			if !strings.Contains(unwrappedErr.Error(), "not found") && !strings.Contains(unwrappedErr.Error(), "no such file or directory") {
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not determine org/repo from git: %v\n", errGit)
			} else {
				fmt.Fprintf(cmd.ErrOrStderr(), "Info: no git remote 'origin' found or not a git repo: %s\n", absSourcePath)
			}
		}

		// 3. Derive AppName from hooks.py or Git repository name
		derivedAppName := ""
		// Use initial guess for app_name dir from metadata to find hooks.py.
		// meta.PackageName is often the directory name. If AppName was loaded, use that.
		appModuleDirGuess := meta.AppName
		if appModuleDirGuess == "" {
			appModuleDirGuess = meta.PackageName // Fallback to PackageName if AppName isn't set yet
		}

		if appModuleDirGuess != "" { // Only try if we have a directory to look into
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


		// 4. Determine Final Org and AppName (Flags override derived values, which override loaded values)
		orgFlagValue, _ := cmd.Flags().GetString("org")
		appNameFlagValue, _ := cmd.Flags().GetString("app-name")

		finalOrg := meta.Org // Start with value from app_metadata.json (if any)
		if orgFromGit != "" { // Derived from Git (if no flag)
			finalOrg = orgFromGit
		}
		if orgFlagValue != "" { // Flag has highest precedence
			finalOrg = orgFlagValue
		}

		finalAppName := meta.AppName // Start with value from app_metadata.json (if any)
		if derivedAppName != "" { // Derived (hooks or git)
			finalAppName = derivedAppName
		}
		if appNameFlagValue != "" { // Flag has highest precedence
			finalAppName = appNameFlagValue
		}

		// 5. Validate final AppName (must exist)
		if finalAppName == "" {
			hooksPathForError := filepath.Join(absSourcePath, "[app_module_name]", "hooks.py")
			if appModuleDirGuess != "" { // Use the guess if available for a more specific error message
				hooksPathForError = filepath.Join(absSourcePath, appModuleDirGuess, "hooks.py")
			}
			return fmt.Errorf("app_name could not be determined. Please provide --app-name flag, or ensure it's in '%s', or derivable from git remote name.", hooksPathForError)
		}

		// 6. Update metadata object with the final determined values
		meta.Org = finalOrg
		meta.AppName = finalAppName
		meta.PackageName = finalAppName // AppName is the primary identifier now
		// meta.PackageVersion is already set from the flag

		// Validate Frappe app structure using the final determined AppName
		if err := validateFrappeAppStructure(absSourcePath, meta.AppName); err != nil {
			return err
		}

		// Output filename uses final AppName and Version
		outputFileName := fmt.Sprintf("%s-%s.fpm", meta.AppName, meta.PackageVersion)
		absOutputPath, err := filepath.Abs(packageOutputPath)
		if err != nil {
			return fmt.Errorf("failed to get absolute output path: %w", err)
		}

		finalFpmFilePath := filepath.Join(absOutputPath, outputFileName)

		if _, err := os.Stat(finalFpmFilePath); err == nil && !packageOverwrite {
			return fmt.Errorf("output file '%s' already exists. Use --overwrite to replace it", finalFpmFilePath)
		}

		fmt.Printf("Packaging '%s' version '%s' from '%s'...\n", meta.PackageName, packageVersion, absSourcePath)

		err = archive.CreateFPMArchive(absSourcePath, absOutputPath, meta, packageVersion)
		if err != nil {
			return fmt.Errorf("failed to create package: %w", err)
		}

		fmt.Printf("Successfully packaged: %s\n", finalFpmFilePath)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(packageCmd)
	// packageSourcePath is now an optional argument, not a flag.
	// packageCmd.Flags().StringVarP(&packageSourcePath, "source", "s", ".", "Path to the Frappe app source directory")
	packageCmd.Flags().StringVarP(&packageOutputPath, "output-path", "o", ".", "Directory to save the .fpm file")
	packageCmd.Flags().StringVarP(&packageVersion, "version", "v", "", "Package version (e.g., 1.0.0) (required)") // Still global for now
	packageCmd.Flags().BoolVar(&packageOverwrite, "overwrite", false, "Overwrite if .fpm file already exists")

	// Optional flags for overriding derived values
	packageCmd.Flags().String("org", "", "GitHub organization or similar identifier for the app (overrides auto-detection)")
	packageCmd.Flags().String("app-name", "", "Actual Frappe app name (e.g., erpnext, my_custom_app) (overrides auto-detection)")

	// No longer marking app-name as required, as it can be derived.
	// Version is still marked as required implicitly by the check in RunE.
	// Consider using packageCmd.MarkFlagRequired("version") for consistency in help text.
}
