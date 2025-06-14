package cmd

import (
	"encoding/json"
	"fmt"
	"io/fs" // For filepath.WalkDir
	"os"
	"path/filepath"
	"strings"

	"fpm/internal/config"     // For FPMConfig to find cache path relative to FPM base
	"fpm/internal/repository" // For PackageMetadata struct
	"github.com/spf13/cobra"
)

var searchCmd = &cobra.Command{
	Use:   "search [query]",
	Short: "Search for FPM packages in the local cache of repository metadata",
	Long: `Searches for FPM packages by matching the query against the groupID, artifactID,
or description of packages found in the locally cached repository metadata.
If no query is provided, it lists all packages found in the cache.`,
	Args: cobra.MaximumNArgs(1), // 0 or 1 argument
	RunE: func(cmd *cobra.Command, args []string) error {
		query := ""
		if len(args) > 0 {
			query = strings.ToLower(strings.TrimSpace(args[0]))
		}

		cfg, err := config.InitConfig() // Load config to potentially find FPM base dir
		if err != nil {
			// Non-fatal if we can still determine a default cache path
			fmt.Fprintf(os.Stderr, "Warning: could not load FPM config, using default cache path: %v\n", err)
			// cfg will be nil, handle below
		}

		homeDir, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to get user home directory: %w", err)
		}
		fpmBaseDir := filepath.Join(homeDir, ".fpm")
		// If cfg is not nil and AppsBasePath is set, we could derive fpmBaseDir if it's different
		// For now, ~/.fpm is the standard.
		if cfg != nil && cfg.AppsBasePath != "" {
			// Example: if AppsBasePath is /custom/path/.fpm/apps, fpmBaseDir could be /custom/path/.fpm
			// This logic assumes AppsBasePath is of the form .../.fpm/apps
			potentialFpmDir := filepath.Dir(cfg.AppsBasePath) // Gives .../.fpm
			if filepath.Base(potentialFpmDir) == ".fpm" {
				// Check if this path actually exists and looks like an FPM dir, otherwise stick to default.
				// For simplicity, if FPM_APPS_BASE_PATH is set, assume .fpm is its parent.
				// This might not always be true if user sets a very custom AppsBasePath.
				// Sticking to default ~/.fpm for cache unless FPM_HOME or similar is introduced.
			}
		}
		cacheBaseDir := filepath.Join(fpmBaseDir, "cache")

		if _, statErr := os.Stat(cacheBaseDir); os.IsNotExist(statErr) {
			fmt.Println("Cache directory does not exist. No packages to search.")
			fmt.Printf("Cache directory expected at: %s\n", cacheBaseDir)
			return nil
		} else if statErr != nil {
			return fmt.Errorf("could not access cache directory at %s: %w", cacheBaseDir, statErr)
		}


		var foundPackages []struct {
			RepoName    string
			GroupID     string
			ArtifactID  string
			Version     string // Latest version
			Description string
		}

		err = filepath.WalkDir(cacheBaseDir, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				// Log error and try to continue if possible, or return to stop.
				fmt.Fprintf(os.Stderr, "Error accessing path %q: %v\n", path, walkErr)
				if d == nil || !d.IsDir() { // If it's a file error or inaccessible dir, skip.
					return nil // Try to continue with other files/dirs.
				}
				// If it's a directory error, WalkDir might not be able to continue deeper.
				return walkErr // Propagate directory errors.
			}

			if !d.IsDir() && d.Name() == "package-metadata.json" {
				// path is .../.fpm/cache/<repo_name>/metadata/<groupID>/<artifactID>/package-metadata.json
				// Extract repoName, groupID, artifactID from path
				relPath, errRel := filepath.Rel(cacheBaseDir, path)
				if errRel != nil {
					fmt.Fprintf(os.Stderr, "Error getting relative path for %s: %v\n", path, errRel)
					return nil // Skip this file
				}

				parts := strings.Split(filepath.ToSlash(relPath), "/")
				// Expected parts: [repoName, "metadata", groupID, artifactID, "package-metadata.json"]
				if len(parts) != 5 || parts[1] != "metadata" {
					// fmt.Fprintf(os.Stderr, "Skipping metadata file in unexpected location: %s (rel: %s)\n", path, relPath)
					return nil // Skip files not matching the expected structure
				}
				repoNameFromPath := parts[0]
				// groupIDFromPath := parts[2] // GroupID from path
				// artifactIDFromPath := parts[3] // ArtifactID from path

				fileBytes, readErr := os.ReadFile(path)
				if readErr != nil {
					fmt.Fprintf(os.Stderr, "Error reading metadata file %s: %v\n", path, readErr)
					return nil // Skip this file
				}

				var pkgMeta repository.PackageMetadata
				if unmarshalErr := json.Unmarshal(fileBytes, &pkgMeta); unmarshalErr != nil {
					fmt.Fprintf(os.Stderr, "Error parsing metadata file %s: %v\n", path, unmarshalErr)
					return nil // Skip this file
				}

				// Use groupID and artifactID from metadata content for consistency, path is for discovery
				match := false
				if query == "" {
					match = true // List all if no query
				} else {
					if strings.Contains(strings.ToLower(pkgMeta.GroupID), query) {
						match = true
					} else if strings.Contains(strings.ToLower(pkgMeta.ArtifactID), query) {
						match = true
					} else if strings.Contains(strings.ToLower(pkgMeta.Description), query) {
						match = true
					}
				}

				if match {
					foundPackages = append(foundPackages, struct {
						RepoName    string
						GroupID     string
						ArtifactID  string
						Version     string
						Description string
					}{
						RepoName:    repoNameFromPath, // Use repo name from path structure
						GroupID:     pkgMeta.GroupID,
						ArtifactID:  pkgMeta.ArtifactID,
						Version:     pkgMeta.LatestVersion, // Show latest version
						Description: pkgMeta.Description,
					})
				}
			}
			return nil
		})

		if err != nil {
			return fmt.Errorf("error walking cache directory %s: %w", cacheBaseDir, err)
		}

		if len(foundPackages) == 0 {
			if query != "" {
				fmt.Printf("No packages found matching query '%s' in the local metadata cache.\n", query)
			} else {
				fmt.Println("No package metadata found in the local cache.")
			}
			return nil
		}

		fmt.Printf("%-20s %-40s %-15s %s\n", "REPOSITORY", "PACKAGE (GROUP/ARTIFACT)", "LATEST_VER", "DESCRIPTION")
		fmt.Printf("%-20s %-40s %-15s %s\n", strings.Repeat("-", 20), strings.Repeat("-", 40), strings.Repeat("-", 15), strings.Repeat("-", 11))
		for _, pkg := range foundPackages {
			packageName := fmt.Sprintf("%s/%s", pkg.GroupID, pkg.ArtifactID)
			// Truncate description if too long for cleaner table view
			desc := pkg.Description
			if len(desc) > 50 { // Max description length for display
				desc = desc[:47] + "..."
			}
			fmt.Printf("%-20s %-40s %-15s %s\n", pkg.RepoName, packageName, pkg.Version, desc)
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(searchCmd)
}
