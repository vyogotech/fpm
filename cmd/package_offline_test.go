package cmd

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"fpm/internal/apputils"
	"fpm/internal/assets"
	"fpm/internal/metadata"
	"fpm/internal/resolver"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gitInit turns dir into a git repository with one commit and returns its SHA.
func gitInit(t *testing.T, dir string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
		return string(out)
	}
	run("init", "-q", "-b", "main")
	run("remote", "add", "origin", "https://github.com/testorg/testrepo.git")
	run("add", ".")
	run("commit", "-q", "-m", "init")
	sha := run("rev-parse", "HEAD")
	return sha[:40]
}

func packageArgs(t *testing.T, sourceDir, version string, extra ...string) (args []string, fpmPath string) {
	t.Helper()
	// packageCmd is shared by every test; flags set here must not leak into the next.
	resetPackageCmdFlags()
	t.Cleanup(resetPackageCmdFlags)
	outDir := t.TempDir()
	// --no-bench-scaffold keeps the suite hermetic: without a bench, packaging an app
	// that declares esbuild entry points now fetches frappe's asset pipeline to compile
	// them. A test that wants that path builds against a fake bench instead.
	args = append([]string{"package", "--bundle-deps=false", "--skip-local-install", "--no-bench-scaffold",
		"--version", version, "--output-path", outDir}, extra...)
	args = append(args, sourceDir)
	return args, outDir
}

// TestPackageRecordsCommitSHA covers item 5: the exact HEAD commit, not a ref, is
// stored in app_metadata.json so external caches can key on it.
func TestPackageRecordsCommitSHA(t *testing.T) {
	src := SharedCreateMinimalAppForPackage(t, t.TempDir(), "sha_app", nil)
	sha := gitInit(t, src)
	resetPackageCmdFlags()
	args, outDir := packageArgs(t, src, "1.0.0")
	_, err := SharedExecuteCommand(rootCmd, args...)
	require.NoError(t, err)

	meta, err := SharedReadMetadataFromFpm(t, filepath.Join(outDir, "sha_app-1.0.0.fpm"))
	require.NoError(t, err)
	assert.Equal(t, sha, meta.CommitSHA)
	assert.Equal(t, "main", meta.GitRef)
	assert.False(t, meta.GitDirty)
	assert.Equal(t, "testorg", meta.Org, "org still derived from the remote")
	assert.Equal(t, "https://github.com/testorg/testrepo.git", meta.SourceControlURL)

	// A dirty tree is flagged: the SHA alone would not reproduce the package.
	require.NoError(t, os.WriteFile(filepath.Join(src, "sha_app", "modules.txt"), []byte("changed"), 0o644))
	resetPackageCmdFlags()
	args, outDir = packageArgs(t, src, "1.0.1")
	_, err = SharedExecuteCommand(rootCmd, args...)
	require.NoError(t, err)
	meta, err = SharedReadMetadataFromFpm(t, filepath.Join(outDir, "sha_app-1.0.1.fpm"))
	require.NoError(t, err)
	assert.Equal(t, sha, meta.CommitSHA)
	assert.True(t, meta.GitDirty)
}

// TestPackageRejectsNonFrappeAppFirst covers item 1: a plain Python project fails
// validation before any other work, with the typed error and its exit code.
func TestPackageRejectsNonFrappeAppFirst(t *testing.T) {
	src := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(src, "pkg"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "pkg", "__init__.py"), nil, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(src, "pyproject.toml"), []byte("[project]\nname='pkg'\ndependencies=['requests']\n"), 0o644))

	resetPackageCmdFlags()
	args, outDir := packageArgs(t, src, "1.0.0")
	_, err := SharedExecuteCommand(rootCmd, args...)
	require.Error(t, err)
	assert.True(t, errors.Is(err, apputils.ErrNotFrappeApp), "got %v", err)
	assert.Equal(t, ExitNotFrappeApp, ExitCodeFor(err))
	entries, _ := os.ReadDir(outDir)
	assert.Empty(t, entries, "nothing is built for a rejected input")
}

// TestPackageResolvesRequiredApps covers item 7 at packaging time: required_apps
// are pinned against the local store and recorded; an unresolvable one fails.
func TestPackageResolvesRequiredApps(t *testing.T) {
	store := t.TempDir()
	t.Setenv("FPM_APPS_BASE_PATH", store)
	t.Setenv("HOME", t.TempDir()) // no configured repositories
	existsStoreApp(t, store, "frappe", "erpnext", "15.2.0", metadata.AppMetadata{})
	existsStoreApp(t, store, "frappe", "erpnext", "15.10.0", metadata.AppMetadata{})

	src := SharedCreateMinimalAppForPackage(t, t.TempDir(), "needs_erpnext", map[string]string{
		"needs_erpnext/hooks.py": "app_name = \"needs_erpnext\"\nrequired_apps = [\n\t\"frappe\",\n\t\"erpnext\",\n]\n",
	})
	resetPackageCmdFlags()
	// A prod package has to name its resolution source; here it is this host's own
	// store, said out loud (see TestPackageProdRefusesAmbientStorePin).
	args, outDir := packageArgs(t, src, "1.0.0", "--org", "acme", "--requires-from-local-store")
	_, err := SharedExecuteCommand(rootCmd, args...)
	require.NoError(t, err)
	meta, err := SharedReadMetadataFromFpm(t, filepath.Join(outDir, "needs_erpnext-1.0.0.fpm"))
	require.NoError(t, err)
	require.Len(t, meta.RequiredApps, 1, "frappe is never a dependency")
	assert.Equal(t, "15.10.0", meta.RequiredApps[0].Version, "the version resolved against")
	assert.Equal(t, ">=15.0.0-0,<16.0.0", meta.RequiredApps[0].VersionSpec, "recorded as a release line, not one version")
	assert.Equal(t, "erpnext", meta.RequiredApps[0].Requirement)
	assert.Equal(t, ">=15.0.0-0,<16.0.0", meta.Dependencies["frappe/erpnext"])

	// Unresolvable requirement: hard failure with its own exit code.
	src = SharedCreateMinimalAppForPackage(t, t.TempDir(), "needs_hrms", map[string]string{
		"needs_hrms/hooks.py": "app_name = \"needs_hrms\"\nrequired_apps = [\"erpnext\", \"hrms\"]\n",
	})
	resetPackageCmdFlags()
	args, _ = packageArgs(t, src, "1.0.0", "--org", "acme", "--requires-from-local-store")
	_, err = SharedExecuteCommand(rootCmd, args...)
	require.Error(t, err)
	assert.True(t, errors.Is(err, resolver.ErrUnresolved), "got %v", err)
	assert.Equal(t, ExitUnresolvedRequiredApps, ExitCodeFor(err))
	assert.Contains(t, err.Error(), "hrms")
}

// TestPackageShipsBuiltAssetsInsideModule: a package built from a tree whose
// <app>/public/dist already holds bundles ships them in place (the layout
// bench build leaves), which fpm install then records in assets.json.
func TestPackageAndInstallDeployBuiltAssets(t *testing.T) {
	src := SharedCreateMinimalAppForPackage(t, t.TempDir(), "builtapp", map[string]string{
		"builtapp/public/dist/js/builtapp.bundle.ABCDEFGH.js":       "console.log(1)",
		"builtapp/public/dist/css/builtapp.bundle.HHHHHHHH.css":     "body{}",
		"builtapp/public/dist/css-rtl/builtapp.bundle.RRRRRRRR.css": "body{}",
		"builtapp/public/images/logo.svg":                           "<svg/>",
	})
	resetPackageCmdFlags()
	args, outDir := packageArgs(t, src, "2.0.0", "--org", "acme")
	_, err := SharedExecuteCommand(rootCmd, args...)
	require.NoError(t, err)
	fpmPath := filepath.Join(outDir, "builtapp-2.0.0.fpm")

	meta, err := metadata.ReadMetadataFromFPMArchive(fpmPath)
	require.NoError(t, err)
	assert.True(t, meta.AssetsBuilt, "meta.AssetsBuilt should be true for discovered prebuilt assets")
	assert.Len(t, meta.AssetBundles, 3, "meta.AssetBundles should contain the discovered bundles (js, css, rtl-css)")

	store := t.TempDir()
	t.Setenv("FPM_APPS_BASE_PATH", store)
	t.Setenv("HOME", t.TempDir())
	bench := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(bench, "env", "bin"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bench, "env", "bin", "pip"), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(bench, "sites", "assets"), 0o755))
	// Another app's manifest entry must survive untouched.
	require.NoError(t, os.WriteFile(filepath.Join(bench, "sites", "assets", "assets.json"),
		[]byte("{\n    \"libs.bundle.js\": \"/assets/frappe/dist/js/libs.bundle.WGSJP7XT.js\"\n}"), 0o644))

	resetInstallCmdFlags()
	_, err = SharedExecuteCommand(rootCmd, "install", fpmPath, "--bench-path", bench)
	require.NoError(t, err)

	// sites/assets/<app> is a symlink to the app's public dir, as make_asset_dirs does.
	link := filepath.Join(bench, "sites", "assets", "builtapp")
	target, err := os.Readlink(link)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(store, "acme", "builtapp", "2.0.0", "builtapp", "public"), target)
	_, err = os.Stat(filepath.Join(link, "images", "logo.svg"))
	assert.NoError(t, err, "non-bundled public files are served through the link")

	m, err := assets.ReadManifest(filepath.Join(bench, "sites", "assets", "assets.json"))
	require.NoError(t, err)
	assert.Equal(t, []string{"libs.bundle.js", "builtapp.bundle.css", "builtapp.bundle.js"}, m.Keys())
	v, _ := m.Get("builtapp.bundle.js")
	assert.Equal(t, "/assets/builtapp/dist/js/builtapp.bundle.ABCDEFGH.js", v)
	// The served path must resolve to a real file.
	_, err = os.Stat(filepath.Join(bench, "sites", v[1:]))
	assert.NoError(t, err)

	rtl, err := assets.ReadManifest(filepath.Join(bench, "sites", "assets", "assets-rtl.json"))
	require.NoError(t, err)
	v, _ = rtl.Get("rtl_builtapp.bundle.css")
	assert.Equal(t, "/assets/builtapp/dist/css-rtl/builtapp.bundle.RRRRRRRR.css", v)
}

// TestInstallRefusesMissingRequiredApps covers item 7 at install time: a package whose
// pinned required app is not in the local store is refused, and nothing is fetched.
func TestInstallRefusesMissingRequiredApps(t *testing.T) {
	store := t.TempDir()
	t.Setenv("FPM_APPS_BASE_PATH", store)
	t.Setenv("HOME", t.TempDir())
	src := SharedCreateMinimalAppForPackage(t, t.TempDir(), "dependent", map[string]string{
		"dependent/hooks.py": "app_name = \"dependent\"\nrequired_apps = [\"erpnext\"]\n",
	})
	// Resolve against a store that has erpnext, then remove it before installing.
	existsStoreApp(t, store, "frappe", "erpnext", "15.10.0", metadata.AppMetadata{})
	resetPackageCmdFlags()
	args, outDir := packageArgs(t, src, "1.0.0", "--org", "acme", "--requires-from-local-store")
	_, err := SharedExecuteCommand(rootCmd, args...)
	require.NoError(t, err)
	fpmPath := filepath.Join(outDir, "dependent-1.0.0.fpm")
	require.NoError(t, os.RemoveAll(filepath.Join(store, "frappe")))

	bench := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(bench, "env", "bin"), 0o755))
	pipCalled := filepath.Join(bench, "pip-called")
	require.NoError(t, os.WriteFile(filepath.Join(bench, "env", "bin", "pip"), []byte("#!/bin/sh\ntouch "+pipCalled+"\nexit 0\n"), 0o755))

	resetInstallCmdFlags()
	_, err = SharedExecuteCommand(rootCmd, "install", fpmPath, "--bench-path", bench)
	require.Error(t, err)
	assert.True(t, errors.Is(err, resolver.ErrMissing), "got %v", err)
	assert.Equal(t, ExitMissingRequiredApps, ExitCodeFor(err))
	assert.Contains(t, err.Error(), "frappe/erpnext>=15.0.0-0,<16.0.0")
	_, statErr := os.Stat(pipCalled)
	assert.True(t, os.IsNotExist(statErr), "the bench must not be touched")
	_, statErr = os.Lstat(filepath.Join(bench, "apps", "dependent"))
	assert.True(t, os.IsNotExist(statErr), "no symlink is created before the check passes")

	// With the dependency back in the store (and in the bench) the install proceeds.
	existsStoreApp(t, store, "frappe", "erpnext", "15.10.0", metadata.AppMetadata{})
	require.NoError(t, os.MkdirAll(filepath.Join(bench, "sites"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bench, "sites", "apps.txt"), []byte("frappe\nerpnext\n"), 0o644))
	resetInstallCmdFlags()
	out, err := SharedExecuteCommand(rootCmd, "install", fpmPath, "--bench-path", bench)
	require.NoError(t, err, out)
	assert.Contains(t, out, "Required app frappe/erpnext==15.10.0 satisfied from local FPM store")
	_, statErr = os.Stat(pipCalled)
	assert.NoError(t, statErr)
}

// dummyFPMWithMeta builds an .fpm in memory from metadata, a minimal app module and
// extra files, for install-side tests that do not need a real packaging run.
func dummyFPMWithMeta(t *testing.T, meta metadata.AppMetadata, extra map[string]string) []byte {
	t.Helper()
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)
	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	require.NoError(t, err)
	w, err := zw.Create("app_metadata.json")
	require.NoError(t, err)
	_, _ = w.Write(metaBytes)
	for _, f := range []string{"__init__.py", "hooks.py", "modules.txt"} {
		w, err := zw.Create(meta.AppName + "/" + f)
		require.NoError(t, err)
		_, _ = w.Write([]byte("app_name = \"" + meta.AppName + "\"\n"))
	}
	for name, content := range extra {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, _ = w.Write([]byte(content))
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// TestInstallRefusesWrongPlatformWheels covers item 3's install-side hard check.
func TestInstallRefusesWrongPlatformWheels(t *testing.T) {
	store := t.TempDir()
	t.Setenv("FPM_APPS_BASE_PATH", store)
	t.Setenv("HOME", t.TempDir())

	// A package claiming wheels for a platform this host is not.
	fpmPath := filepath.Join(t.TempDir(), "foreign-1.0.0.fpm")
	require.NoError(t, os.WriteFile(fpmPath, dummyFPMWithMeta(t, metadata.AppMetadata{
		Org: "acme", AppName: "foreign", PackageVersion: "1.0.0", WheelPlatform: "win_amd64_nonsense",
	}, map[string]string{"wheels/x-1.0-py3-none-any.whl": "x"}), 0o644))

	bench := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(bench, "env", "bin"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bench, "env", "bin", "pip"), []byte("#!/bin/sh\nexit 0\n"), 0o755))

	resetInstallCmdFlags()
	_, err := SharedExecuteCommand(rootCmd, "install", fpmPath, "--bench-path", bench)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPlatformMismatch), "got %v", err)
	assert.Equal(t, ExitPlatformMismatch, ExitCodeFor(err))

	resetInstallCmdFlags()
	_, err = SharedExecuteCommand(rootCmd, "install", fpmPath, "--bench-path", bench, "--ignore-platform-mismatch")
	require.NoError(t, err)
}
