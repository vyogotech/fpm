package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"fpm/internal/archive" // For archive.VerifyArchiveContentChecksum
	"fpm/internal/config"
	"fpm/internal/metadata"    // For metadata.ReadMetadataFromFPMArchive
	"fpm/internal/ociregistry" // For OCI registry publishing
	"fpm/internal/repository"  // For repository.FetchRemotePackageMetadata, etc.
	"fpm/internal/semver"      // For correct latest-version selection
	"fpm/internal/utils"       // For utils.CalculateFileChecksum

	"github.com/spf13/cobra"
)

var (
	publishRepoName string
	publishFromFile string
	publishForce    bool
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

			appOrg = parsedOrg      // Use renamed variables
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

		// Verify the archive's payload still matches the checksum recorded inside it,
		// before anything is uploaded, so a tampered package never reaches a repository.
		if err := archive.VerifyArchiveContentChecksum(fpmFilePathToPublish, currentAppMeta.ContentChecksum); err != nil {
			return fmt.Errorf("integrity check failed for %s: %w", fpmFilePathToPublish, err)
		}
		fmt.Printf("Verified package contents against checksum %s.\n", currentAppMeta.ContentChecksum)

		// OCI Repository Backend
		if targetRepo.Type == "oci" {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			// Check if version already exists
			exists, _, err := ociregistry.Exists(ctx, targetRepo, appOrg, appName, appVersion)
			if err != nil {
				return fmt.Errorf("failed to check if version %s exists in OCI repository %s: %w", appVersion, targetRepo.Name, err)
			}
			if exists && !publishForce {
				return fmt.Errorf("version %s for package %s/%s already exists in OCI repository %s", appVersion, appOrg, appName, targetRepo.Name)
			}

			var extraTags []string
			if currentAppMeta.CommitSHA != "" {
				extraTags = append(extraTags, currentAppMeta.CommitSHA)
			}

			fmt.Printf("Pushing OCI package layer and manifest to %s (%s/%s:%s)...\n", targetRepo.Name, appOrg, appName, appVersion)
			manifestDesc, err := ociregistry.Push(ctx, targetRepo, fpmFilePathToPublish, currentAppMeta, extraTags)
			if err != nil {
				return fmt.Errorf("failed to publish to OCI repository %s: %w", targetRepo.Name, err)
			}

			fmt.Printf("Successfully published package %s/%s version %s to OCI repository %s.\n", appOrg, appName, appVersion, targetRepo.Name)
			fmt.Printf("  Manifest Digest: %s (%d bytes)\n", manifestDesc.Digest.String(), manifestDesc.Size)
			return nil
		}

		// WebDAV / HTTP Repository Backend
		// Publishing writes to the repository, which is exactly what a secured repository
		// requires credentials for, so resolve them before any request is made.
		creds, err := repository.ResolveCredentials(targetRepo.Name, targetRepo.Username, true)
		if err != nil {
			return err
		}
		httpClient, err := repository.NewClient(targetRepo.URL, creds, 180*time.Second)
		if err != nil {
			return err
		}
		if creds.Configured() {
			fmt.Printf("Authenticating to %s as '%s'.\n", targetRepo.Name, creds.Username)
		}

		fmt.Printf("Fetching remote metadata for %s/%s from %s...\n", appOrg, appName, targetRepo.Name)
		remoteMeta, metadataExisted, err := repository.FetchRemotePackageMetadata(targetRepo.URL, appOrg, appName, httpClient)
		if err != nil {
			return fmt.Errorf("failed to fetch remote package metadata for %s/%s from %s: %w", appOrg, appName, targetRepo.URL, err)
		}

		if !metadataExisted {
			fmt.Printf("No existing metadata found for %s/%s on remote repository %s. Initializing new metadata.\n", appOrg, appName, targetRepo.Name)
			remoteMeta = &repository.PackageMetadata{
				Org:         appOrg,
				AppName:     appName,
				Title:       currentAppMeta.Title,
				Icon:        currentAppMeta.Icon,
				IconFile:    currentAppMeta.IconFile,
				Versions:    make(map[string]repository.PackageVersionMetadata),
				Description: currentAppMeta.Description, // Populate description from current package
			}
		} else if remoteMeta != nil { // metadataExisted is true, remoteMeta should not be nil if no error occurred
			fmt.Printf("Successfully fetched existing metadata for %s/%s from repository %s.\n", appOrg, appName, targetRepo.Name)
			if remoteMeta.Versions == nil {
				remoteMeta.Versions = make(map[string]repository.PackageVersionMetadata)
			}
			// Ensure Org and AppName in fetched metadata match, or update if necessary (using local as source of truth for path)
			remoteMeta.Org = appOrg
			remoteMeta.AppName = appName
			if remoteMeta.Title == "" && currentAppMeta.Title != "" {
				remoteMeta.Title = currentAppMeta.Title
			}
			if remoteMeta.Description == "" && currentAppMeta.Description != "" { // If remote has no desc, use current's
				remoteMeta.Description = currentAppMeta.Description
			}
			if remoteMeta.Icon == "" && currentAppMeta.Icon != "" {
				remoteMeta.Icon = currentAppMeta.Icon
			}
			if remoteMeta.IconFile == "" && currentAppMeta.IconFile != "" {
				remoteMeta.IconFile = currentAppMeta.IconFile
			}
		} else {
			// This case should ideally not happen if metadataExisted is true and err is nil.
			return fmt.Errorf("internal error: metadata reported as existing but was nil for %s/%s from %s", appOrg, appName, targetRepo.URL)
		}

		if _, exists := remoteMeta.Versions[appVersion]; exists && !publishForce {
			return fmt.Errorf("version %s for package %s/%s already exists in repository %s", appVersion, appOrg, appName, targetRepo.Name)
		}

		fpmServerRelPath := strings.TrimPrefix(fmt.Sprintf("/%s/%s/%s/%s-%s.fpm", appOrg, appName, appVersion, appName, appVersion), "/")
		fpmDestURL, err := url.JoinPath(targetRepo.URL, fpmServerRelPath)
		if err != nil {
			return fmt.Errorf("error constructing FPM upload URL: %w", err)
		}

		// ChecksumSHA256 in the repository metadata covers the raw .fpm bytes so clients
		// can verify the download itself. It is intentionally a different value from
		// ContentChecksum, which covers the extracted payload.
		checksum, err := utils.CalculateFileChecksum(fpmFilePathToPublish)
		if err != nil {
			return fmt.Errorf("failed to calculate checksum for %s: %w", fpmFilePathToPublish, err)
		}

		var extraHeaders map[string]string
		if publishForce {
			extraHeaders = map[string]string{"X-FPM-Force": "true"}
		}

		fmt.Printf("Uploading FPM package to %s...\n", fpmDestURL)
		err = repository.UploadHTTPFile(fpmDestURL, fpmFilePathToPublish, http.MethodPut, "application/octet-stream", httpClient, "", extraHeaders)
		if err != nil {
			// A bare 401 gives no hint that credentials are the missing piece, and this
			// is the first write a publish makes, so it is where auth problems surface.
			if repository.IsAuthFailure(err) {
				var statusErr *repository.HTTPStatusError
				errors.As(err, &statusErr)
				return fmt.Errorf("failed to upload FPM package: %w\n%s",
					err, repository.DescribeAuthFailure(targetRepo.Name, targetRepo.Username, statusErr.StatusCode))
			}
			if repository.IsTooLarge(err) {
				size := int64(0)
				if info, statErr := os.Stat(fpmFilePathToPublish); statErr == nil {
					size = info.Size()
				}
				return fmt.Errorf("failed to upload FPM package: %w\n%s",
					err, repository.DescribeTooLarge(targetRepo.Name, size))
			}
			return fmt.Errorf("failed to upload FPM package: %w", err)
		}

		versionEntry := repository.PackageVersionMetadata{
			FPMPath:        fpmServerRelPath,
			ChecksumSHA256: checksum,
			ReleaseDate:    time.Now().UTC().Format(time.RFC3339Nano),
			Dependencies:   repository.DependenciesFrom(currentAppMeta.Dependencies),
			Notes:          currentAppMeta.Description,
			// Carried into the published metadata so a consumer can read
			// compatibility and provenance without downloading and unpacking
			// the artifact. Every field is omitempty, so an older client simply
			// ignores what it does not recognise.
			FrappeCompatibility: currentAppMeta.FrappeCompatibility,
			SourceControlURL:    currentAppMeta.SourceControlURL,
			Title:               currentAppMeta.Title,
			Author:              currentAppMeta.Author,
			Publisher:           currentAppMeta.Publisher,
			Email:               currentAppMeta.Email,
			License:             currentAppMeta.License,
			Icon:                currentAppMeta.Icon,
			IconFile:            currentAppMeta.IconFile,
			PackageType:         currentAppMeta.PackageType,
			WheelPlatform:       currentAppMeta.WheelPlatform,
			WheelPythonVersion:  currentAppMeta.WheelPythonVersion,
			// Identity and dependency closure, so `fpm exists` and orchestration
			// tooling can answer from metadata alone.
			CommitSHA:    currentAppMeta.CommitSHA,
			GitRef:       currentAppMeta.GitRef,
			RequiredApps: repository.RequiredAppsFrom(currentAppMeta.RequiredApps),
		}
		remoteMeta.Versions[appVersion] = versionEntry

		// Recomputed across every published version rather than comparing the
		// newcomer against the stored value. The previous comparison was a raw
		// string compare, which ranked "1.9.0" above "1.10.0"; recomputing from
		// the whole set also repairs metadata that comparison already corrupted.
		remoteMeta.LatestVersion = semver.LatestOf(remoteMeta.Versions)

		fmt.Printf("Uploading updated metadata for %s/%s...\n", appOrg, appName)
		err = repository.UploadPackageMetadata(targetRepo.URL, appOrg, appName, remoteMeta, httpClient)
		if err != nil {
			return fmt.Errorf("failed to upload updated package metadata: %w", err)
		}

		// Keep the repository's package catalogue current, so `fpm search --remote` can
		// discover this package by keyword rather than only by exact <org>/<app>.
		if err := updateRepositoryIndex(targetRepo, remoteMeta, versionEntry.ReleaseDate, httpClient); err != nil {
			// The package itself published successfully; a stale catalogue makes it
			// harder to discover but does not make it unusable.
			fmt.Fprintf(os.Stderr, "Warning: failed to update repository index: %v\n", err)
		}

		fmt.Printf("Successfully published package %s/%s version %s to repository %s.\n", appOrg, appName, appVersion, targetRepo.Name)
		return nil
	},
}

// updateRepositoryIndex records the just-published package in the repository catalogue,
// creating the catalogue if the repository has none yet.
func updateRepositoryIndex(repo config.RepositoryConfig, meta *repository.PackageMetadata,
	releaseDate string, client *http.Client,
) error {
	idx, found, err := repository.FetchRepositoryIndex(repo.URL, client)
	if err != nil {
		return err
	}
	if !found || idx == nil {
		fmt.Printf("Repository %s has no package index yet. Creating one.\n", repo.Name)
		idx = &repository.RepositoryIndex{}
	}

	idx.Upsert(repository.IndexEntry{
		Org:           meta.Org,
		AppName:       meta.AppName,
		Description:   meta.Description,
		LatestVersion: meta.LatestVersion,
		UpdatedAt:     releaseDate,
	})

	fmt.Printf("Updating package index for repository %s...\n", repo.Name)
	return repository.UploadRepositoryIndex(repo.URL, idx, client)
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
	publishCmd.Flags().BoolVar(&publishForce, "force", false, "Overwrite existing package version in repository")

	rootCmd.AddCommand(publishCmd)
}
