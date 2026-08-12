package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"fpm/internal/repository"

	"github.com/stretchr/testify/require"
)

// newIndexedRepo serves a repository index, and counts requests so a test can prove no
// network call happens without --remote.
func newIndexedRepo(t *testing.T, idx *repository.RepositoryIndex, hits *int32) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		if strings.HasSuffix(r.URL.Path, repository.IndexPath) {
			if idx == nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(idx)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestSearchRemoteUsesIndexForKeyword is the point of the index: matching a keyword
// against a repository, which per-package metadata alone cannot support.
func TestSearchRemoteUsesIndexForKeyword(t *testing.T) {
	_, cleanup := setupTempFPMConfig(t)
	defer cleanup()

	idx := &repository.RepositoryIndex{}
	idx.Upsert(repository.IndexEntry{Org: "acme", AppName: "inventory", LatestVersion: "2.1.0", Description: "Stock"})
	idx.Upsert(repository.IndexEntry{Org: "acme", AppName: "payroll", LatestVersion: "1.0.0", Description: "Salaries"})

	var hits int32
	srv := newIndexedRepo(t, idx, &hits)

	SharedResetRepoCmdFlags()
	_, err := SharedExecuteCommand(rootCmd, "repo", "add", "indexed", srv.URL)
	require.NoError(t, err)

	output, err := SharedExecuteCommand(rootCmd, "search", "inventory", "--remote")
	require.NoError(t, err)

	require.Contains(t, output, "acme/inventory")
	require.Contains(t, output, "2.1.0")
	// A keyword search must not return every package in the repository.
	require.NotContains(t, output, "acme/payroll")
}

// TestSearchWithoutRemoteMakesNoNetworkCall keeps `fpm search` local by default, so it
// never blocks on an unreachable repository the user did not ask to query.
func TestSearchWithoutRemoteMakesNoNetworkCall(t *testing.T) {
	_, cleanup := setupTempFPMConfig(t)
	defer cleanup()

	idx := &repository.RepositoryIndex{}
	idx.Upsert(repository.IndexEntry{Org: "acme", AppName: "inventory", LatestVersion: "2.1.0"})

	var hits int32
	srv := newIndexedRepo(t, idx, &hits)

	SharedResetRepoCmdFlags()
	_, err := SharedExecuteCommand(rootCmd, "repo", "add", "quiet", srv.URL)
	require.NoError(t, err)

	searchRemote = false
	output, err := SharedExecuteCommand(rootCmd, "search", "inventory")
	require.NoError(t, err)

	require.Zero(t, atomic.LoadInt32(&hits), "search without --remote must not contact a repository")
	require.NotContains(t, output, "acme/inventory")
}

// TestSearchRemoteFallsBackWithoutIndex covers a repository that publishes no index: an
// exact <org>/<app> is still resolvable, and a keyword search says why it cannot be.
func TestSearchRemoteFallsBackWithoutIndex(t *testing.T) {
	_, cleanup := setupTempFPMConfig(t)
	defer cleanup()

	pkgMeta := repository.PackageMetadata{
		Org: "acme", AppName: "legacy", LatestVersion: "1.0.0",
		Versions: map[string]repository.PackageVersionMetadata{
			"1.0.0": {FPMPath: "acme/legacy/1.0.0/legacy-1.0.0.fpm"},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, repository.IndexPath) {
			http.NotFound(w, r) // no index published
			return
		}
		if strings.HasSuffix(r.URL.Path, "package-metadata.json") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(pkgMeta)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	SharedResetRepoCmdFlags()
	_, err := SharedExecuteCommand(rootCmd, "repo", "add", "noindex", srv.URL)
	require.NoError(t, err)

	// Exact identifier still resolves via targeted lookup.
	output, err := SharedExecuteCommand(rootCmd, "search", "acme/legacy", "--remote")
	require.NoError(t, err)
	require.Contains(t, output, "acme/legacy")

	// A keyword cannot be matched without an index, and the reason is stated.
	searchRemote = false
	output, err = SharedExecuteCommand(rootCmd, "search", "legacy", "--remote")
	require.NoError(t, err)
	require.NotContains(t, output, "(remote: noindex)")
}
