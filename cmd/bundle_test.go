package cmd

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fpm/internal/config"
	"fpm/internal/metadata"
	"fpm/internal/resolver"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// storePackaged puts a package into the local store the way `fpm package` /
// `fpm install` do: extracted, with the original .fpm kept as _<app>-<version>.fpm.
func storePackaged(t *testing.T, base string, meta metadata.AppMetadata) string {
	t.Helper()
	dir := filepath.Join(base, meta.Org, meta.AppName, meta.PackageVersion)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, meta.AppName), 0o755))
	for _, f := range []string{"__init__.py", "hooks.py", "modules.txt"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, meta.AppName, f), []byte("app_name = \""+meta.AppName+"\"\n"), 0o644))
	}
	require.NoError(t, metadata.SaveAppMetadata(dir, &meta))
	fpm := filepath.Join(dir, "_"+meta.AppName+"-"+meta.PackageVersion+".fpm")
	require.NoError(t, os.WriteFile(fpm, dummyFPMWithMeta(t, meta, nil), 0o644))
	return fpm
}

// TestExportBundleClosure: hrms -> erpnext -> payments; the bundle holds all three,
// each once, deepest first, and the manifest says so.
func TestExportBundleClosure(t *testing.T) {
	store := t.TempDir()
	cfg := &config.FPMConfig{AppsBasePath: store}
	storePackaged(t, store, metadata.AppMetadata{Org: "frappe", AppName: "payments", PackageVersion: "1.0.0"})
	storePackaged(t, store, metadata.AppMetadata{Org: "frappe", AppName: "erpnext", PackageVersion: "16.0.0",
		RequiredApps: []metadata.RequiredApp{{Name: "payments", Org: "frappe", Version: "1.0.0"}}})
	hrms := storePackaged(t, store, metadata.AppMetadata{Org: "frappe", AppName: "hrms", PackageVersion: "16.0.0", CommitSHA: "abc",
		RequiredApps: []metadata.RequiredApp{{Name: "erpnext", Org: "frappe", Version: "16.0.0", Requirement: "frappe/erpnext"}}})

	out := filepath.Join(t.TempDir(), "hrms-bundle")
	m, err := exportBundle(hrms, out, cfg, false, "", "")
	require.NoError(t, err)

	ids := make([]string, 0, len(m.InstallOrder))
	for _, e := range m.InstallOrder {
		ids = append(ids, e.Identifier())
		assert.FileExists(t, filepath.Join(out, e.File))
	}
	assert.Equal(t, []string{"frappe/payments==1.0.0", "frappe/erpnext==16.0.0", "frappe/hrms==16.0.0"}, ids)
	assert.Equal(t, "frappe/hrms==16.0.0", m.Root.Identifier())
	assert.Equal(t, "abc", m.Root.CommitSHA)
	assert.Equal(t, "frappe/hrms==16.0.0", m.InstallOrder[1].RequiredBy)

	// The manifest on disk round-trips and marks the directory as a bundle.
	assert.True(t, isBundleDir(out))
	back, err := readBundleManifest(out)
	require.NoError(t, err)
	assert.Equal(t, m.InstallOrder, back.InstallOrder)
	data, _ := os.ReadFile(filepath.Join(out, BundleManifestName))
	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.Contains(t, raw, "install_order")
}

// TestExportBundleMissingDependency: without --remote a dependency absent from the
// store is a hard error naming it; nothing is written.
func TestExportBundleMissingDependency(t *testing.T) {
	store := t.TempDir()
	cfg := &config.FPMConfig{AppsBasePath: store}
	hrms := storePackaged(t, store, metadata.AppMetadata{Org: "frappe", AppName: "hrms", PackageVersion: "16.0.0",
		RequiredApps: []metadata.RequiredApp{{Name: "erpnext", Org: "frappe", Version: "16.0.0"}}})
	out := filepath.Join(t.TempDir(), "bundle")
	_, err := exportBundle(hrms, out, cfg, false, "", "")
	require.Error(t, err)
	assert.True(t, errors.Is(err, resolver.ErrMissing), "got %v", err)
	assert.Contains(t, err.Error(), "frappe/erpnext==16.0.0")
	_, statErr := os.Stat(out)
	assert.True(t, os.IsNotExist(statErr))
}

// TestInstallBundleInstallsInOrder: `fpm install <bundle-dir>` installs every package,
// dependencies first, so each step's required-apps check passes.
func TestInstallBundleInstallsInOrder(t *testing.T) {
	// Build the bundle from one store...
	src := t.TempDir()
	storePackaged(t, src, metadata.AppMetadata{Org: "frappe", AppName: "erpnext", PackageVersion: "16.0.0"})
	hrms := storePackaged(t, src, metadata.AppMetadata{Org: "frappe", AppName: "hrms", PackageVersion: "16.0.0",
		RequiredApps: []metadata.RequiredApp{{Name: "erpnext", Org: "frappe", Version: "16.0.0"}}})
	bundleDir := filepath.Join(t.TempDir(), "hrms-bundle")
	_, err := exportBundle(hrms, bundleDir, &config.FPMConfig{AppsBasePath: src}, false, "", "")
	require.NoError(t, err)

	// ...and install it into an empty store + bench, as the offline target would.
	target := t.TempDir()
	t.Setenv("FPM_APPS_BASE_PATH", target)
	t.Setenv("HOME", t.TempDir())
	bench := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(bench, "env", "bin"), 0o755))
	pipLog := filepath.Join(bench, "pip.log")
	require.NoError(t, os.WriteFile(filepath.Join(bench, "env", "bin", "pip"), []byte("#!/bin/sh\necho \"$@\" >> "+pipLog+"\nexit 0\n"), 0o755))

	resetInstallCmdFlags()
	out, err := SharedExecuteCommand(rootCmd, "install", bundleDir, "--bench-path", bench)
	require.NoError(t, err, out)
	assert.Contains(t, out, "[1/2] frappe/erpnext==16.0.0")
	assert.Contains(t, out, "[2/2] frappe/hrms==16.0.0")
	assert.Contains(t, out, "Required app frappe/erpnext==16.0.0 satisfied from local FPM store")

	appsTxt, err := os.ReadFile(filepath.Join(bench, "sites", "apps.txt"))
	require.NoError(t, err)
	assert.Equal(t, "erpnext\nhrms\n", string(appsTxt))
	pips, _ := os.ReadFile(pipLog)
	assert.Equal(t, 2, strings.Count(string(pips), "install "))
	for _, app := range []string{"erpnext", "hrms"} {
		_, err := os.Lstat(filepath.Join(bench, "apps", app))
		assert.NoError(t, err, app)
	}
}

func TestInstallBundleMissingFile(t *testing.T) {
	dir := t.TempDir()
	m := BundleManifest{InstallOrder: []BundleEntry{{Org: "a", App: "b", Version: "1", File: "b-1.fpm"}}}
	data, _ := json.Marshal(m)
	require.NoError(t, os.WriteFile(filepath.Join(dir, BundleManifestName), data, 0o644))
	t.Setenv("FPM_APPS_BASE_PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	resetInstallCmdFlags()
	_, err := SharedExecuteCommand(rootCmd, "install", dir, "--bench-path", t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "b-1.fpm")
}

// TestBundleWithBenchProvidedDependency: made against a bench that already has
// erpnext (an image), the bundle lists erpnext as provided by the bench and ships only
// hrms; installing it into such a bench skips erpnext, and into a bench without it fails.
func TestBundleWithBenchProvidedDependency(t *testing.T) {
	store := t.TempDir()
	hrms := storePackaged(t, store, metadata.AppMetadata{Org: "frappe", AppName: "hrms", PackageVersion: "17.0.0-dev",
		RequiredApps: []metadata.RequiredApp{{Name: "erpnext", Org: "frappe", Version: "17.0.0-dev", Requirement: "frappe/erpnext"}}})
	buildBench := t.TempDir()
	writeBenchApp(t, buildBench, "erpnext", "17.0.0-dev")

	out := filepath.Join(t.TempDir(), "hrms-bundle")
	m, err := exportBundle(hrms, out, &config.FPMConfig{AppsBasePath: store}, false, "", buildBench)
	require.NoError(t, err)
	require.Len(t, m.InstallOrder, 2)
	assert.Equal(t, "bench", m.InstallOrder[0].ProvidedBy)
	assert.Equal(t, "frappe/erpnext==17.0.0-dev", m.InstallOrder[0].Identifier())
	assert.Empty(t, m.InstallOrder[0].File)
	entries, _ := os.ReadDir(out)
	assert.Len(t, entries, 2, "only hrms-…fpm and the manifest are shipped")

	t.Setenv("FPM_APPS_BASE_PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	target := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(target, "env", "bin"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(target, "env", "bin", "pip"), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	writeBenchApp(t, target, "erpnext", "17.0.0-dev")

	resetInstallCmdFlags()
	outText, err := SharedExecuteCommand(rootCmd, "install", out, "--bench-path", target)
	require.NoError(t, err, outText)
	assert.Contains(t, outText, "provided by the bench, not reinstalled")
	assert.Contains(t, outText, "Required app frappe/erpnext==17.0.0-dev provided by the bench")
	appsTxt, _ := os.ReadFile(filepath.Join(target, "sites", "apps.txt"))
	assert.Equal(t, "hrms\n", string(appsTxt), "erpnext was not (re)installed")

	// A bench without erpnext cannot take this bundle.
	bare := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(bare, "env", "bin"), 0o755))
	resetInstallCmdFlags()
	_, err = SharedExecuteCommand(rootCmd, "install", out, "--bench-path", bare)
	require.Error(t, err)
	assert.True(t, errors.Is(err, resolver.ErrMissing), "got %v", err)
}

func writeBenchApp(t *testing.T, bench, name, version string) {
	t.Helper()
	module := filepath.Join(bench, "apps", name, name)
	require.NoError(t, os.MkdirAll(module, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(module, "hooks.py"), []byte("app_name = \""+name+"\"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(module, "__init__.py"), []byte("__version__ = \""+version+"\"\n"), 0o644))
}

// TestExportBundleWithAReleaseLine is the co-installation case from issue #14, through
// the bundle command: hrms was built against erpnext 16.16.0 and the store has moved on
// to 16.30.0. The requirement's release line accepts it, so the bundle ships the version
// that is actually there instead of failing on a pin that no longer exists.
func TestExportBundleWithAReleaseLine(t *testing.T) {
	store := t.TempDir()
	cfg := &config.FPMConfig{AppsBasePath: store}
	storePackaged(t, store, metadata.AppMetadata{Org: "frappe", AppName: "erpnext", PackageVersion: "16.30.0"})
	hrms := storePackaged(t, store, metadata.AppMetadata{Org: "frappe", AppName: "hrms", PackageVersion: "16.16.0",
		RequiredApps: []metadata.RequiredApp{{
			Name: "erpnext", Org: "frappe", Version: "16.16.0",
			VersionSpec: ">=16.0.0-0,<17.0.0", Requirement: "frappe/erpnext",
		}}})

	out := filepath.Join(t.TempDir(), "hrms-bundle")
	m, err := exportBundle(hrms, out, cfg, false, "", "")
	require.NoError(t, err)

	require.Len(t, m.InstallOrder, 2)
	assert.Equal(t, "frappe/erpnext==16.30.0", m.InstallOrder[0].Identifier(),
		"the bundle ships the version in the store, which the release line accepts")
	assert.FileExists(t, filepath.Join(out, "erpnext-16.30.0.fpm"))

	// Two apps built a day apart against different patch releases of the same
	// dependency now bundle the one copy the bench can hold.
	lms := storePackaged(t, store, metadata.AppMetadata{Org: "frappe", AppName: "lms", PackageVersion: "2.62.0",
		RequiredApps: []metadata.RequiredApp{{
			Name: "erpnext", Org: "frappe", Version: "16.29.0",
			VersionSpec: ">=16.0.0-0,<17.0.0", Requirement: "frappe/erpnext",
		}}})
	out2 := filepath.Join(t.TempDir(), "lms-bundle")
	m2, err := exportBundle(lms, out2, cfg, false, "", "")
	require.NoError(t, err)
	assert.Equal(t, "frappe/erpnext==16.30.0", m2.InstallOrder[0].Identifier())
}

// TestExportBundleReleaseLineOutOfRange keeps the line meaningful: a store holding only
// the previous major cannot satisfy it, and the error says what was needed.
func TestExportBundleReleaseLineOutOfRange(t *testing.T) {
	store := t.TempDir()
	cfg := &config.FPMConfig{AppsBasePath: store}
	storePackaged(t, store, metadata.AppMetadata{Org: "frappe", AppName: "erpnext", PackageVersion: "15.120.0"})
	hrms := storePackaged(t, store, metadata.AppMetadata{Org: "frappe", AppName: "hrms", PackageVersion: "16.16.0",
		RequiredApps: []metadata.RequiredApp{{
			Name: "erpnext", Org: "frappe", Version: "16.16.0", VersionSpec: ">=16.0.0-0,<17.0.0",
		}}})

	_, err := exportBundle(hrms, filepath.Join(t.TempDir(), "bundle"), cfg, false, "", "")
	require.Error(t, err)
	assert.True(t, errors.Is(err, resolver.ErrMissing), "got %v", err)
	assert.Contains(t, err.Error(), ">=16.0.0-0,<17.0.0")
	assert.Contains(t, err.Error(), "have 15.120.0")
}
