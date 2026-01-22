package cmd

import (
	"archive/zip"
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

	"fpm/internal/config"
	"fpm/internal/metadata"
	"fpm/internal/repository"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type MockPackageData struct {
	RepoName          string
	IsLocalStore      bool
	Org               string
	AppName           string
	Version           string
	Description       string
	LatestVersionHint string
}

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

	cfg, err := config.InitConfig()
	require.NoError(t, err)

	appsBaseDir := cfg.AppsBasePath
	cacheBaseDir := filepath.Join(filepath.Dir(appsBaseDir), "cache")

	require.NoError(t, os.MkdirAll(appsBaseDir, 0o755))
	require.NoError(t, os.MkdirAll(cacheBaseDir, 0o755))

	for _, data := range mockData {
		if data.IsLocalStore {
			appVersionStorePath := filepath.Join(appsBaseDir, data.Org, data.AppName, data.Version)
			require.NoError(t, os.MkdirAll(appVersionStorePath, 0o755))

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
			
			appModuleDirEntry := fmt.Sprintf("%s/", data.AppName)
			header := &zip.FileHeader{Name: appModuleDirEntry}
			header.SetMode(0o755 | os.ModeDir)
			zipWriter.CreateHeader(header)
			
			fHook, _ := zipWriter.Create(filepath.Join(data.AppName, "hooks.py"))
			io.WriteString(fHook, "# hooks")

			zipWriter.Close()
			archiveFile.Close()
		} else {
			metadataFilePath := filepath.Join(cacheBaseDir, data.RepoName, "metadata", data.Org, data.AppName, "package-metadata.json")
			require.NoError(t, os.MkdirAll(filepath.Dir(metadataFilePath), 0o755))

			pkgMeta := repository.PackageMetadata{
				Org:           data.Org,
				AppName:       data.AppName,
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
	var serverRequests []string
	mockRepoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.ToLower(r.URL.Path)
		serverRequests = append(serverRequests, path)
		if path == "/metadata/orgb/appy/package-metadata.json" {
			pkgMeta := repository.PackageMetadata{
				Org: "orgB", AppName: "appY", Description: "App Y from remote", LatestVersion: "1.1.0",
				Versions: map[string]repository.PackageVersionMetadata{
					"1.0.0": {FPMPath: "orgB/appY/1.0.0/appY-1.0.0.fpm"},
					"1.1.0": {FPMPath: "orgB/appY/1.1.0/appY-1.1.0.fpm"},
				},
			}
			json.NewEncoder(w).Encode(pkgMeta)
		} else if path == "/metadata/orgz/appz/package-metadata.json" {
			 pkgMeta := repository.PackageMetadata{
                Org: "orgZ", AppName: "appZ", Description: "App Z only on remote", LatestVersion: "3.0.0",
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
		{IsLocalStore: true, Org: "orgA", AppName: "appX", Version: "1.0.0", Description: "App X installed locally"},
		{RepoName: "repo1", IsLocalStore: false, Org: "orgA", AppName: "appX", Version: "1.0.0", Description: "App X metadata in cache repo1", LatestVersionHint: "1.0.0"},
		{RepoName: "repo1", IsLocalStore: false, Org: "orgC", AppName: "appCacheOnly", Version: "0.9.0", Description: "App C only in cache", LatestVersionHint: "0.9.0"},
		{IsLocalStore: true, Org: "orgD", AppName: "appLocalOnly", Version: "1.5.0", Description: "App D only in local store"},
	}

	tempHome, cleanup := setupTestEnvironment(t, mockData)
	defer cleanup()
	_ = tempHome

	SharedResetRepoCmdFlags()
	_, err := SharedExecuteCommand(rootCmd, "repo", "add", "repo2", mockRepoServer.URL)
	require.NoError(t, err)

	t.Run("TestSearch_OrderAndSources", func(t *testing.T) {
		serverRequests = nil
		
		originalHandler := mockRepoServer.Config.Handler
		mockRepoServer.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := strings.ToLower(r.URL.Path)
			serverRequests = append(serverRequests, path)
			if path == "/metadata/orga/appx/package-metadata.json" {
				pkgMeta := repository.PackageMetadata{
					Org: "orgA", AppName: "appX", Description: "App X from remote repo2", LatestVersion: "1.0.0",
					Versions: map[string]repository.PackageVersionMetadata{"1.0.0": {FPMPath: "orgA/appX/1.0.0/appX-1.0.0.fpm"}},
				}
				json.NewEncoder(w).Encode(pkgMeta)
			} else { originalHandler.ServeHTTP(w,r) }
		})

		output, err := SharedExecuteCommand(rootCmd, "search", "orgA/appX")
		require.NoError(t, err)

		found := false
		for _, line := range strings.Split(output, "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == "(local-store)" && fields[1] == "orgA/appX" {
				found = true
				assert.Contains(t, line, "1.0.0")
				break
			}
		}
		assert.True(t, found, "Should find orgA/appX from local store. Output:\n%s", output)
		assert.NotContains(t, output, "(cache: repo1)")
		assert.NotContains(t, output, "(remote: repo2)")

		wasQueried := false
		for _, reqPath := range serverRequests {
			if reqPath == "/metadata/orga/appx/package-metadata.json" { wasQueried = true; break }
		}
		assert.True(t, wasQueried)

		serverRequests = nil
		outputAll, errAll := SharedExecuteCommand(rootCmd, "search")
		require.NoError(t, errAll)
		
		assert.Contains(t, outputAll, "(local-store)")
		assert.Contains(t, outputAll, "orgA/appX")
		assert.Contains(t, outputAll, "(cache: repo1)")
		assert.Contains(t, outputAll, "orgC/appCacheOnly")
		assert.Contains(t, outputAll, "orgD/appLocalOnly")

		wasQueriedAll := false
        for _, reqPath := range serverRequests {
            if strings.HasPrefix(reqPath, "/metadata/orga/appx") || strings.HasPrefix(reqPath, "/metadata/orgc/appcacheonly") || strings.HasPrefix(reqPath, "/metadata/orgd/applocalonly") {
                wasQueriedAll = true; break
            }
        }
        assert.False(t, wasQueriedAll)
		
		mockRepoServer.Config.Handler = originalHandler
	})

	t.Run("TestSearch_RemoteQueryOnlyWhenIdentifierIsSpecific", func(t *testing.T) {
		serverRequests = nil
		output, err := SharedExecuteCommand(rootCmd, "search", "orgZ/appZ")
		require.NoError(t, err)
		
		found := false
		for _, line := range strings.Split(output, "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == "(remote:" && fields[1] == "repo2)" && fields[2] == "orgZ/appZ" {
				found = true
				assert.Contains(t, line, "3.0.0")
				break
			}
		}
		assert.True(t, found, "Should find orgZ/appZ from remote repo2. Output:\n%s", output)

		wasQueried := false
		for _, reqPath := range serverRequests { if reqPath == "/metadata/orgz/appz/package-metadata.json" { wasQueried = true; break } }
		assert.True(t, wasQueried)

		serverRequests = nil
		_, errGeneric := SharedExecuteCommand(rootCmd, "search", "appZ")
		require.NoError(t, errGeneric)

		wasQueriedGeneric := false
		for _, reqPath := range serverRequests { if reqPath == "/metadata/orgz/appz/package-metadata.json" { wasQueriedGeneric = true; break } }
		assert.False(t, wasQueriedGeneric)
	})
}
