package cmd

import (
	"encoding/json"
	"fmt"
	"io/fs" // For filepath.WalkDir
	"net/http" // For targeted remote query
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time" // For http client timeout

	"fpm/internal/config"
	"fpm/internal/metadata"
	"fpm/internal/repository"
	"github.com/spf13/cobra"
)

// SearchResultItem holds information about a found package for display.
type SearchResultItem struct {
	Source      string // e.g., "(local-store)", "(cache: <repo_name>)", "(remote: <repo_name>)"
	GroupID     string
	ArtifactID  string
	Version     string // Specific version found
	Description string
	SourceRank  int    // 1 for local-store, 2 for remote-live, 3 for cache
}

var searchCmd = &cobra.Command{
	Use:   "search [query]",
	Short: "Search for FPM packages in local store, cache, and optionally remote repositories",
	Long: `Searches for FPM packages by matching the query against the groupID, artifactID,
or description. It searches packages installed in the local FPM app store (~/.fpm/apps),
metadata cached from remote repositories (~/.fpm/cache), and if the query is a specific
package identifier (e.g., <group>/<artifact>), it will also query remote repositories live.
If no query is provided, it lists all packages found in the local store and cache.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		query := ""
		if len(args) > 0 {
			query = strings.ToLower(strings.TrimSpace(args[0]))
		}

		cfg, err := config.InitConfig()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not load FPM config, using default paths for search: %v\n", err)
		}

		homeDir, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to get user home directory: %w", err)
		}

		fpmBaseDir := filepath.Join(homeDir, ".fpm")
		if cfg != nil && cfg.AppsBasePath != "" {
			fpmBaseDir = filepath.Dir(cfg.AppsBasePath)
		}

		localAppStoreDir := filepath.Join(fpmBaseDir, "apps")
		cacheBaseDir := filepath.Join(fpmBaseDir, "cache")

		// Key: <groupID>/<artifactID>:<version> for de-duplication
		deDupMap := make(map[string]SearchResultItem)

		// 1. Search Local FPM App Store (~/.fpm/apps) - SourceRank = 1
		if _, statErr := os.Stat(localAppStoreDir); statErr == nil {
			fmt.Printf("Searching in local FPM app store: %s\n", localAppStoreDir)
			filepath.WalkDir(localAppStoreDir, func(path string, d fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					fmt.Fprintf(os.Stderr, "Error accessing path %q during local store search: %v\n", path, walkErr)
					return nil
				}
				if !d.IsDir() && strings.HasPrefix(d.Name(), "_") && strings.HasSuffix(d.Name(), ".fpm") {
					versionDir := filepath.Dir(path)
					appDir := filepath.Dir(versionDir)
					orgDir := filepath.Dir(appDir)
					if filepath.Base(orgDir) == "apps" || orgDir == localAppStoreDir || appDir == localAppStoreDir { return nil }

					version := filepath.Base(versionDir)
					appNameFromFilePath := filepath.Base(appDir)
					orgNameFromFilePath := filepath.Base(orgDir)

					appMeta, metaErr := metadata.ReadMetadataFromFPMArchive(path)
					if metaErr != nil {
						fmt.Fprintf(os.Stderr, "Error reading metadata from local FPM store file %s: %v\n", path, metaErr)
						return nil
					}
					if appMeta.Org != orgNameFromFilePath || appMeta.AppName != appNameFromFilePath || appMeta.PackageVersion != version {
						 fmt.Fprintf(os.Stderr, "Warning: Metadata mismatch for FPM file %s. Path: %s/%s/%s, Meta: %s/%s/%s. Using metadata values.\n",
							 path, orgNameFromFilePath, appNameFromFilePath, version, appMeta.Org, appMeta.AppName, appMeta.PackageVersion)
					}

					match := false
					if query == "" { match = true
					} else {
						if strings.Contains(strings.ToLower(appMeta.Org), query) { match = true }
						if !match && strings.Contains(strings.ToLower(appMeta.AppName), query) { match = true }
						if !match && strings.Contains(strings.ToLower(appMeta.Description), query) { match = true }
					}

					if match {
						key := fmt.Sprintf("%s/%s:%s", appMeta.Org, appMeta.AppName, appMeta.PackageVersion)
						deDupMap[key] = SearchResultItem{
							Source: "(local-store)", GroupID: appMeta.Org, ArtifactID: appMeta.AppName,
							Version: appMeta.PackageVersion, Description: appMeta.Description, SourceRank: 1,
						}
					}
				}
				return nil
			})
		} else if !os.IsNotExist(statErr){
            fmt.Fprintf(os.Stderr, "Warning: Could not access local app store at %s: %v\n", localAppStoreDir, statErr)
        }

		// 2. Search Repository Metadata Cache (~/.fpm/cache) - SourceRank = 3
		if _, statErr := os.Stat(cacheBaseDir); statErr == nil {
			fmt.Printf("Searching in repository metadata cache: %s\n", cacheBaseDir)
			filepath.WalkDir(cacheBaseDir, func(path string, d fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					fmt.Fprintf(os.Stderr, "Error accessing path %q during cache search: %v\n", path, walkErr)
					return nil
				}
				if !d.IsDir() && d.Name() == "package-metadata.json" {
					relPath, _ := filepath.Rel(cacheBaseDir, path)
					parts := strings.Split(filepath.ToSlash(relPath), "/")
					if len(parts) != 5 || parts[1] != "metadata" { return nil }
					repoNameFromPath := parts[0]

					fileBytes, readErr := os.ReadFile(path)
					if readErr != nil { fmt.Fprintf(os.Stderr, "Error reading metadata file %s: %v\n", path, readErr); return nil }
					var pkgMeta repository.PackageMetadata
					if unmarshalErr := json.Unmarshal(fileBytes, &pkgMeta); unmarshalErr != nil {
						fmt.Fprintf(os.Stderr, "Error parsing metadata file %s: %v\n", path, unmarshalErr); return nil
					}

					pkgMatch := false
					if query == "" { pkgMatch = true
					} else {
						if strings.Contains(strings.ToLower(pkgMeta.GroupID), query) { pkgMatch = true }
						if !pkgMatch && strings.Contains(strings.ToLower(pkgMeta.ArtifactID), query) { pkgMatch = true }
						if !pkgMatch && strings.Contains(strings.ToLower(pkgMeta.Description), query) { pkgMatch = true }
					}

					if pkgMatch {
						for ver, verMeta := range pkgMeta.Versions {
							newItem := SearchResultItem{
								Source: fmt.Sprintf("(cache: %s)", repoNameFromPath), GroupID: pkgMeta.GroupID, ArtifactID: pkgMeta.ArtifactID,
								Version: ver, Description: pkgMeta.Description, // Potentially verMeta.Notes for specific version desc
								SourceRank: 3,
							}
							key := fmt.Sprintf("%s/%s:%s", newItem.GroupID, newItem.ArtifactID, newItem.Version)
							if existingItem, ok := deDupMap[key]; !ok || newItem.SourceRank < existingItem.SourceRank {
								deDupMap[key] = newItem
							}
						}
					}
				}
				return nil
			})
		} else if !os.IsNotExist(statErr){
            fmt.Fprintf(os.Stderr, "Warning: Could not access cache directory at %s: %v\n", cacheBaseDir, statErr)
        }

		// 3. Targeted Remote Query if query is <group>/<artifact> - SourceRank = 2
		var queryGroupID, queryArtifactID string
		isSpecificIdentifier := false
		if query != "" && strings.Count(query, "/") == 1 && !strings.Contains(query, "==") && !strings.Contains(query, "*") {
			parts := strings.Split(query, "/")
			if len(parts) == 2 {
				parsedGroup := strings.TrimSpace(parts[0])
				parsedArtifact := strings.TrimSpace(parts[1])
				if parsedGroup != "" && parsedArtifact != "" {
					queryGroupID = parsedGroup
					queryArtifactID = parsedArtifact
					isSpecificIdentifier = true
					fmt.Printf("\nPerforming targeted remote query for %s/%s...\n", queryGroupID, queryArtifactID)
				}
			}
		}

		if isSpecificIdentifier && cfg != nil { // cfg might be nil if InitConfig failed earlier
			httpClient := &http.Client{Timeout: 15 * time.Second}
			sortedRepos := config.ListRepositories(cfg)
			for _, repo := range sortedRepos {
				fmt.Printf("Querying repository: %s (%s)\n", repo.Name, repo.URL)
				// FetchRemotePackageMetadata returns (nil,nil) if 404, (nil, err) for other errors
				remotePkgMeta, fetchErr := repository.FetchRemotePackageMetadata(repo.URL, queryGroupID, queryArtifactID, httpClient)
				if fetchErr != nil {
					fmt.Fprintf(os.Stderr, "Error fetching metadata from %s for %s/%s: %v\n", repo.Name, queryGroupID, queryArtifactID, fetchErr)
					continue
				}
				if remotePkgMeta != nil { // Metadata found
					for versionStr, versionMeta := range remotePkgMeta.Versions {
						_ = versionMeta // to use versionMeta if needed for notes, etc.
						newItem := SearchResultItem{
							Source:      fmt.Sprintf("(remote: %s)", repo.Name),
							GroupID:     remotePkgMeta.GroupID,
							ArtifactID:  remotePkgMeta.ArtifactID,
							Version:     versionStr,
							Description: remotePkgMeta.Description, // Or versionMeta.Notes
							SourceRank:  2,
						}
						key := fmt.Sprintf("%s/%s:%s", newItem.GroupID, newItem.ArtifactID, newItem.Version)
						if existingItem, ok := deDupMap[key]; !ok || newItem.SourceRank < existingItem.SourceRank {
							deDupMap[key] = newItem
						}
					}
				} else {
					fmt.Printf("Package %s/%s not found in remote repository %s.\n", queryGroupID, queryArtifactID, repo.Name)
				}
			}
		}

		foundPackages := make([]SearchResultItem, 0, len(deDupMap))
		for _, item := range deDupMap {
			foundPackages = append(foundPackages, item)
		}

		sort.Slice(foundPackages, func(i, j int) bool {
			if foundPackages[i].SourceRank != foundPackages[j].SourceRank {
				return foundPackages[i].SourceRank < foundPackages[j].SourceRank
			}
			if foundPackages[i].GroupID != foundPackages[j].GroupID {
				return foundPackages[i].GroupID < foundPackages[j].GroupID
			}
			if foundPackages[i].ArtifactID != foundPackages[j].ArtifactID {
				return foundPackages[i].ArtifactID < foundPackages[j].ArtifactID
			}
			// TODO: Proper SemVer sort for version
			return foundPackages[i].Version < foundPackages[j].Version
		})

		if len(foundPackages) == 0 {
			if query != "" {
				fmt.Printf("No packages found matching query '%s'.\n", query)
			} else {
				fmt.Println("No packages found in local FPM app store or metadata cache.")
			}
			return nil
		}

		fmt.Printf("\n%-20s %-40s %-15s %s\n", "SOURCE", "PACKAGE (GROUP/ARTIFACT)", "VERSION", "DESCRIPTION")
		fmt.Printf("%-20s %-40s %-15s %s\n", strings.Repeat("-", 20), strings.Repeat("-", 40), strings.Repeat("-", 15), strings.Repeat("-", 11))
		for _, pkg := range foundPackages {
			packageName := fmt.Sprintf("%s/%s", pkg.GroupID, pkg.ArtifactID)
			desc := pkg.Description
			if len(desc) > 50 {
				desc = desc[:47] + "..."
			}
			fmt.Printf("%-20s %-40s %-15s %s\n", pkg.Source, packageName, pkg.Version, desc)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(searchCmd)
}
