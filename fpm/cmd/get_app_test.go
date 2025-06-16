package cmd

import (
	"archive/zip" // For creating dummy FPM for mock server
	"bytes"       // For zip buffer
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	// "runtime" // Not used
	"strings"
	"testing"

	"fpm/internal/config"     // For FPMConfig
	"fpm/internal/metadata"   // For AppMetadata
	"fpm/internal/repository" // For PackageMetadata, PackageVersionMetadata
	// "github.com/spf13/cobra" // Not directly used, executeCommand handles it
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createDummyFPMBytes generates a minimal FPM zip archive in memory for tests.
// It includes a basic app_metadata.json.
func createDummyFPMBytes(t *testing.T, appOrg, appName, appVersion string) []byte {
	t.Helper()
	buf := new(bytes.Buffer)
	zipWriter := zip.NewWriter(buf)

	// Create app_metadata.json
	appMeta := metadata.AppMetadata{
		Org:            appOrg,
		AppName:        appName,
		PackageVersion: appVersion,
		Description:    "A test app.",
	}
	metaBytes, err := json.MarshalIndent(appMeta, "", "  ")
	require.NoError(t, err)
	metaWriter, err := zipWriter.Create("app_metadata.json")
	require.NoError(t, err)
	_, err = metaWriter.Write(metaBytes)
	require.NoError(t, err)

	// Add a dummy app module directory and a file in it
	// For zip, a path ending with "/" is typically treated as a directory.
	_, err = zipWriter.Create(appName + "/")
	require.NoError(t, err)
	dummyFileWriter, err := zipWriter.Create(filepath.Join(appName, "dummy.py")) // Zip paths should use forward slashes
	require.NoError(t, err)
	_, err = dummyFileWriter.Write([]byte("# dummy python file"))
	require.NoError(t, err)

	require.NoError(t, zipWriter.Close())
	return buf.Bytes()
}

func TestGetAppCommand(t *testing.T) {
	testAppName := "mygetapp"
	testAppVersion := "1.0.0"
	testAppOrg := "testorgg"
	testRepoName := "mockgetrepo"
	fpmFileName := fmt.Sprintf("%s-%s.fpm", testAppName, testAppVersion)

	dummyFPMContent := createDummyFPMBytes(t, testAppOrg, testAppName, testAppVersion)

	// Mock Server Setup
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Logf("Mock server for get-app received request: %s %s", r.Method, r.URL.Path)
		// Using ToSlash for consistency as zip/URL paths use forward slashes.
		expectedMetadataPath := filepath.ToSlash(filepath.Join("/metadata", testAppOrg, testAppName, "package-metadata.json"))
		// FPMPath in metadata is relative to repo base, so join it with /
		expectedFPMPathOnServer := filepath.ToSlash(filepath.Join("/", testAppOrg, testAppName, testAppVersion, fpmFileName))


		if r.URL.Path == expectedMetadataPath {
			repoMeta := repository.PackageMetadata{
				Org:           testAppOrg,
				AppName:       testAppName,
				LatestVersion: testAppVersion,
				Versions: map[string]repository.PackageVersionMetadata{
					testAppVersion: {
						// FPMPath stored in metadata is relative to the repo root
						FPMPath:        strings.TrimPrefix(expectedFPMPathOnServer, "/"),
						ChecksumSHA256: "dummychecksum", // TODO: generate real checksum for dummyFPMContent
					},
				},
			}
			metaBytes, _ := json.Marshal(repoMeta)
			w.Header().Set("Content-Type", "application/json")
			w.Write(metaBytes)
		} else if r.URL.Path == expectedFPMPathOnServer {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write(dummyFPMContent)
		} else {
			t.Errorf("Mock server received unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	// FPM Config and Environment Setup
	// setupTempFPMConfig sets up a temporary FPM home directory and returns its path.
	// The FPM config file (and thus default AppsBasePath) will be within this temp home.
	tempFPMHome, cleanup := setupTempFPMConfig(t)
	defer cleanup()

	// Load the config from the temporary FPM home to get the correct AppsBasePath
	cfg, err := config.LoadConfig(filepath.Join(tempFPMHome, ".fpm", "config.json"))
	require.NoError(t, err)
	mockAppsBasePath := cfg.AppsBasePath
	require.NotEmpty(t, mockAppsBasePath, "AppsBasePath should be set by default config in temp home")

	// Ensure the mockAppsBasePath (e.g. tempHome/.fpm/apps) actually exists for the test
	err = os.MkdirAll(mockAppsBasePath, 0o755)
	require.NoError(t, err)


	// Add the mock server as a repository using fpm repo add
	// executeCommand runs the rootCmd, so flags should be reset if necessary,
	// but repoAddCmd flags are typically handled by its own init or are not persistent across calls.
	// resetRepoCmdFlags() // if repo add flags were persistent and modified package-level vars.
	_, err = executeCommand(rootCmd, "repo", "add", testRepoName, server.URL)
	require.NoError(t, err)

	// --- Test Case 1: Get Specific Version ---
	t.Run("GetSpecificVersion", func(t *testing.T) {
		identifier := fmt.Sprintf("%s/%s/%s:%s", testRepoName, testAppOrg, testAppName, testAppVersion)
		output, errCmd := executeCommand(rootCmd, "get-app", identifier)
		t.Log("get-app output (specific version):\n", output)
		require.NoError(t, errCmd)
		assert.Contains(t, output, fmt.Sprintf("App %s/%s version %s successfully fetched from %s and installed to local FPM app store.", testAppOrg, testAppName, testAppVersion, testRepoName))

		expectedStorePath := filepath.Join(mockAppsBasePath, testAppOrg, testAppName, testAppVersion)
		_, err = os.Stat(filepath.Join(expectedStorePath, testAppName, "dummy.py"))
		assert.NoError(t, err, "App content not found in local FPM app store")
		_, err = os.Stat(filepath.Join(expectedStorePath, "_"+fpmFileName))
		assert.NoError(t, err, "Original .fpm not found in local FPM app store")

		os.RemoveAll(filepath.Join(mockAppsBasePath, testAppOrg)) // Clean up org folder for next test
	})

	// --- Test Case 2: Get Latest Version ---
	t.Run("GetLatestVersion", func(t *testing.T) {
		identifier := fmt.Sprintf("%s/%s/%s", testRepoName, testAppOrg, testAppName) // No version
		output, errCmd := executeCommand(rootCmd, "get-app", identifier)
		t.Log("get-app output (latest version):\n", output)
		require.NoError(t, errCmd)
		assert.Contains(t, output, fmt.Sprintf("App %s/%s version %s successfully fetched from %s and installed to local FPM app store.", testAppOrg, testAppName, testAppVersion, testRepoName))

		expectedStorePath := filepath.Join(mockAppsBasePath, testAppOrg, testAppName, testAppVersion)
		_, err = os.Stat(filepath.Join(expectedStorePath, testAppName, "dummy.py"))
		assert.NoError(t, err, "App content not found for 'latest' version test")
		_, err = os.Stat(filepath.Join(expectedStorePath, "_"+fpmFileName))
		assert.NoError(t, err, "Original .fpm not found for 'latest' version test")
		os.RemoveAll(filepath.Join(mockAppsBasePath, testAppOrg)) // Clean up
	})

	// --- Test Case 3: Error - Repo Not Found ---
	t.Run("ErrorRepoNotFound", func(t *testing.T) {
		identifier := fmt.Sprintf("nonexistentrepo/%s/%s:%s", testAppOrg, testAppName, testAppVersion)
		_, errCmd := executeCommand(rootCmd, "get-app", identifier)
		require.Error(t, errCmd)
		assert.Contains(t, errCmd.Error(), "Repository 'nonexistentrepo' not configured")
	})

	// --- Test Case 4: Error - Package Not Found In Repo ---
	t.Run("ErrorPackageNotFoundInRepo", func(t *testing.T) {
		// Server serves 404 for metadata for this app
		identifier := fmt.Sprintf("%s/%s/nonexistentapp:%s", testRepoName, testAppOrg, testAppVersion)
		_, errCmd := executeCommand(rootCmd, "get-app", identifier)
		require.Error(t, errCmd)
		// This error comes from FindPackageInSpecificRepo -> FetchRemotePackageMetadata
		assert.Contains(t, errCmd.Error(), "package testorgg/nonexistentapp not found in repository mockgetrepo (metadata missing)")
	})

	// --- Test Case 5: Error - Invalid Identifier Format ---
	t.Run("ErrorInvalidIdentifierFormat", func(t *testing.T) {
		invalidIdentifiers := []string{
			"invalid",                           // Too few parts
			"repoonly/",                         // Repo name but no package part
			"repo/orgonly",                      // Missing appName part
			"/org/app",                          // Empty repo name
			"repo//app",                         // Empty org
			"repo/org/",                         // Empty appName
			"repo/org/app/ver/extra",            // Too many parts after repo
			"repo/org/:ver",                     // Empty appName with version
		}
		for _, identifier := range invalidIdentifiers {
			_, errCmd := executeCommand(rootCmd, "get-app", identifier)
			require.Errorf(t, errCmd, "Expected error for invalid identifier: %s", identifier)
			// The specific error message might vary slightly based on parsing stage.
			// Checking for "invalid" and "format" or "identifier" should be good.
			assert.True(t, strings.Contains(strings.ToLower(errCmd.Error()), "invalid") &&
				(strings.Contains(strings.ToLower(errCmd.Error()), "format") || strings.Contains(strings.ToLower(errCmd.Error()), "identifier")),
				"Error message for '%s' should indicate format/identifier issue: %s", identifier, errCmd.Error())
		}
	})
}
