package cmd

import (
	// "bytes" // Not directly used, executeCommand captures output
	"archive/zip" // For creating dummy FPM for mock server
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync" // For safely accessing receivedTestData
	"testing"
	"time"

	"fpm/internal/config"
	"fpm/internal/metadata"
	"fpm/internal/repository"
	"fpm/internal/utils"

	// "github.com/spf13/cobra" // Not directly manipulating commands here, using executeCommand
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createDummyFPMForPublishing creates a valid .fpm file with new package structure.
func createDummyFPMForPublishing(t *testing.T, targetDir, appOrg, appName, appVersion string, contentChecksum string) string {
	t.Helper()
	fpmFileName := fmt.Sprintf("%s-%s.fpm", appName, appVersion)
	fpmFilePath := filepath.Join(targetDir, fpmFileName)

	archiveFile, err := os.Create(fpmFilePath)
	require.NoError(t, err)
	defer archiveFile.Close()

	zipWriter := zip.NewWriter(archiveFile)

	// Add app_metadata.json
	appMeta := metadata.AppMetadata{
		Org:             appOrg,
		AppName:         appName,
		PackageName:     appName,
		PackageVersion:  appVersion,
		ContentChecksum: contentChecksum, // Use provided checksum
		Description:     "A test app for publishing",
	}
	metaBytes, err := json.MarshalIndent(appMeta, "", "  ")
	require.NoError(t, err)
	fWriter, err := zipWriter.Create("app_metadata.json")
	require.NoError(t, err)
	_, err = fWriter.Write(metaBytes)
	require.NoError(t, err)

	// Add app module directory and a file in it
	appModuleDirEntry := fmt.Sprintf("%s/", appName)
	_, err = zipWriter.CreateHeader(&zip.FileHeader{Name: appModuleDirEntry, Mode: 0o755 | os.ModeDir})
	require.NoError(t, err)

	hooksContent := fmt.Sprintf("app_name = \"%s\"\n", appName)
	fWriterHooks, err := zipWriter.Create(filepath.Join(appName, "hooks.py"))
	require.NoError(t, err)
	_, err = io.WriteString(fWriterHooks, hooksContent)
	require.NoError(t, err)

	require.NoError(t, zipWriter.Close())
	return fpmFilePath
}

func TestPublishCommand(t *testing.T) {
	// --- Common Setup ---
	tempHome, baseCleanup := setupTempFPMConfig(t)
	defer baseCleanup()
	t.Logf("TestPublishCommand using temp home: %s", tempHome)

	cfg, err := config.LoadConfig()
	require.NoError(t, err)
	mockAppsBasePath := cfg.AppsBasePath
	require.NoError(t, os.MkdirAll(mockAppsBasePath, 0o755))

	// Mock Server state
	var receivedFPMs sync.Map // Store path -> content
	var receivedMetadata sync.Map // Store path -> repository.PackageMetadata

	mockRepoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Logf("Publish Test Mock Repo Server: %s %s", r.Method, r.URL.Path)
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "package-metadata.json") {
			data, found := receivedMetadata.Load(r.URL.Path)
			if found {
				w.Header().Set("Content-Type", "application/json")
				w.Write(data.([]byte)) // Serve stored metadata
			} else {
				http.NotFound(w, r)
			}
		} else if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, ".fpm") {
			bodyBytes, _ := io.ReadAll(r.Body)
			receivedFPMs.Store(r.URL.Path, bodyBytes) // Store FPM content
			w.WriteHeader(http.StatusCreated)
		} else if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "package-metadata.json") {
			bodyBytes, _ := io.ReadAll(r.Body)
			receivedMetadata.Store(r.URL.Path, bodyBytes) // Store metadata content
			w.WriteHeader(http.StatusCreated)
		} else {
			http.Error(w, "Unsupported request", http.StatusMethodNotAllowed)
		}
	}))
	defer mockRepoServer.Close()

	// Add mock repo to config
	resetRepoCmdFlags()
	_, err = executeCommand(rootCmd, "repo", "add", "mockpublishrepo", mockRepoServer.URL)
	require.NoError(t, err)

	// --- Test Cases ---
	testOrg := "publishtestorg"
	testAppName := "pubapp"
	testAppVersion := "1.0.0"

	t.Run("PublishFromFile", func(t *testing.T) {
		// Reset publish flags for this sub-test
		publishRepoName = ""
		publishFromFile = ""
		if publishCmd.Flags().Lookup("repo") != nil {
			publishCmd.Flags().Lookup("repo").Value.Set("") // Reset to empty or default
		}
		if publishCmd.Flags().Lookup("from-file") != nil {
			publishCmd.Flags().Lookup("from-file").Value.Set("") // Reset to empty or default
		}


		tempPackageDir, _ := os.MkdirTemp("", "fpm-publish-fromfile-*")
		defer os.RemoveAll(tempPackageDir)

		// Create a dummy .fpm file to publish
		dummyFPMPath := createDummyFPMForPublishing(t, tempPackageDir, testOrg, testAppName, testAppVersion, "dummychecksum1")

		// Clear any previously received data for this specific package on the mock server
		expectedFpmServerPath := fmt.Sprintf("/%s/%s/%s/%s-%s.fpm", testOrg, testAppName, testAppVersion, testAppName, testAppVersion)
		expectedMetadataServerPath := fmt.Sprintf("/metadata/%s/%s/package-metadata.json", testOrg, testAppName)
		receivedFPMs.Delete(expectedFpmServerPath)
		receivedMetadata.Delete(expectedMetadataServerPath)


		args := []string{"publish", "--from-file", dummyFPMPath, "--repo", "mockpublishrepo"}
		output, err := executeCommand(rootCmd, args...)
		t.Logf("PublishFromFile output: %s", output)
		require.NoError(t, err)
		assert.Contains(t, output, "Successfully published package")

		// Verify FPM file uploaded
		fpmData, found := receivedFPMs.Load(expectedFpmServerPath)
		assert.True(t, found, ".fpm file not uploaded to mock server")
		if found {
			localFpmBytes, _ := os.ReadFile(dummyFPMPath)
			assert.Equal(t, localFpmBytes, fpmData.([]byte), "Uploaded .fpm content mismatch")
		}

		// Verify metadata uploaded
		metaDataBytes, found := receivedMetadata.Load(expectedMetadataServerPath)
		assert.True(t, found, "package-metadata.json not uploaded")
		if found {
			var remoteMeta repository.PackageMetadata
			require.NoError(t, json.Unmarshal(metaDataBytes.([]byte), &remoteMeta))
			assert.Equal(t, testOrg, remoteMeta.Org) // Changed GroupID to Org
			assert.Equal(t, testAppName, remoteMeta.AppName) // Changed ArtifactID to AppName
			assert.Equal(t, testAppVersion, remoteMeta.LatestVersion)
			versionInfo, ok := remoteMeta.Versions[testAppVersion]
			require.True(t, ok, "Version info not found in remote metadata")
			assert.Equal(t, strings.TrimPrefix(expectedFpmServerPath, "/"), versionInfo.FPMPath) // FPMPath is stored relative

			expectedChecksum, _ := utils.CalculateFileChecksum(dummyFPMPath)
			assert.Equal(t, expectedChecksum, versionInfo.ChecksumSHA256)
		}
	})

	t.Run("PublishFromLocalStoreByIdentifier", func(t *testing.T) {
		// 1. Package and install to local store first
		sourceAppDir, _ := os.MkdirTemp("", "sourceapp-publocal-*")
		defer os.RemoveAll(sourceAppDir)
		createMinimalFrappeApp(t, sourceAppDir, testAppName, testAppVersion, testOrg) // Using helper from install_test

		packageOutputDir, _ := os.MkdirTemp("", "fpmoutput-publocal-*")
		defer os.RemoveAll(packageOutputDir)

		resetPackageCmdFlags()
		packageSkipLocalInstall = false // Ensure it installs locally
		_, err := executeCommand(rootCmd, "package", sourceAppDir, "--output-path", packageOutputDir, "--version", testAppVersion, "--org", testOrg, "--app-name", testAppName)
		require.NoError(t, err)

		// Clear server state for this app
		expectedFpmServerPath := fmt.Sprintf("/%s/%s/%s/%s-%s.fpm", testOrg, testAppName, testAppVersion, testAppName, testAppVersion)
		expectedMetadataServerPath := fmt.Sprintf("/metadata/%s/%s/package-metadata.json", testOrg, testAppName)
		receivedFPMs.Delete(expectedFpmServerPath)
		receivedMetadata.Delete(expectedMetadataServerPath)


		// 2. Publish from local store
		// Reset publish flags
		publishRepoName = ""
		publishFromFile = ""
		if publishCmd.Flags().Lookup("repo") != nil {
			publishCmd.Flags().Lookup("repo").Value.Set("")
		}
		if publishCmd.Flags().Lookup("from-file") != nil {
			publishCmd.Flags().Lookup("from-file").Value.Set("")
		}

		args := []string{"publish", fmt.Sprintf("%s/%s==%s", testOrg, testAppName, testAppVersion), "--repo", "mockpublishrepo"}
		output, err := executeCommand(rootCmd, args...)
		t.Logf("PublishFromLocalStore output: %s", output)
		require.NoError(t, err)
		assert.Contains(t, output, "Successfully published package")

		// Verify FPM file and metadata on server (similar assertions as PublishFromFile)
		_, found := receivedFPMs.Load(expectedFpmServerPath)
		assert.True(t, found, ".fpm file not uploaded from local store")

		metaDataBytes, found := receivedMetadata.Load(expectedMetadataServerPath)
		assert.True(t, found, "package-metadata.json not uploaded from local store publish")
		if found {
			var remoteMeta repository.PackageMetadata
			json.Unmarshal(metaDataBytes.([]byte), &remoteMeta)
			assert.Equal(t, testAppVersion, remoteMeta.Versions[testAppVersion].FPMPath.Split('/')[2])
		}
	})

	t.Run("PublishToDefaultRepo", func(t *testing.T) {
		tempPackageDir, _ := os.MkdirTemp("", "fpm-publish-default-*")
		defer os.RemoveAll(tempPackageDir)
		dummyFPMPath := createDummyFPMForPublishing(t, tempPackageDir, testOrg, "defaultrepoapp", "1.0.0", "dummychecksum_default")

		// Set default repo
		resetRepoCmdFlags()
		_, err := executeCommand(rootCmd, "repo", "default", "mockpublishrepo")
		require.NoError(t, err)

		// Publish without --repo flag
		// Reset publish flags
		publishRepoName = ""
		publishFromFile = ""
		if publishCmd.Flags().Lookup("repo") != nil {
			publishCmd.Flags().Lookup("repo").Value.Set("")
		}
		if publishCmd.Flags().Lookup("from-file") != nil {
			publishCmd.Flags().Lookup("from-file").Value.Set("")
		}
		args := []string{"publish", "--from-file", dummyFPMPath}
		output, err := executeCommand(rootCmd, args...)
		t.Logf("PublishToDefaultRepo output: %s", output)
		require.NoError(t, err)
		assert.Contains(t, output, "Successfully published package")
		assert.Contains(t, output, "Publishing to repository: mockpublishrepo")
	})

	t.Run("Error_VersionExistsOnRemote", func(t *testing.T) {
		// Setup: Ensure version 1.0.0 of testOrg/pubapp already exists in remoteMeta
		existingMeta := repository.PackageMetadata{
			Org: testOrg, AppName: testAppName, LatestVersion: testAppVersion, // Changed fields
			Versions: map[string]repository.PackageVersionMetadata{
				testAppVersion: {FPMPath: "path", ChecksumSHA256: "abc"},
			},
		}
		metaBytes, _ := json.Marshal(existingMeta)
		receivedMetadata.Store(fmt.Sprintf("/metadata/%s/%s/package-metadata.json", testOrg, testAppName), metaBytes)

		tempPackageDir, _ := os.MkdirTemp("", "fpm-publish-exists-*")
		defer os.RemoveAll(tempPackageDir)
		dummyFPMPath := createDummyFPMForPublishing(t, tempPackageDir, testOrg, testAppName, testAppVersion, "dummychecksum_exists")

		// Reset publish flags
		publishRepoName = ""
		publishFromFile = ""
		if publishCmd.Flags().Lookup("repo") != nil {
			publishCmd.Flags().Lookup("repo").Value.Set("")
		}
		if publishCmd.Flags().Lookup("from-file") != nil {
			publishCmd.Flags().Lookup("from-file").Value.Set("")
		}
		args := []string{"publish", "--from-file", dummyFPMPath, "--repo", "mockpublishrepo"}
		output, err := executeCommand(rootCmd, args...)
		t.Logf("Error_VersionExistsOnRemote output: %s", output)
		assert.Error(t, err)
		if err != nil {
			assert.Contains(t, err.Error(), "already exists in repository")
		}
	})
}

// Note: createMinimalFrappeApp is defined in install_test.go
// setupTempFPMConfig & executeCommand are defined in repo_test.go
// They are accessible as all these test files are in 'package cmd'.
// resetPackageCmdFlags is in package_test.go
// resetRepoCmdFlags is in install_test.go (placeholder) - ensure it's defined or remove call if not needed.
// For TestPublishCommand, if executeCommand is used on `rootCmd`, flags of subcommands like `publishCmd`
// need to be reset if they are package-level variables or stateful.
// publishRepoName, publishFromFile are package-level, so they need reset before each `executeCommand`
// that might use them.
// The `executeCommand` helper itself does not reset flags of subcommands.
// It's better to reset flags specific to the command being tested if they are global.
// For publishCmd, its flags publishRepoName and publishFromFile are package-level vars.
// So, they should be reset at the start of each sub-test in TestPublishCommand.

func TestMain(m *testing.M) {
	// This TestMain is for the cmd package.
	// It can be used to set up shared resources or ensure cleanup.
	// For example, resetting rootCmd if it's stateful across tests.
	// However, direct manipulation of rootCmd flags between tests is often preferred.
	// For now, individual tests manage their state (e.g. via setupTempFPMConfig).
	os.Exit(m.Run())
}
