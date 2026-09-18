package resolver

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// benchWithPython makes a bench whose env/bin/python is the host's python3, so
// BenchAppDir can ask it where an app imports from. Skips when there is none.
func benchWithPython(t *testing.T) string {
	t.Helper()
	python3, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	bench := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(bench, "env", "bin"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(bench, "sites"), 0o755))
	require.NoError(t, os.Symlink(python3, filepath.Join(bench, "env", "bin", "python")))
	return bench
}

// appOutsideApps puts an app at <root>/<name>/<name> — anywhere but apps/ — and
// returns <root>/<name>, the directory that has to be on the python path.
func appOutsideApps(t *testing.T, root, name, version string) string {
	t.Helper()
	module := filepath.Join(root, name, name)
	require.NoError(t, os.MkdirAll(module, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(module, "__init__.py"), []byte("__version__ = '"+version+"'\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(module, "hooks.py"), []byte("app_name = '"+name+"'\n"), 0o644))
	return filepath.Join(root, name)
}

func listApps(t *testing.T, bench string, apps string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(bench, "sites", "apps.txt"), []byte(apps), 0o644))
}

// A bench does not have to keep its apps in apps/: one whose apps live on a volume
// shared by several pods puts them on the python path instead. Frappe's own test
// applies — listed in sites/apps.txt and importable by the bench's python — so the
// app is found wherever it is, with no location assumed.
func TestBenchAppDirFindsAnAppOutsideApps(t *testing.T) {
	bench := benchWithPython(t)
	dir := appOutsideApps(t, filepath.Join(t.TempDir(), "shared"), "erpnext", "16.1.0")
	t.Setenv("PYTHONPATH", dir)
	listApps(t, bench, "frappe\nerpnext\n")

	got, present := BenchAppDir(bench, "erpnext")
	require.True(t, present)
	wantDir, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	gotDir, err := filepath.EvalSymlinks(got)
	require.NoError(t, err)
	assert.Equal(t, wantDir, gotDir)

	version, present := BenchAppVersion(bench, "erpnext")
	assert.True(t, present)
	assert.Equal(t, "16.1.0", version)
}

func TestBenchAppDirNeedsAppsTxtAndAnImport(t *testing.T) {
	bench := benchWithPython(t)
	root := filepath.Join(t.TempDir(), "shared")
	dir := appOutsideApps(t, root, "erpnext", "16.1.0")

	// Importable, but not one of the bench's apps.
	t.Setenv("PYTHONPATH", dir)
	listApps(t, bench, "frappe\n")
	_, present := BenchAppDir(bench, "erpnext")
	assert.False(t, present, "an importable package the bench does not list is not a bench app")

	// Listed, but only the app's outer repo dir is reachable: python resolves
	// that as a namespace package with no hooks.py — not the app.
	t.Setenv("PYTHONPATH", root)
	listApps(t, bench, "frappe\nerpnext\n")
	_, present = BenchAppDir(bench, "erpnext")
	assert.False(t, present, "a namespace package is not the app")

	// Listed, and nothing imports it.
	t.Setenv("PYTHONPATH", "")
	_, present = BenchAppDir(bench, "erpnext")
	assert.False(t, present)
}

// apps/<name> is still where bench get-app puts an app, and needs no python.
func TestBenchAppDirPrefersApps(t *testing.T) {
	bench := t.TempDir()
	benchApp(t, bench, "erpnext", "16.0.0")
	got, present := BenchAppDir(bench, "erpnext")
	require.True(t, present)
	assert.Equal(t, filepath.Join(bench, "apps", "erpnext"), got)
}
