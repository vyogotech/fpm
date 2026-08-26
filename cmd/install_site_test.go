package cmd

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// stubBench writes a fake bench virtualenv python that records the arguments it was
// called with (and the directory it ran in), so a test can assert the exact frappe
// command without needing a real Frappe bench.
func stubBench(t *testing.T, benchPath string, exitCode int) string {
	t.Helper()

	envBin := filepath.Join(benchPath, "env", "bin")
	require.NoError(t, os.MkdirAll(envBin, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(benchPath, "sites"), 0o755))

	logPath := filepath.Join(benchPath, "bench_args.log")
	script := "#!/bin/sh\necho \"cwd=$(pwd) $@\" >> " + logPath + "\n"
	if exitCode != 0 {
		script += "echo 'site does not exist' >&2\n"
	}
	script += "exit " + strconv.Itoa(exitCode) + "\n"

	require.NoError(t, os.WriteFile(filepath.Join(envBin, "python"), []byte(script), 0o755))
	return logPath
}

// TestInstallAppOnSiteRunsBench is the behaviour --site advertises: adding an app to the
// bench does not make it active on a site, which needs bench to create DocTypes and run
// the app's patches.
func TestInstallAppOnSiteRunsBench(t *testing.T) {
	benchPath := t.TempDir()
	logPath := stubBench(t, benchPath, 0)

	err := installAppOnSite(benchPath, "mysite.local", "my_app")
	require.NoError(t, err)

	logged, readErr := os.ReadFile(logPath)
	require.NoError(t, readErr, "frappe should have been invoked")
	// Exactly what `bench --site mysite.local install-app my_app` execs, from sites/.
	require.Contains(t, string(logged), "-m frappe.utils.bench_helper frappe --site mysite.local install-app my_app")
	// Run from <bench>/sites, as bench's frappe_cmd does (paths may be reported through
	// a symlinked temp dir, so only the tail is compared).
	require.Regexp(t, `cwd=\S*/sites `, string(logged))
	// And the site cache is cleared afterwards, explicitly.
	require.Contains(t, string(logged), "frappe --site mysite.local clear-cache")
}

// TestInstallAppOnSitePropagatesFailure keeps a failed site install loud: the app is in
// the bench but not active on the site, which is a state the user must know about.
func TestInstallAppOnSitePropagatesFailure(t *testing.T) {
	benchPath := t.TempDir()
	stubBench(t, benchPath, 1)

	err := installAppOnSite(benchPath, "mysite.local", "my_app")
	require.Error(t, err)
	require.Contains(t, err.Error(), "mysite.local")
	require.Contains(t, err.Error(), "site does not exist",
		"bench output should be surfaced, not swallowed")
}

// TestInstallAppOnSiteWithoutBench covers a bench directory with no bench executable.
// The app is already installed at that point, so the error must say so and give the
// command to run by hand rather than implying the whole install failed.
func TestInstallAppOnSiteWithoutBench(t *testing.T) {
	benchPath := t.TempDir()

	err := installAppOnSite(benchPath, "mysite.local", "my_app")
	require.Error(t, err)
	require.Contains(t, err.Error(), "bench --site mysite.local install-app my_app",
		"error should give the command to run manually")
}

// TestInstallAppOnSiteRestoresWorkingDirectory guards against leaking a directory change
// into the rest of the process, which would break any later relative path.
func TestInstallAppOnSiteRestoresWorkingDirectory(t *testing.T) {
	benchPath := t.TempDir()
	stubBench(t, benchPath, 0)

	before, err := os.Getwd()
	require.NoError(t, err)

	require.NoError(t, installAppOnSite(benchPath, "mysite.local", "my_app"))

	after, err := os.Getwd()
	require.NoError(t, err)
	require.Equal(t, before, after, "working directory must be restored")

	// And on the failure path too.
	stubBench(t, benchPath, 1)
	_ = installAppOnSite(benchPath, "mysite.local", "my_app")

	after, err = os.Getwd()
	require.NoError(t, err)
	require.Equal(t, before, after, "working directory must be restored even when bench fails")
}
