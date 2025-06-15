package cmd

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"fpm/internal/config"
	"fpm/internal/metadata"      // For metadata.ReadMetadataFromFPMArchive
	"fpm/internal/repository" // For repository.FetchRemotePackageMetadata, etc.
	"fpm/internal/utils"      // For utils.CalculateFileChecksum

	"github.com/spf13/cobra"
)

var (
	publishRepoName string
	publishFromFile string
)

// publishCmd represents the publish command
var publishCmd = &cobra.Command{
	Use:   "publish [<group>/<artifact>[==<version>]]",
	Short: "Publish an FPM package to a repository",
	Long: `Publishes an FPM package to a configured repository.
The package can be specified directly via a .fpm file using --from-file,
or as a package identifier (e.g., myorg/myapp==1.0.0 or myorg/myapp for latest)
to publish from the local FPM app store.`,
	Args: cobra.MaximumNArgs(1), // 0 or 1 arg for optional package identifier
	RunE: func(cmd *cobra.Command, args []string) error {
		var fpmFilePathToPublish string
		var appOrg, appName, appVersion string // Will be determined from metadata

		cfg, err := config.InitConfig()
		if err != nil {
			return fmt.Errorf("failed to initialize FPM configuration: %w", err)
		}

		if publishFromFile != "" { // Case 1: --from-file is provided
			if len(args) > 0 {
				return fmt.Errorf("cannot use package identifier argument when --from-file is specified")
			}
			fpmFilePathToPublish, err = filepath.Abs(publishFromFile)
			if err != nil {
				return fmt.Errorf("failed to get absolute path for --from-file: %w", err)
			}
			if _, err := os.Stat(fpmFilePathToPublish); os.IsNotExist(err) {
				return fmt.Errorf(".fpm file specified by --from-file does not exist: %s", fpmFilePathToPublish)
			}
			fmt.Printf("Publishing from direct file: %s\n", fpmFilePathToPublish)
		} else if len(args) == 1 { // Case 2: Package identifier is provided
			packageIdentifier := args[0]
			var parsedGroupID, parsedArtifactID, parsedVersion string
			parts := strings.Split(packageIdentifier, "/")
			if len(parts) == 2 {
				parsedGroupID = strings.TrimSpace(parts[0])
				appAndVersion := strings.Split(parts[1], "==")
				parsedArtifactID = strings.TrimSpace(appAndVersion[0])
				if len(appAndVersion) == 2 {
					parsedVersion = strings.TrimSpace(appAndVersion[1])
				}
			} else {
				return fmt.Errorf("invalid package identifier format: '%s'. Expected <group>/<artifact> or <group>/<artifact>==<version>", packageIdentifier)
			}
			if parsedGroupID == "" || parsedArtifactID == "" {
				return fmt.Errorf("invalid package identifier: groupID and artifactID must be specified in '%s'", packageIdentifier)
			}

			appOrg = parsedGroupID
			appName = parsedArtifactID
			appVersion = parsedVersion

			if appVersion == "" || appVersion == "latest" {
				fmt.Printf("Resolving latest version for %s/%s from local FPM app store...\n", appOrg, appName)
				resolvedVersion, err := resolveLatestVersionFromLocalStore(cfg.AppsBasePath, appOrg, appName)
				if err != nil {
					return fmt.Errorf("failed to resolve latest version for %s/%s: %w", appOrg, appName, err)
				}
				if resolvedVersion == "" {
					return fmt.Errorf("no versions found for %s/%s in the local FPM app store. Package the desired version first or specify a version explicitly", appOrg, appName)
				}
				appVersion = resolvedVersion
				fmt.Printf("Latest version resolved to: %s\n", appVersion)
			}

			appVersionPathInStore := filepath.Join(cfg.AppsBasePath, appOrg, appName, appVersion)
			expectedFpmFilename := fmt.Sprintf("_%s-%s.fpm", appName, appVersion) // Note the underscore prefix
			fpmFilePathToPublish = filepath.Join(appVersionPathInStore, expectedFpmFilename)

			if _, err := os.Stat(fpmFilePathToPublish); os.IsNotExist(err) {
				return fmt.Errorf("package %s/%s version %s .fpm file not found in local FPM app store at %s", appOrg, appName, appVersion, fpmFilePathToPublish)
			}
			fmt.Printf("Publishing %s/%s version %s from local FPM store: %s\n", appOrg, appName, appVersion, fpmFilePathToPublish)
		} else {
			return fmt.Errorf("either a package identifier argument or --from-file flag must be provided")
		}

		currentAppMeta, err := metadata.ReadMetadataFromFPMArchive(fpmFilePathToPublish)
		if err != nil {
			return fmt.Errorf("failed to read metadata from FPM package %s: %w", fpmFilePathToPublish, err)
		}
		appOrg = currentAppMeta.Org
		appName = currentAppMeta.AppName
		appVersion = currentAppMeta.PackageVersion
		if appOrg == "" || appName == "" || appVersion == "" {
			return fmt.Errorf("package metadata in %s is incomplete (missing Org, AppName, or PackageVersion)", fpmFilePathToPublish)
		}

		var targetRepo config.RepositoryConfig
		if publishRepoName != "" {
			repo, found := cfg.Repositories[publishRepoName]
			if !found {
				return fmt.Errorf("specified repository '%s' not found in FPM configuration", publishRepoName)
			}
			targetRepo = repo
		} else if cfg.DefaultPublishRepository != "" {
			repo, found := cfg.Repositories[cfg.DefaultPublishRepository]
			if !found {
				return fmt.Errorf("default publish repository '%s' not found in FPM configuration. Please set a valid default or specify --repo", cfg.DefaultPublishRepository)
			}
			targetRepo = repo
		} else {
			return fmt.Errorf("no repository specified with --repo and no default publish repository is set. Use 'fpm repo add' and 'fpm repo default'")
		}
		fmt.Printf("Publishing to repository: %s (%s)\n", targetRepo.Name, targetRepo.URL)

		httpClient := &http.Client{Timeout: 180 * time.Second}

		fmt.Printf("Fetching remote metadata for %s/%s from %s...\n", appOrg, appName, targetRepo.Name)
		remoteMeta, metadataFound, err := repository.FetchRemotePackageMetadata(targetRepo.URL, appOrg, appName, httpClient)
		if err != nil {
			// FetchRemotePackageMetadata returns (nil, false, err) for actual errors
			return fmt.Errorf("failed to fetch remote package metadata: %w", err)
		}
		if !metadataFound { // This means 404, so create new metadata
			fmt.Printf("No existing remote metadata found for %s/%s. Creating new.\n", appOrg, appName)
			remoteMeta = &repository.PackageMetadata{
				GroupID:    appOrg,
				ArtifactID: appName,
				Versions:   make(map[string]repository.PackageVersionMetadata),
				Description: currentAppMeta.Description, // Use description from current package
			}
		} else {
			fmt.Printf("Existing remote metadata found for %s/%s.\n", appOrg, appName)
			if remoteMeta.Versions == nil {
				remoteMeta.Versions = make(map[string]repository.PackageVersionMetadata)
			}
			// Update description if it's empty on remote, from current package
			if remoteMeta.Description == "" && currentAppMeta.Description != "" {
				remoteMeta.Description = currentAppMeta.Description
			}
		}

		if _, exists := remoteMeta.Versions[appVersion]; exists {
			return fmt.Errorf("version %s for package %s/%s already exists in repository %s. Use --force to overwrite (not implemented).", appVersion, appOrg, appName, targetRepo.Name)
		}

		// Construct relative path for FPM file on server, ensure no leading slash for JoinPath with URL
		fpmServerRelPath := strings.TrimPrefix(fmt.Sprintf("/%s/%s/%s/%s-%s.fpm", appOrg, appName, appVersion, appName, appVersion), "/")
		fpmDestURL, err := url.JoinPath(targetRepo.URL, fpmServerRelPath)
		if err != nil {
			return fmt.Errorf("error constructing FPM upload URL: %w", err)
		}

		fmt.Printf("Uploading FPM package to %s...\n", fpmDestURL)
		// Assuming server expects PUT for FPM file upload
		err = repository.UploadHTTPFile(fpmDestURL, fpmFilePathToPublish, http.MethodPut, "application/octet-stream", httpClient, "", nil)
		if err != nil {
			return fmt.Errorf("failed to upload FPM package: %w", err)
		}

		checksum, err := utils.CalculateFileChecksum(fpmFilePathToPublish)
		if err != nil {
			return fmt.Errorf("failed to calculate checksum for %s: %w", fpmFilePathToPublish, err)
		}
		if currentAppMeta.ContentChecksum != "" && currentAppMeta.ContentChecksum != checksum {
		    fmt.Fprintf(os.Stderr, "Warning: checksum in app_metadata.json (%s) of the FPM file being published does not match its calculated content checksum (%s). Using calculated checksum for remote metadata.\n", currentAppMeta.ContentChecksum, checksum)
		}

		versionEntry := repository.PackageVersionMetadata{
			FPMPath:        fpmServerRelPath,
			ChecksumSHA256: checksum, // Use calculated checksum of the file
			ReleaseDate:    time.Now().UTC().Format(time.RFC3339Nano), // More precise timestamp
			Dependencies:   nil, // TODO: Populate from currentAppMeta.Dependencies (needs conversion)
			Notes:          currentAppMeta.Description, // Or a dedicated "release notes" field if added
		}
		remoteMeta.Versions[appVersion] = versionEntry

		// TODO: Implement proper SemVer comparison for LatestVersion
		if remoteMeta.LatestVersion == "" || appVersion > remoteMeta.LatestVersion {
			remoteMeta.LatestVersion = appVersion
		}

		fmt.Printf("Uploading updated metadata for %s/%s...\n", appOrg, appName)
		err = repository.UploadPackageMetadata(targetRepo.URL, appOrg, appName, remoteMeta, httpClient)
		if err != nil {
			return fmt.Errorf("failed to upload updated package metadata: %w", err)
		}

		fmt.Printf("Successfully published package %s/%s version %s to repository %s.\n", appOrg, appName, appVersion, targetRepo.Name)
		return nil
	},
}

// resolveLatestVersionFromLocalStore finds the "latest" version of an app in the local FPM store.
// Currently uses lexicographical sort. TODO: Implement proper SemVer sorting.
func resolveLatestVersionFromLocalStore(appsBasePath, groupID, artifactID string) (string, error) {
	versionsDir := filepath.Join(appsBasePath, groupID, artifactID)
	entries, err := os.ReadDir(versionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil // No versions found locally is not an error here, just means no local latest
		}
		return "", fmt.Errorf("failed to read versions directory %s: %w", versionsDir, err)
	}

	var availableVersions []string
	for _, entry := range entries {
		if entry.IsDir() {
			// TODO: Validate entry.Name() as a SemVer string before adding
			availableVersions = append(availableVersions, entry.Name())
		}
	}

	if len(availableVersions) == 0 {
		return "", nil // No versions found
	}

	// Simple lexicographical sort. Replace with SemVer sort for correctness.
	// For SemVer: use a library like "github.com/Masterminds/semver/v3"
	// Example:
	// vs := make([]*semver.Version, len(availableVersions))
	// for i, r := range availableVersions { vs[i], _ = semver.NewVersion(r) }
	// sort.Sort(semver.Collection(vs))
	// return vs[len(vs)-1].Original(), nil
	sort.Strings(availableVersions)
	return availableVersions[len(availableVersions)-1], nil
}

func init() {
	publishCmd.Flags().StringVar(&publishRepoName, "repo", "", "Name of the repository to publish to (must be configured in FPM)")
	publishCmd.Flags().StringVar(&publishFromFile, "from-file", "", "Path to the .fpm package file to publish directly")

	rootCmd.AddCommand(publishCmd)
}
