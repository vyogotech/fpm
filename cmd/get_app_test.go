package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fpm/internal/config"
	"fpm/internal/repository"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetAppCommand(t *testing.T) {
	// --- Setup ---
	testRepoName := "mockgetrepo"
	testAppOrg := "testorgg"
	testAppName := "mygetapp"
	testAppVersion := "1.0.0"
	fpmFileName := fmt.Sprintf("%s-%s.fpm", testAppName, testAppVersion)

	expectedMetadataPath := fmt.Sprintf("/metadata/%s/%s/package-metadata.json", testAppOrg, testAppName)
	expectedFPMPath := fmt.Sprintf("/%s/%s/%s/%s", testAppOrg, testAppName, testAppVersion, fpmFileName)

	// Create dummy FPM package bytes
	fpmBytes := SharedCreateDummyFPMBytes(t, testAppOrg, testAppName, testAppVersion)

	// Mock PackageMetadata
	pkgMeta := repository.PackageMetadata{
		Org:           testAppOrg,
		AppName:       testAppName,
		LatestVersion: testAppVersion,
		Versions: map[string]repository.PackageVersionMetadata{
			testAppVersion: {
				FPMPath:        strings.TrimPrefix(expectedFPMPath, "/"),
				ChecksumSHA256: "dummychecksum",
			},
		},
	}
	pkgMetaBytes, _ := json.Marshal(pkgMeta)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		t.Logf("Mock server for get-app received request: %s %s", r.Method, path)
		if r.Method == "GET" {
			if path == expectedMetadataPath {
				w.Header().Set("Content-Type", "application/json")
				w.Write(pkgMetaBytes)
				return
			}
			if path == expectedFPMPath {
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Write(fpmBytes)
				return
			}
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	tempFPMHome, cleanup := setupTempFPMConfig(t)
	_ = tempFPMHome
	defer cleanup()

	cfg, err := config.LoadConfig()
	require.NoError(t, err)
	mockAppsBasePath := cfg.AppsBasePath
	require.NotEmpty(t, mockAppsBasePath)

	require.NoError(t, os.MkdirAll(mockAppsBasePath, 0o755))

	SharedResetRepoCmdFlags()
	_, err = SharedExecuteCommand(rootCmd, "repo", "add", testRepoName, server.URL)
	require.NoError(t, err)

	// --- Execution & Verification ---

	t.Run("GetSpecificVersion", func(t *testing.T) {
		identifier := fmt.Sprintf("%s/%s/%s:%s", testRepoName, testAppOrg, testAppName, testAppVersion)
		output, errCmd := SharedExecuteCommand(rootCmd, "get-app", identifier)
		require.NoError(t, errCmd)
		assert.Contains(t, strings.ToLower(output), "successfully fetched")
		assert.Contains(t, strings.ToLower(output), "installed to local fpm app store")

		expectedStorePath := filepath.Join(mockAppsBasePath, testAppOrg, testAppName, testAppVersion)
		_, err = os.Stat(expectedStorePath)
		assert.NoError(t, err, "App directory not found in local store")
	})

	t.Run("GetLatestVersion", func(t *testing.T) {
		identifier := fmt.Sprintf("%s/%s/%s", testRepoName, testAppOrg, testAppName)
		output, errCmd := SharedExecuteCommand(rootCmd, "get-app", identifier)
		require.NoError(t, errCmd)
		assert.Contains(t, strings.ToLower(output), "resolved 'latest'")
		assert.Contains(t, strings.ToLower(output), "successfully fetched")
	})

	t.Run("ErrorRepoNotFound", func(t *testing.T) {
		identifier := fmt.Sprintf("nonexistentrepo/%s/%s:%s", testAppOrg, testAppName, testAppVersion)
		_, errCmd := SharedExecuteCommand(rootCmd, "get-app", identifier)
		require.Error(t, errCmd)
		assert.Contains(t, strings.ToLower(errCmd.Error()), "not configured")
	})

	t.Run("ErrorPackageNotFoundInRepo", func(t *testing.T) {
		identifier := fmt.Sprintf("%s/%s/nonexistentapp:%s", testRepoName, testAppOrg, testAppVersion)
		_, errCmd := SharedExecuteCommand(rootCmd, "get-app", identifier)
		require.Error(t, errCmd)
		assert.Contains(t, strings.ToLower(errCmd.Error()), "not found")
	})

	t.Run("ErrorInvalidIdentifierFormat", func(t *testing.T) {
		invalidIdentifiers := []string{
			"invalid",
			"repoonly/",
			"/org/app",
			"repo//app",
		}
		for _, identifier := range invalidIdentifiers {
			_, errCmd := SharedExecuteCommand(rootCmd, "get-app", identifier)
			require.Error(t, errCmd)
			assert.True(t, strings.Contains(strings.ToLower(errCmd.Error()), "invalid") ||
				strings.Contains(strings.ToLower(errCmd.Error()), "format") ||
				strings.Contains(strings.ToLower(errCmd.Error()), "empty") ||
				strings.Contains(strings.ToLower(errCmd.Error()), "identifier"))
		}
	})
}
