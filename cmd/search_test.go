package cmd

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	// "time" // Not strictly needed for search tests unless http client timeout is customized here

	"fpm/internal/config"
	"fpm/internal/metadata"      // For AppMetadata when creating dummy FPMs
	"fpm/internal/repository" // For PackageMetadata struct for cache & remote mock
	// "fpm/internal/utils" // Not directly used in this test file

	// "github.com/spf13/cobra" // Not directly manipulating commands here
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockPackageData represents data for setting up in cache or local store.
type MockPackageData struct {
	RepoName          string // For cache entries
	IsLocalStore      bool   // True if this should be in ~/.fpm/apps
	Org               string
	AppName           string
	Version           string
	Description       string
	LatestVersionHint string // For package-metadata.json if IsLocalStore is false
	// Add other fields if needed, like FPMPath for remote metadata
}

// setupTestEnvironment creates a temporary FPM config, local app store, and cache.
// It can populate these based on mockData.
func setupTestEnvironment(t *testing.T, mockData []MockPackageData) (tempHomeDir string, cleanupFunc func()) {
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

	cfg, err := config.InitConfig() // This creates default config file in tempHome/.fpm/
	require.NoError(t, err, "Failed to init config in temp search home dir")

	appsBaseDir := cfg.AppsBasePath // from config, likely tempHome/.fpm/apps
	cacheBaseDir := filepath.Join(filepath.Dir(appsBaseDir), "cache") // Sibling to apps dir

	require.NoError(t, os.MkdirAll(appsBaseDir, 0o755))
	require.NoError(t, os.MkdirAll(cacheBaseDir, 0o755))

	for _, data := range mockData {
		if data.IsLocalStore {
			// Populate local FPM app store: <appsBaseDir>/<org>/<appName>/<version>/
			appVersionStorePath := filepath.Join(appsBaseDir, data.Org, data.AppName, data.Version)
			require.NoError(t, os.MkdirAll(appVersionStorePath, 0o755))

			// Create a dummy _<appName>-<version>.fpm file
			fpmFileName := fmt.Sprintf("_%s-%s.fpm", data.AppName, data.Version)
			fpmFilePath := filepath.Join(appVersionStorePath, fpmFileName)

			archiveFile, err := os.Create(fpmFilePath)
			require.NoError(t, err)
			zipWriter := zip.NewWriter(archiveFile)

			appMeta := metadata.AppMetadata{
				Org:            data.Org,
				AppName:        data.AppName,
				PackageName:    data.AppName,
				PackageVersion: data.Version,
				Description:    data.Description,
			}
			metaBytes, _ := json.MarshalIndent(appMeta, "", "  ")
			fWriter, _ := zipWriter.Create("app_metadata.json")
			io.WriteString(fWriter, string(metaBytes))
			// Add a dummy file inside app module dir for realism
			appModuleDirEntry := fmt.Sprintf("%s/", data.AppName)
			zipWriter.CreateHeader(&zip.FileHeader{Name: appModuleDirEntry, Mode: 0o755 | os.ModeDir})
			fHook, _ := zipWriter.Create(filepath.Join(data.AppName, "hooks.py"))
			io.WriteString(fHook, "# hooks")

			zipWriter.Close()
			archiveFile.Close()
		} else { // Populate cache
			metadataFilePath := filepath.Join(cacheBaseDir, data.RepoName, "metadata", data.Org, data.AppName, "package-metadata.json")
			require.NoError(t, os.MkdirAll(filepath.Dir(metadataFilePath), 0o755))

			pkgMeta := repository.PackageMetadata{
				Org:           data.Org,     // Changed from GroupID
				AppName:       data.AppName, // Changed from ArtifactID
				Description:   data.Description,
				LatestVersion: data.LatestVersionHint,
				Versions: map[string]repository.PackageVersionMetadata{
					data.Version: {
						FPMPath: fmt.Sprintf("%s/%s/%s/%s-%s.fpm", data.Org, data.AppName, data.Version, data.AppName, data.Version),
						ChecksumSHA256: "dummychecksum",
					},
				},
			}
			if data.LatestVersionHint != "" && data.LatestVersionHint != data.Version {
				pkgMeta.Versions[data.LatestVersionHint] = repository.PackageVersionMetadata{
					FPMPath: fmt.Sprintf("%s/%s/%s/%s-%s.fpm", data.Org, data.AppName, data.LatestVersionHint, data.AppName, data.LatestVersionHint),
					ChecksumSHA256: "dummychecksumlatest",
				}
			}

			metaBytes, err := json.MarshalIndent(pkgMeta, "", "  ")
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(metadataFilePath, metaBytes, 0o644))
		}
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
	// Mock server for remote queries
	var serverRequests []string // To track requests to the server
	mockRepoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverRequests = append(serverRequests, r.URL.Path) // Log the request path
		t.Logf("Search Test Mock Repo Server: %s %s", r.Method, r.URL.Path)
		// Ensure path matching uses Org/AppName terminology if tests are updated accordingly
		if r.URL.Path == "/metadata/orgB/appY/package-metadata.json" { // Assuming query will use orgB/appY
			pkgMeta := repository.PackageMetadata{
				Org: "orgB", AppName: "appY", Description: "App Y from remote", LatestVersion: "1.1.0", // Changed fields
				Versions: map[string]repository.PackageVersionMetadata{
					"1.0.0": {FPMPath: "orgB/appY/1.0.0/appY-1.0.0.fpm"},
					"1.1.0": {FPMPath: "orgB/appY/1.1.0/appY-1.1.0.fpm"},
				},
			}
			json.NewEncoder(w).Encode(pkgMeta)
		} else if r.URL.Path == "/metadata/orgZ/appZ/package-metadata.json" { // Assuming query will use orgZ/appZ
			 pkgMeta := repository.PackageMetadata{
                Org: "orgZ", AppName: "appZ", Description: "App Z only on remote", LatestVersion: "3.0.0", // Changed fields
                Versions: map[string]repository.PackageVersionMetadata{
                    "3.0.0": {FPMPath: "orgZ/appZ/3.0.0/appZ-3.0.0.fpm"},
                },
            }
            json.NewEncoder(w).Encode(pkgMeta)
		} else {
			http.NotFound(w, r)
		}
	}))
	defer mockRepoServer.Close()

	mockData := []MockPackageData{
		// For TestSearch_OrderAndSources
		{IsLocalStore: true, Org: "orgA", AppName: "appX", Version: "1.0.0", Description: "App X installed locally"},
		{RepoName: "repo1", IsLocalStore: false, Org: "orgA", AppName: "appX", Version: "1.0.0", Description: "App X metadata in cache repo1", LatestVersionHint: "1.0.0"},
		// repo2 will serve orgA/appX via mockRepoServer if queried live. Let's make its metadata slightly different for remote.
		// For TestSearch_OrderAndSources (other packages)
		{RepoName: "repo1", IsLocalStore: false, Org: "orgC", AppName: "appCacheOnly", Version: "0.9.0", Description: "App C only in cache", LatestVersionHint: "0.9.0"},
		{IsLocalStore: true, Org: "orgD", AppName: "appLocalOnly", Version: "1.5.0", Description: "App D only in local store"},
	}

	tempHome, cleanup := setupTestEnvironment(t, mockData)
	defer cleanup()
	t.Logf("TestSearchCmd using temp home for ALL searches: %s", tempHome)

	// Add mockRepoServer as repo2 for live queries
	resetRepoCmdFlags() // from repo_test.go
	_, err := executeCommand(rootCmd, "repo", "add", "repo2", mockRepoServer.URL)
	require.NoError(t, err)


	t.Run("TestSearch_OrderAndSources", func(t *testing.T) {
		serverRequests = nil // Reset server request log
		output, err := executeCommand(rootCmd, "search", "orgA/appX")
		require.NoError(t, err)

		t.Logf("Output for TestSearch_OrderAndSources (orgA/appX):\n%s", output)
		// Expect orgA/appX==1.0.0 from (local-store) only, due to de-duplication.
		// The mock server for repo2 should serve orgA/appX if queried.
		mockRepoServer.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			serverRequests = append(serverRequests, r.URL.Path)
			if r.URL.Path == "/metadata/orgA/appX/package-metadata.json" {
				pkgMeta := repository.PackageMetadata{
					Org: "orgA", AppName: "appX", Description: "App X from remote repo2", LatestVersion: "1.0.0",
					Versions: map[string]repository.PackageVersionMetadata{"1.0.0": {FPMPath: "orgA/appX/1.0.0/appX-1.0.0.fpm"}},
				}
				json.NewEncoder(w).Encode(pkgMeta)
			} else { http.NotFound(w,r) }
		})


		output, err := executeCommand(rootCmd, "search", "orgA/appX") // Query for specific identifier
		require.NoError(t, err)
		t.Logf("Output for TestSearch_OrderAndSources (orgA/appX):\n%s", output)

		assert.Contains(t, output, "(local-store)         orgA/appX", "Should find orgA/appX from local store")
		assert.Contains(t, output, "1.0.0", "Version for local appX")
		assert.NotContains(t, output, "(cache: repo1)", "orgA/appX from cache should be overridden by local-store")
		assert.NotContains(t, output, "(remote: repo2)", "orgA/appX from remote should be overridden by local-store")

		wasQueried := false
		for _, reqPath := range serverRequests {
			if reqPath == "/metadata/orgA/appX/package-metadata.json" { wasQueried = true; break }
		}
		assert.True(t, wasQueried, "Mock server (repo2) should have been queried for orgA/appX")

		// Search for all to see other distinct packages
		serverRequests = nil
		outputAll, errAll := executeCommand(rootCmd, "search") // No query, should not hit remote for specific apps
		require.NoError(t, errAll)
		t.Logf("Output for TestSearch_OrderAndSources (all):\n%s", outputAll)
		assert.Contains(t, outputAll, "(local-store)         orgA/appX")
		assert.Contains(t, outputAll, "(cache: repo1)          orgC/appCacheOnly") // Using Org field from mockData
		assert.Contains(t, outputAll, "(local-store)         orgD/appLocalOnly") // Using Org field

		wasQueriedAll := false
        for _, reqPath := range serverRequests {
            if strings.HasPrefix(reqPath, "/metadata/orgA/appX") || strings.HasPrefix(reqPath, "/metadata/orgC/appCacheOnly") || strings.HasPrefix(reqPath, "/metadata/orgD/appLocalOnly") {
                wasQueriedAll = true; break
            }
        }
        assert.False(t, wasQueriedAll, "Generic search should not trigger specific live remote queries")
	})

	t.Run("TestSearch_RemoteQueryOnlyWhenIdentifierIsSpecific", func(t *testing.T) {
		serverRequests = nil
		// Setup mock server for orgZ/appZ for this test
		originalHandler := mockRepoServer.Config.Handler
		mockRepoServer.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			serverRequests = append(serverRequests, r.URL.Path)
			if r.URL.Path == "/metadata/orgZ/appZ/package-metadata.json" {
				pkgMeta := repository.PackageMetadata{ Org: "orgZ", AppName: "appZ", Description: "App Z only on remote", LatestVersion: "3.0.0",
					Versions: map[string]repository.PackageVersionMetadata{"3.0.0": {FPMPath: "orgZ/appZ/3.0.0/appZ-3.0.0.fpm"}},
				}
				json.NewEncoder(w).Encode(pkgMeta)
			} else { originalHandler.ServeHTTP(w,r) } // Fallback to original handler for other paths if needed
		})


		output, err := executeCommand(rootCmd, "search", "orgZ/appZ")
		require.NoError(t, err)
		t.Logf("Output for TestSearch_RemoteQueryOnlyWhenIdentifierIsSpecific (orgZ/appZ):\n%s", output)
		assert.Contains(t, output, "(remote: repo2)         orgZ/appZ")
		assert.Contains(t, output, "3.0.0", "Version for remote appZ")

		wasQueried := false
		for _, reqPath := range serverRequests { if reqPath == "/metadata/orgZ/appZ/package-metadata.json" { wasQueried = true; break } }
		assert.True(t, wasQueried, "Mock server should have been queried for specific orgZ/appZ")

		serverRequests = nil
		outputGeneric, errGeneric := executeCommand(rootCmd, "search", "appZ")
		require.NoError(t, errGeneric)
		t.Logf("Output for TestSearch_RemoteQueryOnlyWhenIdentifierIsSpecific (appZ generic):\n%s", outputGeneric)

		wasQueriedGeneric := false
		for _, reqPath := range serverRequests { if reqPath == "/metadata/orgZ/appZ/package-metadata.json" { wasQueriedGeneric = true; break } }
		assert.False(t, wasQueriedGeneric, "Mock server should NOT have been queried for orgZ/appZ on a generic 'appZ' search term")

		if strings.Contains(outputGeneric, "orgZ/appZ") { // If found, it must be from cache (populated by previous specific query)
			assert.Contains(t, outputGeneric, "(cache: repo2)          orgZ/appZ")
		}
		mockRepoServer.Config.Handler = originalHandler // Restore original handler
	})

	t.Run("TestSearch_MultipleRemoteVersionsFromLiveQuery", func(t *testing.T) {
		serverRequests = nil
		originalHandler := mockRepoServer.Config.Handler
		mockRepoServer.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			serverRequests = append(serverRequests, r.URL.Path)
			if r.URL.Path == "/metadata/orgB/appY/package-metadata.json" {
				pkgMeta := repository.PackageMetadata{ Org: "orgB", AppName: "appY", Description: "App Y from remote", LatestVersion: "1.1.0",
					Versions: map[string]repository.PackageVersionMetadata{
						"1.0.0": {FPMPath: "orgB/appY/1.0.0/appY-1.0.0.fpm"},
						"1.1.0": {FPMPath: "orgB/appY/1.1.0/appY-1.1.0.fpm"},
					},
				}
				json.NewEncoder(w).Encode(pkgMeta)
			} else { originalHandler.ServeHTTP(w,r) }
		})

		output, err := executeCommand(rootCmd, "search", "orgB/appY")
		require.NoError(t, err)
		t.Logf("Output for TestSearch_MultipleRemoteVersionsFromLiveQuery (orgB/appY):\n%s", output)
		assert.Contains(t, output, "(remote: repo2)         orgB/appY")
		assert.Contains(t, output, "1.0.0")
		assert.Contains(t, output, "1.1.0")

		wasQueried := false
		for _, reqPath := range serverRequests { if reqPath == "/metadata/orgB/appY/package-metadata.json" { wasQueried = true; break } }
		assert.True(t, wasQueried, "Mock server should have been queried for orgB/appY")
		mockRepoServer.Config.Handler = originalHandler // Restore
	})
}

// executeCommand helper is expected from repo_test.go (same package)
// setupTempFPMConfig helper is expected from repo_test.go (same package)
// createMinimalFrappeApp helper is from install_test.go (same package)
// resetRepoCmdFlags is defined in repo_test.go (same package)
// Note: Ensure these helpers are correctly accessible and that global state (like rootCmd flags)
// is properly managed between test runs if `executeCommand` uses the global `rootCmd`.
// The `setupTempFPMConfig` re-initializes config, which is good.
// Cobra commands often need their flags reset if the command object is reused.
// The `executeCommand` in `repo_test.go` uses `rootCmd.Execute()`.
// It's generally safer if `executeCommand` took `NewRootCmd()` or if `rootCmd` was reset.
// For now, assuming `rootCmd.SetArgs` and subsequent `Execute` handle flag parsing correctly for each call.
// The `resetRepoCmdFlags` in `install_test.go` was a placeholder.
// A proper `resetRootCmdFlags` or per-command flag reset might be needed if tests interfere.
// For `searchCmd`, it has no flags itself, so less risk.
// For `repo add` calls within test setup, `repoAddCmd` flags are reset in `repo_test.go`.
// Let's assume the `executeCommand` from `repo_test.go` is sufficient.
// The `resetRepoCmdFlags` in `install_test.go` was a placeholder.
// If `repo_test.go` `executeCommand` is `func executeCommand(root *cobra.Command, args ...string) (string, error)`
// then it's fine. The `runFPMCommand` in `install_test.go` is different.
// For consistency, I'll assume `executeCommand` from `repo_test.go` is the standard one being used.

// Adding a local definition for resetRepoCmdFlags to ensure it's available.
// This should ideally be in a shared test helper file.
func resetRepoCmdFlags() {
	if repoAddCmd != nil && repoAddCmd.Flags() != nil { // repoAddCmd is defined in repo.go
		repoAddCmd.Flags().VisitAll(func(f *cobra.Flag) {
			f.Value.Set(f.DefValue)
			f.Changed = false
		})
	}
	// Reset other repo subcommands flags if they exist and have persistent flags
}
