package cmd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"fpm/internal/metadata"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// benchWithApp makes a bench that already has app at version, as a real
// directory the way `bench get-app` leaves it, listed in sites/apps.txt. Its
// python is stubBench's, so every frappe invocation is logged.
func benchWithApp(t *testing.T, app, version string) (bench, frappeLog string) {
	t.Helper()
	bench = t.TempDir()
	frappeLog = stubBench(t, bench, 0)
	module := filepath.Join(bench, "apps", app, app)
	require.NoError(t, os.MkdirAll(module, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(module, "__init__.py"), []byte("__version__ = '"+version+"'\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(module, "hooks.py"), []byte("app_name = '"+app+"'\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(bench, "sites", "apps.txt"), []byte("frappe\n"+app+"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(bench, "env", "bin", "pip"), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	return bench, frappeLog
}

// countingRepo is a configured repository that records every request made to
// it, so a test can prove nothing was fetched.
func countingRepo(t *testing.T) *atomic.Int32 {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	SharedResetRepoCmdFlags()
	_, err := SharedExecuteCommand(rootCmd, "repo", "add", "counting", srv.URL)
	require.NoError(t, err)
	return &hits
}

// A second site on a bench that already has the app is a site-level install-app
// against the bench's copy, nothing more: no fetch, no relink, no apps.txt write
// — and a different requested version does not replace the copy every other
// site on the bench is running.
func TestInstall_AppAlreadyInBenchIsASiteInstallOnly(t *testing.T) {
	_, cleanup := setupTempFPMConfig(t)
	defer cleanup()
	hits := countingRepo(t)
	bench, frappeLog := benchWithApp(t, "erpnext", "16.1.0")
	appsTxtBefore, err := os.ReadFile(filepath.Join(bench, "sites", "apps.txt"))
	require.NoError(t, err)

	resetInstallCmdFlags()
	out, err := SharedExecuteCommand(rootCmd, "install", "frappe/erpnext==16.2.0", "--bench-path", bench, "--site", "tenant2.local")
	require.NoError(t, err, out)

	assert.Zero(t, hits.Load(), "an app the bench already has must not be fetched")
	info, err := os.Lstat(filepath.Join(bench, "apps", "erpnext"))
	require.NoError(t, err)
	assert.True(t, info.IsDir(), "the bench's copy must be left in place, not relinked")
	appsTxtAfter, err := os.ReadFile(filepath.Join(bench, "sites", "apps.txt"))
	require.NoError(t, err)
	assert.Equal(t, string(appsTxtBefore), string(appsTxtAfter))

	logged, err := os.ReadFile(frappeLog)
	require.NoError(t, err)
	assert.Contains(t, string(logged), "frappe --site tenant2.local install-app erpnext")
	assert.Contains(t, out, "already in bench")
	assert.Contains(t, out, "erpnext 16.2.0 was asked for, but bench", "a version mismatch must be reported, not hidden")
}

// Without --site there is nothing left to do once the bench has the app.
func TestInstall_AppAlreadyInBenchWithoutSiteDoesNothing(t *testing.T) {
	_, cleanup := setupTempFPMConfig(t)
	defer cleanup()
	hits := countingRepo(t)
	bench, frappeLog := benchWithApp(t, "erpnext", "16.1.0")

	resetInstallCmdFlags()
	out, err := SharedExecuteCommand(rootCmd, "install", "frappe/erpnext", "--bench-path", bench)
	require.NoError(t, err, out)

	assert.Zero(t, hits.Load())
	_, err = os.Stat(frappeLog)
	assert.True(t, os.IsNotExist(err), "no site was named, so frappe must not run")
	assert.NotContains(t, out, "was asked for", "an unpinned request is satisfied by any version")
}

// benchKeepingAppsElsewhere makes a bench that keeps app outside apps/ — on the
// python path, the way a bench on a volume shared by several pods does — and
// lists it in sites/apps.txt. Its python answers fpm's import probe with the
// host's python3 and logs every frappe invocation instead of running it.
func benchKeepingAppsElsewhere(t *testing.T, app, version string) (bench, frappeLog string) {
	t.Helper()
	python3, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	bench = t.TempDir()
	frappeLog = filepath.Join(bench, "bench_args.log")
	envBin := filepath.Join(bench, "env", "bin")
	require.NoError(t, os.MkdirAll(envBin, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(bench, "apps"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(bench, "sites"), 0o755))
	python := "#!/bin/sh\nif [ \"$1\" = \"-c\" ]; then exec " + python3 + " \"$@\"; fi\necho \"cwd=$(pwd) $@\" >> " + frappeLog + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(envBin, "python"), []byte(python), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(envBin, "pip"), []byte("#!/bin/sh\nexit 0\n"), 0o755))

	shared := filepath.Join(t.TempDir(), "shared-volume", app)
	module := filepath.Join(shared, app)
	require.NoError(t, os.MkdirAll(module, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(module, "__init__.py"), []byte("__version__ = '"+version+"'\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(module, "hooks.py"), []byte("app_name = '"+app+"'\n"), 0o644))
	t.Setenv("PYTHONPATH", shared)
	require.NoError(t, os.WriteFile(filepath.Join(bench, "sites", "apps.txt"), []byte("frappe\n"+app+"\n"), 0o644))
	return bench, frappeLog
}

// Where a bench keeps its apps is the bench's business. An app it has anywhere
// frappe would import it from is an app it has: a second site gets install-app
// against that copy, and nothing is fetched into apps/ to shadow it.
func TestInstall_AppTheBenchKeepsOutsideAppsIsASiteInstallOnly(t *testing.T) {
	_, cleanup := setupTempFPMConfig(t)
	defer cleanup()
	hits := countingRepo(t)
	bench, frappeLog := benchKeepingAppsElsewhere(t, "erpnext", "16.1.0")

	resetInstallCmdFlags()
	out, err := SharedExecuteCommand(rootCmd, "install", "frappe/erpnext==16.1.0", "--bench-path", bench, "--site", "tenant2.local")
	require.NoError(t, err, out)

	assert.Zero(t, hits.Load(), "an app the bench already has must not be fetched")
	_, err = os.Lstat(filepath.Join(bench, "apps", "erpnext"))
	assert.True(t, os.IsNotExist(err), "nothing may be put in apps/ to shadow the bench's copy")
	logged, err := os.ReadFile(frappeLog)
	require.NoError(t, err)
	assert.Contains(t, string(logged), "frappe --site tenant2.local install-app erpnext")
}

// The same holds for a required app: installing a new app whose dependency the
// bench keeps outside apps/ installs the new app only.
func TestInstall_RequiredAppTheBenchKeepsOutsideAppsIsNotReinstalled(t *testing.T) {
	_, cleanup := setupTempFPMConfig(t)
	defer cleanup()
	bench, _ := benchKeepingAppsElsewhere(t, "erpnext", "16.1.0")
	pkg := filepath.Join(t.TempDir(), "hrms-16.1.0.fpm")
	require.NoError(t, os.WriteFile(pkg, buildFpmWithDeps(t, "frappe", "hrms", "16.1.0", []metadata.RequiredApp{
		{Name: "erpnext", Org: "frappe", Version: "16.1.0"},
	}), 0o644))

	resetInstallCmdFlags()
	out, err := SharedExecuteCommand(rootCmd, "install", pkg, "--bench-path", bench)
	require.NoError(t, err, out)

	_, err = os.Lstat(filepath.Join(bench, "apps", "hrms"))
	assert.NoError(t, err, "the new app is installed")
	_, err = os.Lstat(filepath.Join(bench, "apps", "erpnext"))
	assert.True(t, os.IsNotExist(err), "the bench's erpnext must be used as it is, not reinstalled")
	assert.Contains(t, out, "provided by the bench")
}

// A local package file names its app in its metadata; the same rule applies.
// --overwrite is the deliberate way to replace the bench's copy.
func TestInstall_LocalPackageForAppInBenchNeedsOverwrite(t *testing.T) {
	_, cleanup := setupTempFPMConfig(t)
	defer cleanup()
	bench, _ := benchWithApp(t, "myapp", "1.0.0")
	pkg := filepath.Join(t.TempDir(), "myapp-2.0.0.fpm")
	require.NoError(t, os.WriteFile(pkg, buildFpmWithDeps(t, "acme", "myapp", "2.0.0", nil), 0o644))
	link := filepath.Join(bench, "apps", "myapp")

	resetInstallCmdFlags()
	out, err := SharedExecuteCommand(rootCmd, "install", pkg, "--bench-path", bench)
	require.NoError(t, err, out)
	info, err := os.Lstat(link)
	require.NoError(t, err)
	assert.True(t, info.IsDir(), "without --overwrite the bench's copy stays")

	resetInstallCmdFlags()
	out, err = SharedExecuteCommand(rootCmd, "install", pkg, "--bench-path", bench, "--overwrite")
	require.NoError(t, err, out)
	target, err := os.Readlink(link)
	require.NoError(t, err, "--overwrite replaces the bench's copy with the package")
	assert.Contains(t, target, filepath.Join("acme", "myapp", "2.0.0"))
}
