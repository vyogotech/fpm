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
			var parsedOrg, parsedAppName, parsedVersion string // Renamed variables
			parts := strings.Split(packageIdentifier, "/")
			if len(parts) == 2 {
				parsedOrg = strings.TrimSpace(parts[0]) // Renamed variable
				appAndVersion := strings.Split(parts[1], "==")
				parsedAppName = strings.TrimSpace(appAndVersion[0]) // Renamed variable
				if len(appAndVersion) == 2 {
					parsedVersion = strings.TrimSpace(appAndVersion[1])
				}
			} else {
				return fmt.Errorf("invalid package identifier format: '%s'. Expected <org>/<appName> or <org>/<appName>==<version>", packageIdentifier)
			}
			if parsedOrg == "" || parsedAppName == "" { // Renamed variables
				return fmt.Errorf("invalid package identifier: Org and AppName must be specified in '%s'", packageIdentifier)
			}

			appOrg = parsedOrg     // Use renamed variables
			appName = parsedAppName // Use renamed variables
			appVersion = parsedVersion

			if appVersion == "" || appVersion == "latest" {
				fmt.Printf("Resolving latest version for %s/%s from local FPM app store...\n", appOrg, appName)
				resolvedVersion, err := resolveLatestVersionFromLocalStore(cfg.AppsBasePath, appOrg, appName) // Call with new params
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
				return fmt.Errorf("package %s/%s version %s .fpm file not found in local FPM app store at %s (expected %s)", appOrg, appName, appVersion, appVersionPathInStore, expectedFpmFilename)
			}
			fmt.Printf("Publishing %s/%s version %s from local FPM store: %s\n", appOrg, appName, appVersion, fpmFilePathToPublish)
		} else {
			return fmt.Errorf("either a package identifier argument or --from-file flag must be provided")
		}

		currentAppMeta, err := metadata.ReadMetadataFromFPMArchive(fpmFilePathToPublish)
		if err != nil {
			return fmt.Errorf("failed to read metadata from FPM package %s: %w", fpmFilePathToPublish, err)
		}
		// Use metadata from package as source of truth for Org, AppName, PackageVersion for publishing coordinates
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
		// FetchRemotePackageMetadata returns (nil,nil) for 404, which is fine (means new package)
		// The boolean metadataFound was part of a planned signature but current FetchRemotePackageMetadata returns (meta, err)
		// where meta is nil and err is nil for a 404.
		remoteMeta, fetchMetaErr := repository.FetchRemotePackageMetadata(targetRepo.URL, appOrg, appName, httpClient)
		if fetchMetaErr != nil {
			return fmt.Errorf("failed to fetch remote package metadata: %w", fetchMetaErr)
		}

		if remoteMeta == nil { // Metadata did not exist (e.g. 404)
			fmt.Printf("No existing remote metadata found for %s/%s. Creating new.\n", appOrg, appName)
			remoteMeta = &repository.PackageMetadata{
				Org:        appOrg,    // Use renamed field
				AppName:    appName, // Use renamed field
				Versions:   make(map[string]repository.PackageVersionMetadata),
				Description: currentAppMeta.Description,
			}
		} else {
			fmt.Printf("Existing remote metadata found for %s/%s.\n", appOrg, appName)
			if remoteMeta.Versions == nil {
				remoteMeta.Versions = make(map[string]repository.PackageVersionMetadata)
			}
			// Ensure Org and AppName in fetched metadata match what we expect, or use fetched ones as canonical.
			// For now, assume appOrg and appName derived from user input/local FPM are the target.
			// Update description if it's empty on remote, from current package
			if remoteMeta.Description == "" && currentAppMeta.Description != "" {
				remoteMeta.Description = currentAppMeta.Description
			}
			// It's good practice to ensure GroupID and ArtifactID in remoteMeta match appOrg and appName if it's not new.
			remoteMeta.Org = appOrg
			remoteMeta.AppName = appName
		}

		if _, exists := remoteMeta.Versions[appVersion]; exists {
			// TODO: Add a --force flag to allow overwriting? For now, error out.
			return fmt.Errorf("version %s for package %s/%s already exists in repository %s", appVersion, appOrg, appName, targetRepo.Name)
		}

		fpmServerRelPath := strings.TrimPrefix(fmt.Sprintf("/%s/%s/%s/%s-%s.fpm", appOrg, appName, appVersion, appName, appVersion), "/")
		fpmDestURL, err := url.JoinPath(targetRepo.URL, fpmServerRelPath)
		if err != nil {
			return fmt.Errorf("error constructing FPM upload URL: %w", err)
		}

		fmt.Printf("Uploading FPM package to %s...\n", fpmDestURL)
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
			ChecksumSHA256: checksum,
			ReleaseDate:    time.Now().UTC().Format(time.RFC3339Nano),
			Dependencies:   nil, // TODO: Populate from currentAppMeta.Dependencies
			Notes:          currentAppMeta.Description,
		}
		remoteMeta.Versions[appVersion] = versionEntry

		if remoteMeta.LatestVersion == "" || appVersion > remoteMeta.LatestVersion { // TODO: Proper SemVer
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

func resolveLatestVersionFromLocalStore(appsBasePath, groupID, artifactID string) (string, error) {
	versionsDir := filepath.Join(appsBasePath, groupID, artifactID)
	entries, err := os.ReadDir(versionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("failed to read versions directory %s: %w", versionsDir, err)
	}

	var availableVersions []string
	for _, entry := range entries {
		if entry.IsDir() {
			availableVersions = append(availableVersions, entry.Name())
		}
	}

	if len(availableVersions) == 0 {
		return "", nil
	}
	sort.Strings(availableVersions) // TODO: Replace with SemVer sort
	return availableVersions[len(availableVersions)-1], nil
}

func init() {
	publishCmd.Flags().StringVar(&publishRepoName, "repo", "", "Name of the repository to publish to (must be configured in FPM)")
	publishCmd.Flags().StringVar(&publishFromFile, "from-file", "", "Path to the .fpm package file to publish directly")

	rootCmd.AddCommand(publishCmd)
}
