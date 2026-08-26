package cmd

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"fpm/internal/config"
	"fpm/internal/metadata"
	"fpm/internal/repository"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func existsStoreApp(t *testing.T, base, org, app, version string, meta metadata.AppMetadata) {
	t.Helper()
	dir := filepath.Join(base, org, app, version)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, app), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, app, "hooks.py"), []byte("app_name = \""+app+"\"\n"), 0o644))
	meta.Org, meta.AppName, meta.PackageVersion = org, app, version
	require.NoError(t, metadata.SaveAppMetadata(dir, &meta))
}

func TestLookupExistsLocalStore(t *testing.T) {
	base := t.TempDir()
	cfg := &config.FPMConfig{AppsBasePath: base}
	existsStoreApp(t, base, "acme", "custom", "1.0.0", metadata.AppMetadata{
		CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", WheelPlatform: "manylinux2014_x86_64", WheelPythonVersion: "3.11"})
	existsStoreApp(t, base, "acme", "custom", "1.1.0", metadata.AppMetadata{
		CommitSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", WheelPlatform: "manylinux2014_x86_64,manylinux_2_28_x86_64", WheelPythonVersion: "3.11"})

	t.Run("exact version", func(t *testing.T) {
		r := lookupExists(cfg, "acme", "custom", "1.0.0", existsQuery{})
		assert.True(t, r.Exists)
		assert.Equal(t, "local-store", r.Source)
		assert.Equal(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", r.CommitSHA)
	})
	t.Run("any version picks newest", func(t *testing.T) {
		r := lookupExists(cfg, "acme", "custom", "", existsQuery{})
		assert.True(t, r.Exists)
		assert.Equal(t, "1.1.0", r.Version)
	})
	t.Run("commit prefix selects the version", func(t *testing.T) {
		r := lookupExists(cfg, "acme", "custom", "", existsQuery{commit: "AAAAAAA"})
		assert.True(t, r.Exists)
		assert.Equal(t, "1.0.0", r.Version)
	})
	t.Run("short commit prefix rejected", func(t *testing.T) {
		r := lookupExists(cfg, "acme", "custom", "", existsQuery{commit: "aaa"})
		assert.False(t, r.Exists)
	})
	t.Run("platform in multi-tag list", func(t *testing.T) {
		r := lookupExists(cfg, "acme", "custom", "", existsQuery{platform: "manylinux_2_28_x86_64"})
		assert.True(t, r.Exists)
		assert.Equal(t, "1.1.0", r.Version)
	})
	t.Run("platform mismatch lists candidates", func(t *testing.T) {
		r := lookupExists(cfg, "acme", "custom", "1.0.0", existsQuery{platform: "manylinux2014_aarch64"})
		assert.False(t, r.Exists)
		require.Len(t, r.Candidates, 1)
		assert.Contains(t, r.Candidates[0].Rejected, "manylinux2014_aarch64")
	})
	t.Run("python version mismatch", func(t *testing.T) {
		r := lookupExists(cfg, "acme", "custom", "1.1.0", existsQuery{python: "3.12"})
		assert.False(t, r.Exists)
	})
	t.Run("unknown app", func(t *testing.T) {
		r := lookupExists(cfg, "acme", "nothing", "", existsQuery{})
		assert.False(t, r.Exists)
		assert.Contains(t, r.Reason, "--remote")
	})
}

func TestLookupExistsRemoteMetadataOnly(t *testing.T) {
	downloads := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/metadata/acme/custom/package-metadata.json", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(repository.PackageMetadata{
			Org: "acme", AppName: "custom", LatestVersion: "2.0.0",
			Versions: map[string]repository.PackageVersionMetadata{
				"2.0.0": {FPMPath: "acme/custom/2.0.0/custom-2.0.0.fpm", CommitSHA: "cccccccccccccccccccccccccccccccccccccccc", WheelPlatform: "manylinux2014_x86_64"},
			},
		})
	})
	mux.HandleFunc("/acme/custom/", func(w http.ResponseWriter, r *http.Request) {
		downloads++
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := &config.FPMConfig{
		AppsBasePath: t.TempDir(),
		Repositories: map[string]config.RepositoryConfig{"r": {Name: "r", URL: srv.URL, Priority: 1}},
	}

	r := lookupExists(cfg, "acme", "custom", "", existsQuery{remote: true, commit: "ccccccc"})
	assert.True(t, r.Exists)
	assert.Equal(t, "repo:r", r.Source)
	assert.Equal(t, "2.0.0", r.Version)
	assert.Equal(t, 0, downloads, "the artifact must never be downloaded")

	r = lookupExists(cfg, "acme", "custom", "", existsQuery{remote: true, commit: "ddddddd"})
	assert.False(t, r.Exists)

	// Not remote: repositories untouched.
	r = lookupExists(cfg, "acme", "custom", "", existsQuery{})
	assert.False(t, r.Exists)
}

func TestExistsCommandExitCode(t *testing.T) {
	base := t.TempDir()
	t.Setenv("FPM_APPS_BASE_PATH", base)
	t.Setenv("HOME", t.TempDir())
	existsStoreApp(t, base, "acme", "custom", "1.0.0", metadata.AppMetadata{CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})

	existsCommit, existsPlatform, existsPythonVersion, existsRepo, existsRemote, existsJSON = "", "", "", "", false, false
	out, err := SharedExecuteCommand(rootCmd, "exists", "acme/custom==1.0.0", "--json")
	require.NoError(t, err)
	var res ExistsResult
	require.NoError(t, json.Unmarshal([]byte(out[lastBraceStart(out):]), &res), out)
	assert.True(t, res.Exists)

	existsCommit, existsPlatform, existsPythonVersion, existsRepo, existsRemote, existsJSON = "", "", "", "", false, false
	_, err = SharedExecuteCommand(rootCmd, "exists", "acme/custom==9.9.9")
	require.Error(t, err)
	assert.True(t, errors.Is(err, errNotFound))
	assert.Equal(t, ExitNotFound, ExitCodeFor(err))
}

// lastBraceStart finds the JSON object in captured output that may be preceded by
// config-initialisation chatter.
func lastBraceStart(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '{' {
			return i
		}
	}
	return 0
}
