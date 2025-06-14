package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"fpm/internal/config" // For FPMConfig to find cache path relative to FPM base
	"fpm/internal/repository" // For PackageMetadata struct
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupMockFPMSearchCache creates a mock FPM cache structure with package-metadata.json files.
// It also sets the HOME/USERPROFILE env var to a temp dir where this cache is created.
func setupMockFPMSearchCache(t *testing.T, packages map[string]repository.PackageMetadata) (tempHomeDir string, cleanupFunc func()) {
	t.Helper()

	var origHome string
	var homeSet bool

	if runtime.GOOS == "windows" {
		origHome, homeSet = os.LookupEnv("USERPROFILE")
	} else {
		origHome, homeSet = os.LookupEnv("HOME")
	}

	tempHome, err := os.MkdirTemp("", "fpm-test-search-home-*")
	require.NoError(t, err)

	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", tempHome)
	} else {
		t.Setenv("HOME", tempHome)
	}

	// Create config file with this tempHome so cache path is derived correctly if code uses it
	// (though search currently defaults to ~/.fpm/cache)
	fpmConfigDir := filepath.Join(tempHome, ".fpm")
	require.NoError(t, os.MkdirAll(fpmConfigDir, 0o755))

	// The search command defaults its cache path discovery based on UserHomeDir then .fpm/cache.
	// It does not strictly need a full config file for *cache path discovery*, but InitConfig might be called.
	// Let's ensure InitConfig can run without error if searchCmd calls it.
	_, err = config.InitConfig() // This will create a default config in tempHome/.fpm/
	require.NoError(t, err, "Failed to init config in temp search home dir")


	cacheBaseDir := filepath.Join(tempHome, ".fpm", "cache")

	for repoName, pkgMeta := range packages {
		// Path: <cacheBaseDir>/<repoName>/metadata/<groupID>/<artifactID>/package-metadata.json
		metadataFilePath := filepath.Join(cacheBaseDir, repoName, "metadata", pkgMeta.GroupID, pkgMeta.ArtifactID, "package-metadata.json")
		require.NoError(t, os.MkdirAll(filepath.Dir(metadataFilePath), 0o755))

		metaBytes, err := json.MarshalIndent(pkgMeta, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(metadataFilePath, metaBytes, 0o644))
	}

	cleanup := func() {
		if homeSet {
			if runtime.GOOS == "windows" {
				os.Setenv("USERPROFILE", origHome)
			} else {
				os.Setenv("HOME", origHome)
			}
		} else {
			if runtime.GOOS == "windows" {
				os.Unsetenv("USERPROFILE")
			} else {
				os.Unsetenv("HOME")
			}
		}
		os.RemoveAll(tempHome)
	}

	return tempHome, cleanup
}


func TestSearchCmd(t *testing.T) {
	mockPackages := map[string]repository.PackageMetadata{
		"repo1": {
			GroupID:       "com.example",
			ArtifactID:    "appone",
			Description:   "First amazing application",
			LatestVersion: "1.2.0",
			Versions: map[string]repository.PackageVersionMetadata{
				"1.2.0": {FPMPath: "path/to/appone-1.2.0.fpm"},
				"1.0.0": {FPMPath: "path/to/appone-1.0.0.fpm"},
			},
		},
		"repo1": { // This will overwrite the previous entry for "repo1" if map key is just repoName.
			         // The cache structure is /<repoName>/metadata/<groupID>/<artifactID>/...
			         // So, the map key for setup should be unique per metadata file.
			         // Let's make the key more unique for setup.
			GroupID:       "com.example",
			ArtifactID:    "apptwo",
			Description:   "Second awesome app",
			LatestVersion: "2.1.0",
			Versions: map[string]repository.PackageVersionMetadata{
				"2.1.0": {FPMPath: "path/to/apptwo-2.1.0.fpm"},
			},
		},
		"repo2": {
			GroupID:       "org.another",
			ArtifactID:    "utility",
			Description:   "A useful utility app",
			LatestVersion: "0.5.0",
			Versions: map[string]repository.PackageVersionMetadata{
				"0.5.0": {FPMPath: "path/to/utility-0.5.0.fpm"},
			},
		},
	}
	// Corrected mockPackages setup: key should be unique for each package metadata file.
	// The key can represent the path segment <repoName>/metadata/<groupID>/<artifactID>
	// or simply ensure different repoNames if group/artifact are the same for this test map.
	// The cache structure allows multiple groupID/artifactID under one repoName.
	// Let's use distinct repoName for simplicity in this map, or make a list of structs.
	// For this test, let's assume distinct metadata files are needed.

	// Redefine mockPackages for clarity, ensuring distinct metadata paths.
	// The setup helper creates path using <repoName>/metadata/<groupID>/<artifactID>
	// So, the map key for setupMockFPMSearchCache should be unique enough to allow this.
	// Using repoName as key is fine if each pkgMeta has unique GroupID/ArtifactID *within that repo*.
	// The issue was that "repo1" was used twice as a map key, overwriting.
	// Let's use a list of structs for more explicit setup.
	type mockPackageEntry struct {
		RepoName string
		Metadata repository.PackageMetadata
	}

	mockEntries := []mockPackageEntry{
		{RepoName: "repo1", Metadata: repository.PackageMetadata{
			GroupID: "com.example", ArtifactID: "appone", Description: "First amazing application", LatestVersion: "1.2.0",
			Versions: map[string]repository.PackageVersionMetadata{"1.2.0": {FPMPath: "path/to/appone-1.2.0.fpm"}},
		}},
		{RepoName: "repo1", Metadata: repository.PackageMetadata{ // Same repo, different app
			GroupID: "com.example", ArtifactID: "apptwo", Description: "Second awesome app", LatestVersion: "2.1.0",
			Versions: map[string]repository.PackageVersionMetadata{"2.1.0": {FPMPath: "path/to/apptwo-2.1.0.fpm"}},
		}},
		{RepoName: "repo2", Metadata: repository.PackageMetadata{
			GroupID: "org.another", ArtifactID: "utility", Description: "A useful utility app", LatestVersion: "0.5.0",
			Versions: map[string]repository.PackageVersionMetadata{"0.5.0": {FPMPath: "path/to/utility-0.5.0.fpm"}},
		}},
		{RepoName: "repo3", Metadata: repository.PackageMetadata{ // Package for testing no description match
			GroupID: "com.special", ArtifactID: "nodessapp", LatestVersion: "1.0.0", Description: "",
			Versions: map[string]repository.PackageVersionMetadata{"1.0.0": {FPMPath: "path/to/nodessapp-1.0.0.fpm"}},
		}},
	}


	tempHome, cleanup := setupMockFPMSearchCacheWithEntries(t, mockEntries)
	defer cleanup()
	t.Logf("TestSearchCmd using temp home for cache: %s", tempHome)

	testCases := []struct {
		name           string
		query          string
		expectedFound  []string // list of <groupID>/<artifactID> expected
		unexpectedFound []string // list of <groupID>/<artifactID> not expected
		expectNoResults bool
	}{
		{
			name:  "list all (no query)",
			query: "",
			expectedFound: []string{"com.example/appone", "com.example/apptwo", "org.another/utility", "com.special/nodessapp"},
		},
		{
			name:  "search by artifactID",
			query: "apptwo",
			expectedFound: []string{"com.example/apptwo"},
			unexpectedFound: []string{"com.example/appone", "org.another/utility"},
		},
		{
			name:  "search by groupID",
			query: "com.example",
			expectedFound: []string{"com.example/appone", "com.example/apptwo"},
			unexpectedFound: []string{"org.another/utility"},
		},
		{
			name:  "search by description",
			query: "useful",
			expectedFound: []string{"org.another/utility"},
			unexpectedFound: []string{"com.example/appone"},
		},
		{
			name:  "search by partial description",
			query: "app", // Should match "application" and "app"
			expectedFound: []string{"com.example/appone", "com.example/apptwo", "org.another/utility"},
		},
		{
			name:  "case insensitive search",
			query: "EXAMPLE",
			expectedFound: []string{"com.example/appone", "com.example/apptwo"},
		},
		{
			name:            "no results found",
			query:           "nonexistentpackage",
			expectNoResults: true,
		},
		{
			name:  "search matching empty description (should not list if query non-empty)",
			query: "nodessapp", // Matches artifactID
			expectedFound: []string{"com.special/nodessapp"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"search"}
			if tc.query != "" {
				args = append(args, tc.query)
			}
			output, err := executeCommand(rootCmd, args...)
			require.NoError(t, err, "fpm search command failed")

			if tc.expectNoResults {
				assert.Contains(t, output, "No package metadata found in the local cache.")
			} else {
				for _, expected := range tc.expectedFound {
					assert.Contains(t, output, expected, "Expected to find package %s", expected)
				}
				if tc.unexpectedFound != nil {
					for _, unexpected := range tc.unexpectedFound {
						assert.NotContains(t, output, unexpected, "Did not expect to find package %s", unexpected)
					}
				}
			}
		})
	}
}

// setupMockFPMSearchCacheWithEntries is a corrected helper for setting up the cache.
func setupMockFPMSearchCacheWithEntries(t *testing.T, entries []struct{RepoName string; Metadata repository.PackageMetadata}) (string, func()) {
	t.Helper()
	// Simplified HOME setup, assuming default .fpm path construction
	tempHome, err := os.MkdirTemp("", "fpm-test-search-home-*")
	require.NoError(t, err)

	origHomeVal := os.Getenv("HOME")
	homeEnvVar := "HOME"
	if runtime.GOOS == "windows" {
		origHomeVal = os.Getenv("USERPROFILE")
		homeEnvVar = "USERPROFILE"
	}

	require.NoError(t, os.Setenv(homeEnvVar, tempHome))

	// Ensure config is initialized for this tempHome
	_, err = config.InitConfig()
	require.NoError(t, err)


	cacheBaseDir := filepath.Join(tempHome, ".fpm", "cache")

	for _, entry := range entries {
		pkgMeta := entry.Metadata
		metadataFilePath := filepath.Join(cacheBaseDir, entry.RepoName, "metadata", pkgMeta.GroupID, pkgMeta.ArtifactID, "package-metadata.json")
		require.NoError(t, os.MkdirAll(filepath.Dir(metadataFilePath), 0o755))

		metaBytes, err := json.MarshalIndent(pkgMeta, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(metadataFilePath, metaBytes, 0o644))
	}

	cleanup := func() {
		if origHomeVal == "" {
			os.Unsetenv(homeEnvVar)
		} else {
			os.Setenv(homeEnvVar, origHomeVal)
		}
		os.RemoveAll(tempHome)
	}
	return tempHome, cleanup
}
