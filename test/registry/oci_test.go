package registry_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"fpm/internal/config"
	"fpm/internal/metadata"
	"fpm/internal/ociregistry"
	"fpm/internal/repository"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockOCIRegistry is an in-process, hermetic OCI distribution v2 registry for integration testing.
type mockOCIRegistry struct {
	mu        sync.RWMutex
	blobs     map[string][]byte
	manifests map[string]map[string][]byte // repo -> tag/digest -> manifest bytes
	tags      map[string][]string          // repo -> tags list
}

func newMockOCIRegistry() *mockOCIRegistry {
	return &mockOCIRegistry{
		blobs:     make(map[string][]byte),
		manifests: make(map[string]map[string][]byte),
		tags:      make(map[string][]string),
	}
}

func (m *mockOCIRegistry) Server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(m.handleRequest))
}

func (m *mockOCIRegistry) handleRequest(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(r.URL.Path, "/")
	if path == "v2" || path == "" {
		w.WriteHeader(http.StatusOK)
		return
	}

	parts := strings.Split(path, "/")
	if len(parts) < 3 || parts[0] != "v2" {
		http.NotFound(w, r)
		return
	}

	// Route /v2/<repo...>/manifests/<ref>
	// Route /v2/<repo...>/blobs/<digest>
	// Route /v2/<repo...>/blobs/uploads/
	// Route /v2/<repo...>/tags/list
	// Route /v2/<repo...>/referrers/<digest>

	last := parts[len(parts)-1]
	secondLast := parts[len(parts)-2]

	if secondLast == "tags" && last == "list" {
		repo := strings.Join(parts[1:len(parts)-2], "/")
		m.mu.RLock()
		tagsList := m.tags[repo]
		m.mu.RUnlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name": repo,
			"tags": tagsList,
		})
		return
	}

	if secondLast == "manifests" {
		repo := strings.Join(parts[1:len(parts)-2], "/")
		ref := last

		switch r.Method {
		case http.MethodGet, http.MethodHead:
			m.mu.RLock()
			repoManifests := m.manifests[repo]
			var data []byte
			if repoManifests != nil {
				data = repoManifests[ref]
			}
			m.mu.RUnlock()

			if data == nil {
				http.NotFound(w, r)
				return
			}

			hasher := sha256.New()
			hasher.Write(data)
			digest := "sha256:" + hex.EncodeToString(hasher.Sum(nil))

			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			w.Header().Set("Docker-Content-Digest", digest)
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))

			if r.Method == http.MethodGet {
				_, _ = w.Write(data)
			}
			return

		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			hasher := sha256.New()
			hasher.Write(body)
			digest := "sha256:" + hex.EncodeToString(hasher.Sum(nil))

			m.mu.Lock()
			if m.manifests[repo] == nil {
				m.manifests[repo] = make(map[string][]byte)
			}
			m.manifests[repo][ref] = body
			m.manifests[repo][digest] = body

			// Add to tags if not digest
			if !strings.HasPrefix(ref, "sha256:") {
				exists := false
				for _, t := range m.tags[repo] {
					if t == ref {
						exists = true
						break
					}
				}
				if !exists {
					m.tags[repo] = append(m.tags[repo], ref)
				}
			}
			m.mu.Unlock()

			w.Header().Set("Docker-Content-Digest", digest)
			w.Header().Set("Location", fmt.Sprintf("/v2/%s/manifests/%s", repo, ref))
			w.WriteHeader(http.StatusCreated)
			return
		}
	}

	if secondLast == "blobs" && strings.HasPrefix(last, "sha256:") {
		digest := last
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			m.mu.RLock()
			data := m.blobs[digest]
			m.mu.RUnlock()

			if data == nil {
				http.NotFound(w, r)
				return
			}

			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Docker-Content-Digest", digest)
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))

			if r.Method == http.MethodGet {
				_, _ = w.Write(data)
			}
			return
		}
	}

	// Blob Uploads
	if secondLast == "blobs" && last == "uploads" && r.Method == http.MethodPost {
		uploadID := "upload-12345"
		repo := strings.Join(parts[1:len(parts)-2], "/")
		w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", repo, uploadID))
		w.WriteHeader(http.StatusAccepted)
		return
	}

	if len(parts) >= 4 && parts[len(parts)-3] == "blobs" && parts[len(parts)-2] == "uploads" {
		switch r.Method {
		case http.MethodPatch:
			w.WriteHeader(http.StatusAccepted)
			return

		case http.MethodPut:
			digest := r.URL.Query().Get("digest")
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			if digest == "" {
				hasher := sha256.New()
				hasher.Write(body)
				digest = "sha256:" + hex.EncodeToString(hasher.Sum(nil))
			}

			m.mu.Lock()
			m.blobs[digest] = body
			m.mu.Unlock()

			w.Header().Set("Docker-Content-Digest", digest)
			w.WriteHeader(http.StatusCreated)
			return
		}
	}

	http.NotFound(w, r)
}

func createTestFPMArchive(t *testing.T, org, appName, version string) (string, *metadata.AppMetadata) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, fmt.Sprintf("%s-%s.fpm", appName, version))

	appMeta := &metadata.AppMetadata{
		Org:                 org,
		AppName:             appName,
		PackageVersion:      version,
		PackageType:         "standalone",
		Description:         "Test Frappe App for OCI End-to-End Test",
		CommitSHA:           "feedbeef12345678",
		GitRef:              "refs/tags/v" + version,
		FrappeCompatibility: []string{"15"},
		Dependencies: map[string]string{
			"requests": ">=2.28.0",
		},
	}

	metaBytes, err := json.MarshalIndent(appMeta, "", "  ")
	require.NoError(t, err)

	contentChecksum := "content-sha256-dummy"
	appMeta.ContentChecksum = contentChecksum

	f, err := os.Create(filePath)
	require.NoError(t, err)
	defer f.Close()

	zw := zip.NewWriter(f)

	mw, err := zw.Create("app_metadata.json")
	require.NoError(t, err)
	_, err = mw.Write(metaBytes)
	require.NoError(t, err)

	fw, err := zw.Create(filepath.Join(appName, "hooks.py"))
	require.NoError(t, err)
	_, err = fw.Write([]byte(fmt.Sprintf("app_name = '%s'\napp_version = '%s'\n", appName, version)))
	require.NoError(t, err)

	require.NoError(t, zw.Close())
	return filePath, appMeta
}

func TestOCIRepositoryEndToEnd(t *testing.T) {
	mockRegistry := newMockOCIRegistry()
	server := mockRegistry.Server()
	defer server.Close()

	repoURL := strings.TrimPrefix(server.URL, "http://")
	repoConfig := config.RepositoryConfig{
		Name:      "test-oci",
		URL:       repoURL + "/fpm",
		Type:      "oci",
		PlainHTTP: true,
	}

	ctx := context.Background()

	// 1. Create fixture package
	org := "frappe"
	app := "wiki"
	version := "1.2.0"
	fpmPath, appMeta := createTestFPMArchive(t, org, app, version)

	rawBytes, err := os.ReadFile(fpmPath)
	require.NoError(t, err)
	hasher := sha256.New()
	hasher.Write(rawBytes)
	expectedChecksum := hex.EncodeToString(hasher.Sum(nil))

	// 2. Test Exists (should be false before push)
	exists, _, err := ociregistry.Exists(ctx, repoConfig, org, app, version)
	require.NoError(t, err)
	assert.False(t, exists, "Package should not exist before push")

	// 3. Test Push
	desc, err := ociregistry.Push(ctx, repoConfig, fpmPath, appMeta, []string{appMeta.CommitSHA})
	require.NoError(t, err)
	assert.NotEmpty(t, desc.Digest)

	// 4. Test Exists (should be true after push)
	exists, vMeta, err := ociregistry.Exists(ctx, repoConfig, org, app, version)
	require.NoError(t, err)
	assert.True(t, exists, "Package should exist after push")
	require.NotNil(t, vMeta)
	assert.Equal(t, expectedChecksum, vMeta.ChecksumSHA256)
	assert.Equal(t, "feedbeef12345678", vMeta.CommitSHA)

	// 5. Test FetchMetadata (lists tags and extracts version metadata)
	pkgMeta, found, err := ociregistry.FetchMetadata(ctx, repoConfig, org, app)
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, pkgMeta)
	assert.Equal(t, org, pkgMeta.Org)
	assert.Equal(t, app, pkgMeta.AppName)
	assert.Equal(t, version, pkgMeta.LatestVersion)
	assert.Contains(t, pkgMeta.Versions, version)

	// 6. Test Pull
	destDir := t.TempDir()
	destPath := filepath.Join(destDir, fmt.Sprintf("%s-%s.fpm", app, version))

	pulledMeta, err := ociregistry.Pull(ctx, repoConfig, org, app, version, destPath)
	require.NoError(t, err)
	require.NotNil(t, pulledMeta)
	assert.Equal(t, expectedChecksum, pulledMeta.ChecksumSHA256)

	pulledBytes, err := os.ReadFile(destPath)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(rawBytes, pulledBytes), "Pulled .fpm file bytes must match published file bit-for-bit")

	// 7. Test FindPackageInRepos integration via repository driver
	cfg := &config.FPMConfig{
		Repositories: map[string]config.RepositoryConfig{
			"test-oci": repoConfig,
		},
	}

	dlInfo, err := repository.FindPackageInRepos(cfg, org, app, version)
	require.NoError(t, err)
	require.NotNil(t, dlInfo)
	assert.Equal(t, "test-oci", dlInfo.RepositoryName)
	assert.Equal(t, version, dlInfo.Version)
	assert.Equal(t, expectedChecksum, dlInfo.Checksum)
}
