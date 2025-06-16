package repository

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	// "sort" // Not directly used in this file, but config.ListRepositories is.
	"time"

	"fpm/internal/config"
	// "fpm/internal/utils" // For future checksum verification
)

// PackageVersionMetadata holds metadata for a specific version of a package.
type PackageVersionMetadata struct {
	FPMPath          string       `json:"fpm_path"`
	ChecksumSHA256   string       `json:"checksum_sha256"`
	ReleaseDate      string       `json:"release_date,omitempty"`
	Dependencies     []Dependency `json:"dependencies,omitempty"`
	Notes            string       `json:"notes,omitempty"`
}

// Dependency defines a package dependency.
type Dependency struct {
	Org               string `json:"org"`
	AppName           string `json:"appName"`
	VersionConstraint string `json:"version_constraint"`
}

// PackageMetadata is the structure of package-metadata.json from a repository.
type PackageMetadata struct {
	Org           string                          `json:"org"`
	AppName       string                          `json:"appName"`
	Description   string                          `json:"description,omitempty"`
	LatestVersion string                          `json:"latest_version,omitempty"`
	Versions      map[string]PackageVersionMetadata `json:"versions"`
}

// DownloadedPackageInfo holds information about a successfully downloaded/cached package.
type DownloadedPackageInfo struct {
	LocalPath      string
	RepositoryName string
	Org            string // Org of the package from its metadata
	AppName        string // AppName of the package from its metadata
	Version        string
	Checksum       string
}

// FindPackageInRepos searches for a specific package version across configured repositories.
func FindPackageInRepos(cfg *config.FPMConfig, org, appName, requestedVersion string) (*DownloadedPackageInfo, error) {
	if cfg == nil || cfg.Repositories == nil {
		return nil, fmt.Errorf("repository configuration is missing or not loaded")
	}

	sortedRepos := config.ListRepositories(cfg)

	if len(sortedRepos) == 0 {
		return nil, fmt.Errorf("no repositories configured. Use 'fpm repo add' to add a repository")
	}

	client := &http.Client{Timeout: time.Second * 30}
	userAgent := "fpm-client/0.1.0" // Define User-Agent

	for _, repo := range sortedRepos {
		fmt.Printf("Searching for %s/%s version '%s' in repository '%s' (%s)...\n", org, appName, requestedVersion, repo.Name, repo.URL)

		// Construct metadata URL: <repo.URL>/metadata/<org>/<appName>/package-metadata.json
		metadataPath := fmt.Sprintf("metadata/%s/%s/package-metadata.json", org, appName)
		fullMetadataURL, err := url.JoinPath(repo.URL, metadataPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error constructing metadata URL for repo %s: %v\n", repo.Name, err)
			continue
		}

		req, err := http.NewRequest("GET", fullMetadataURL, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating request for %s: %v\n", fullMetadataURL, err)
			continue
		}
		req.Header.Set("User-Agent", userAgent)

		resp, err := client.Do(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to fetch metadata %s from repo %s: %v\n", fullMetadataURL, repo.Name, err)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024)) // Limit reading response body
			resp.Body.Close()
			fmt.Fprintf(os.Stderr, "Failed to fetch metadata %s (status: %s). Response: %s\n", fullMetadataURL, resp.Status, string(bodyBytes))
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close() // Close body after reading
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to read response body from %s: %v\n", fullMetadataURL, err)
			continue
		}

		var pkgMeta PackageMetadata
		if err := json.Unmarshal(body, &pkgMeta); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to parse package-metadata.json from %s (repo: %s): %v\n", fullMetadataURL, repo.Name, err)
			continue
		}

		targetVersion := requestedVersion
		if targetVersion == "" || targetVersion == "latest" {
			if pkgMeta.LatestVersion == "" {
				fmt.Fprintf(os.Stderr, "Latest version not specified in metadata for %s/%s in repo %s\n", org, appName, repo.Name)
				continue
			}
			targetVersion = pkgMeta.LatestVersion
			fmt.Printf("Resolved 'latest' to version %s for %s/%s in repo %s\n", targetVersion, org, appName, repo.Name)
		}

		versionMeta, ok := pkgMeta.Versions[targetVersion]
		if !ok {
			fmt.Printf("Version %s for %s/%s not found in repo %s metadata.\n", targetVersion, org, appName, repo.Name)
			continue
		}

		fpmDownloadURL, err := url.JoinPath(repo.URL, versionMeta.FPMPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error constructing FPM download URL for %s in repo %s: %v\n", versionMeta.FPMPath, repo.Name, err)
			continue
		}
		fmt.Printf("Found version %s for %s/%s in repo %s. FPM URL: %s\n", targetVersion, org, appName, repo.Name, fpmDownloadURL)

		homeDir, err := os.UserHomeDir()
		if err != nil {
			// This error is critical and should probably stop the process
			return nil, fmt.Errorf("failed to get user home directory: %w", err)
		}
		fpmBaseDir := filepath.Join(homeDir, ".fpm")
		fpmFileName := filepath.Base(versionMeta.FPMPath) // Extract filename from FPMPath
		cacheDir := filepath.Join(fpmBaseDir, "cache", repo.Name, org, appName, targetVersion) // Use org, appName for path
		cachedFPMPath := filepath.Join(cacheDir, fpmFileName)

		if err := os.MkdirAll(cacheDir, 0o750); err != nil {
			// This could be a more serious error, maybe return it instead of continue
			return nil, fmt.Errorf("failed to create cache directory %s: %w", cacheDir, err)
		}

		// Basic cache check (existence only for now)
		if info, err := os.Stat(cachedFPMPath); err == nil && !info.IsDir() && info.Size() > 0 { // Check if file exists and is not empty
			fmt.Printf("Package found in cache: %s. Using cached file (checksum verification TODO).\n", cachedFPMPath)
			return &DownloadedPackageInfo{
				LocalPath:      cachedFPMPath,
				RepositoryName: repo.Name,
				Org:            pkgMeta.Org,     // Populate from parsed package metadata
				AppName:        pkgMeta.AppName, // Populate from parsed package metadata
				Version:        targetVersion,
				Checksum:       versionMeta.ChecksumSHA256,
			}, nil
		}

		fmt.Printf("Downloading %s to %s...\n", fpmDownloadURL, cachedFPMPath)

		dlReq, err := http.NewRequest("GET", fpmDownloadURL, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating request for %s: %v\n", fpmDownloadURL, err)
			continue
		}
		dlReq.Header.Set("User-Agent", userAgent)

		fpmResp, err := client.Do(dlReq)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to download FPM file %s: %v\n", fpmDownloadURL, err)
			continue
		}
		// It's important to defer fpmResp.Body.Close() right after checking fpmResp for nil
		// and before checking status code, to ensure it's closed even if status is not OK.
		// However, we need to read the body for error messages if status is not OK.
		// So, we'll close it explicitly after potential reads or successful copy.

		if fpmResp.StatusCode != http.StatusOK {
			// Try to read a bit of the body for error context
			errorBodyBytes, _ := io.ReadAll(io.LimitReader(fpmResp.Body, 1024))
			fpmResp.Body.Close()
			fmt.Fprintf(os.Stderr, "Failed to download FPM file %s (status: %s). Response hint: %s\n", fpmDownloadURL, fpmResp.Status, string(errorBodyBytes))
			continue
		}

		outFile, err := os.Create(cachedFPMPath) // os.Create truncates if file exists
		if err != nil {
			fpmResp.Body.Close() // Close the response body as we are erroring out
			fmt.Fprintf(os.Stderr, "Failed to create file in cache %s: %v\n", cachedFPMPath, err)
			continue
		}

		_, copyErr := io.Copy(outFile, fpmResp.Body)

		// Close files before checking errors related to them
		closeOutErr := outFile.Close()
		closeBodyErr := fpmResp.Body.Close()


		if copyErr != nil {
			fmt.Fprintf(os.Stderr, "Failed to write FPM file to cache %s: %v\n", cachedFPMPath, copyErr)
			os.Remove(cachedFPMPath) // Attempt to clean up partial download
			continue
		}
		if closeOutErr != nil {
			fmt.Fprintf(os.Stderr, "Failed to close cached file %s: %v\n", cachedFPMPath, closeOutErr)
			// The file is written, but closing failed. This might be an issue.
			// For now, we'll proceed but this could be made a hard error.
		}
		if closeBodyErr != nil {
             fmt.Fprintf(os.Stderr, "Failed to close FPM download response body for %s: %v\n", fpmDownloadURL, closeBodyErr)
        }

		fmt.Printf("Successfully downloaded %s.\n", fpmFileName)

		// TODO: Implement and call utils.CalculateFileChecksum(cachedFPMPath)
		// TODO: Compare with versionMeta.ChecksumSHA256
		// TODO: If mismatch, os.Remove(cachedFPMPath) and continue
		if versionMeta.ChecksumSHA256 != "" {
			fmt.Printf("Checksum verification for %s to be implemented (expected: %s).\n", cachedFPMPath, versionMeta.ChecksumSHA256)
		} else {
			fmt.Printf("No checksum provided in metadata for %s. Skipping verification.\n", fpmFileName)
		}

		return &DownloadedPackageInfo{
			LocalPath:      cachedFPMPath,
			RepositoryName: repo.Name,
			Org:            pkgMeta.Org,     // Populate from parsed package metadata
			AppName:        pkgMeta.AppName, // Populate from parsed package metadata
			Version:        targetVersion,
			Checksum:       versionMeta.ChecksumSHA256,
		}, nil
	}

	return nil, fmt.Errorf("package %s/%s version '%s' not found in any configured repositories", org, appName, requestedVersion)
}
