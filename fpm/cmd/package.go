package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"fpm/internal/archive"
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
	RunE: func(cmd *cobra.Command, args []string) error { // Using RunE for error handling
		// Retrieve flag values
		orgFlagValue, err := cmd.Flags().GetString("org")
		if err != nil {
			return fmt.Errorf("failed to get 'org' flag value: %w", err)
		}
		appNameFlagValue, err := cmd.Flags().GetString("app-name")
		if err != nil {
			return fmt.Errorf("failed to get 'app-name' flag value: %w", err)
		}
		// Version is already a global variable packageVersion, direct check
		if packageVersion == "" {
			return fmt.Errorf("--version flag is required")
		}
		// appNameFlagValue is marked required by Cobra, so it should exist if command parsing succeeded.


		absSourcePath, err := filepath.Abs(packageSourcePath)
		if err != nil {
			return fmt.Errorf("failed to get absolute source path: %w", err)
		}

		if _, err := os.Stat(absSourcePath); os.IsNotExist(err) {
			return fmt.Errorf("source path '%s' does not exist", absSourcePath)
		}

		// Load existing metadata or generate a new one
		meta, err := metadata.LoadAppMetadata(absSourcePath)
		if err != nil {
			// If LoadAppMetadata returns an error for reasons other than file not found,
			// or if we decide it should error if file not found, handle here.
			// For now, LoadAppMetadata returns empty struct if not found.
			// Let's assume if meta.PackageName is empty after Load, we should generate.
		}

		// If package name is still empty (either file didn't exist or was empty), generate.
		if meta.PackageName == "" {
		    inferredMeta, genErr := metadata.GenerateAppMetadata(absSourcePath, packageVersion)
		    if genErr != nil {
		        return fmt.Errorf("failed to generate default app metadata: %w", genErr)
		    }
		    meta = inferredMeta // Use the generated one
		} else {
            // If loaded, still ensure the CLI version overrides
		    meta.PackageVersion = packageVersion
        }
        // If GenerateAppMetadata was called, it already set the version.
        // If LoadAppMetadata was called and it was successful, PackageVersion in meta
        // will be updated by the GenerateAppMetadata or the line above.

		// --- Populate metadata with flag values (overriding loaded/generated values if any) ---
		// The --app-name flag is now the authoritative source for AppName and PackageName
		meta.AppName = appNameFlagValue
		meta.PackageName = appNameFlagValue // Set PackageName from app-name flag
		meta.Org = orgFlagValue
		// Ensure version from flag is used (already done if generated, this ensures for loaded meta)
		meta.PackageVersion = packageVersion


		// Validate Frappe app structure using the definitive appName from the flag
		if err := validateFrappeAppStructure(absSourcePath, meta.AppName); err != nil {
			// Pass meta.AppName (which is appNameFlagValue) to validation
			return err // The error from validateFrappeAppStructure is already descriptive
		}

		// Output filename should also use the definitive appName from the flag
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
	packageCmd.Flags().StringVarP(&packageSourcePath, "source", "s", ".", "Path to the Frappe app source directory")
	packageCmd.Flags().StringVarP(&packageOutputPath, "output-path", "o", ".", "Directory to save the .fpm file")
	packageCmd.Flags().StringVarP(&packageVersion, "version", "v", "", "Package version (e.g., 1.0.0) (required)")
	packageCmd.Flags().BoolVar(&packageOverwrite, "overwrite", false, "Overwrite if .fpm file already exists")

	// New flags
	packageCmd.Flags().String("org", "", "GitHub organization or similar identifier for the app")
	packageCmd.Flags().String("app-name", "", "Actual Frappe app name (e.g., erpnext, my_custom_app)")

	// Mark app-name as required
	if err := packageCmd.MarkFlagRequired("app-name"); err != nil {
		// This error typically occurs during setup if the flag doesn't exist,
		// which shouldn't happen here as we just defined it.
		// Cobra itself will handle the error if the user doesn't provide the flag.
		// For robustness, one might log this, but it's more of a developer error.
		fmt.Fprintf(os.Stderr, "Critical setup error: failed to mark 'app-name' flag as required: %v\n", err)
		// Depending on desired behavior, could os.Exit(1) if this is considered fatal for CLI setup
	}
	// Manual check for version is already in RunE, but could also use MarkFlagRequired for consistency.
	// if err := packageCmd.MarkFlagRequired("version"); err != nil {
	// 	fmt.Fprintf(os.Stderr, "Critical setup error: failed to mark 'version' flag as required: %v\n", err)
	// }
}
