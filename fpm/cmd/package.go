package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"fpm/internal/archive"
	"fpm/internal/metadata"

	"github.com/spf13/cobra"
)

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
		if packageVersion == "" {
			return fmt.Errorf("--version flag is required")
		}

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


		outputFileName := fmt.Sprintf("%s-%s.fpm", meta.PackageName, packageVersion)
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

	// Mark version as required if using cobra's built-in way, though manual check is also fine.
	// packageCmd.MarkFlagRequired("version") // This causes help text to show if not provided.
}
