package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// scriptedBench writes a fake bench python that dispatches on the frappe subcommand
// it is handed, so a test can model the exact half-installed state issue #13
// reported: install-app fails, and the site's DocType count is whatever the test
// says it is. Every invocation is logged.
//
// The count file holds what `frappe --site <site> execute frappe.db.count` prints;
// an empty file reproduces frappe printing nothing, which means zero.
func scriptedBench(t *testing.T, benchPath string, installExit int, forcedInstallExit int) (logPath, countPath string) {
	t.Helper()

	envBin := filepath.Join(benchPath, "env", "bin")
	require.NoError(t, os.MkdirAll(envBin, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(benchPath, "sites"), 0o755))

	logPath = filepath.Join(benchPath, "bench_args.log")
	countPath = filepath.Join(benchPath, "doctype_count")
	require.NoError(t, os.WriteFile(countPath, nil, 0o644))

	script := `#!/bin/sh
echo "$@" >> ` + logPath + `
case "$*" in
  *"execute frappe.db.count"*)
    cat ` + countPath + `
    exit 0
    ;;
  *"install-app --force"*)
    echo 'forced install output'
    exit ` + strconv.Itoa(forcedInstallExit) + `
    ;;
  *install-app*)
    echo 'ImportError: Module import failed for CRM Lead Status' >&2
    exit ` + strconv.Itoa(installExit) + `
    ;;
esac
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(envBin, "python"), []byte(script), 0o755))
	return logPath, countPath
}

// stubAppWithDocType puts an app with one DocType into the bench, which is what
// makes the DocType verification meaningful.
func stubAppWithDocType(t *testing.T, benchPath, appName, docTypeName string) {
	t.Helper()
	dir := filepath.Join(benchPath, "apps", appName, appName, "fcrm", "doctype", "crm_lead_status")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	body := `{"doctype": "DocType", "name": "` + docTypeName + `", "module": "FCRM"}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "crm_lead_status.json"), []byte(body), 0o644))
}

// TestInstallAppOnSiteClearsCacheFirst covers the cause of issue #13: fpm changed
// sites/apps.txt from the outside, so frappe's cached app-to-modules map is stale
// and its sync_for would iterate an empty module list. The cache has to be cleared
// before install-app, not only after it.
func TestInstallAppOnSiteClearsCacheFirst(t *testing.T) {
	benchPath := t.TempDir()
	logPath := stubBench(t, benchPath, 0)

	require.NoError(t, installAppOnSite(benchPath, "mysite.local", "my_app"))

	logged, err := os.ReadFile(logPath)
	require.NoError(t, err)
	clearFirst := strings.Index(string(logged), "clear-cache")
	install := strings.Index(string(logged), "install-app")
	require.Greater(t, install, -1, "install-app should have run")
	require.Greater(t, clearFirst, -1, "clear-cache should have run")
	require.Less(t, clearFirst, install, "the cache must be cleared before install-app, not only after")
}

// TestInstallAppOnSiteRepairsMissingDocTypes is the defect itself: install-app
// fails in after_install because no DocType was synced. fpm repairs it rather than
// leaving a site that is registered but empty.
func TestInstallAppOnSiteRepairsMissingDocTypes(t *testing.T) {
	benchPath := t.TempDir()
	logPath, countPath := scriptedBench(t, benchPath, 1, 0)
	stubAppWithDocType(t, benchPath, "crm", "CRM Lead Status")
	installNoSiteRepair = false

	// The forced re-install is what makes the DocTypes appear.
	forcedCount := "1\n"

	// Model the count flipping to 1 once install-app --force has run: the stub
	// writes the count file itself on the forced path.
	script, err := os.ReadFile(filepath.Join(benchPath, "env", "bin", "python"))
	require.NoError(t, err)
	patched := strings.Replace(string(script), "echo 'forced install output'",
		"printf '"+forcedCount+"' > "+countPath+"\necho 'forced install output'", 1)
	require.NoError(t, os.WriteFile(filepath.Join(benchPath, "env", "bin", "python"), []byte(patched), 0o755))

	require.NoError(t, installAppOnSite(benchPath, "mysite.local", "crm"),
		"a half-installed site should be repaired, not reported as a failure")

	logged, err := os.ReadFile(logPath)
	require.NoError(t, err)
	require.Contains(t, string(logged), "install-app --force crm", "the repair re-runs the install")
	require.Contains(t, string(logged), "execute frappe.db.count", "the DocTypes must actually be verified")
}

// TestInstallAppOnSiteReportsUnrepairableHalfInstall keeps the failure honest when
// the repair does not work: a distinct error naming the state and the command that
// finishes the job.
func TestInstallAppOnSiteReportsUnrepairableHalfInstall(t *testing.T) {
	benchPath := t.TempDir()
	_, _ = scriptedBench(t, benchPath, 1, 1)
	stubAppWithDocType(t, benchPath, "crm", "CRM Lead Status")
	installNoSiteRepair = false

	err := installAppOnSite(benchPath, "mysite.local", "crm")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrSiteHalfInstalled), "got %v", err)
	require.Contains(t, err.Error(), "bench --site mysite.local migrate")
	require.Equal(t, ExitSiteHalfInstalled, ExitCodeFor(err),
		"a recoverable half-install must be distinguishable from a generic failure")
}

// TestInstallAppOnSiteNoRepairFlag: with --no-site-repair the state is reported and
// nothing is re-run.
func TestInstallAppOnSiteNoRepairFlag(t *testing.T) {
	benchPath := t.TempDir()
	logPath, _ := scriptedBench(t, benchPath, 1, 0)
	stubAppWithDocType(t, benchPath, "crm", "CRM Lead Status")
	installNoSiteRepair = true
	defer func() { installNoSiteRepair = false }()

	err := installAppOnSite(benchPath, "mysite.local", "crm")
	require.True(t, errors.Is(err, ErrSiteHalfInstalled), "got %v", err)

	logged, _ := os.ReadFile(logPath)
	require.NotContains(t, string(logged), "--force", "--no-site-repair must not re-run the install")
}

// TestInstallAppOnSiteKeepsUnrelatedFailure: when the DocTypes did sync, a failing
// install-app is the app's own problem and must be surfaced as-is, not retried.
func TestInstallAppOnSiteKeepsUnrelatedFailure(t *testing.T) {
	benchPath := t.TempDir()
	logPath, countPath := scriptedBench(t, benchPath, 1, 0)
	stubAppWithDocType(t, benchPath, "crm", "CRM Lead Status")
	require.NoError(t, os.WriteFile(countPath, []byte("1\n"), 0o644))
	installNoSiteRepair = false

	err := installAppOnSite(benchPath, "mysite.local", "crm")
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrSiteHalfInstalled), "got %v", err)
	require.Contains(t, err.Error(), "ImportError", "the app's own failure must be surfaced")

	logged, _ := os.ReadFile(logPath)
	require.NotContains(t, string(logged), "--force", "a synced app must not be force-reinstalled")
}

// TestAppDocTypeNames reads the DocTypes an app ships, ignoring the JSON files in a
// doctype directory that are not the DocType itself.
func TestAppDocTypeNames(t *testing.T) {
	benchPath := t.TempDir()
	stubAppWithDocType(t, benchPath, "crm", "CRM Lead Status")
	other := filepath.Join(benchPath, "apps", "crm", "crm", "fcrm", "doctype", "crm_lead_status")
	require.NoError(t, os.WriteFile(filepath.Join(other, "test_records.json"), []byte(`[{"name": "x"}]`), 0o644))

	require.Equal(t, []string{"CRM Lead Status"}, appDocTypeNames(benchPath, "crm", 100))
	require.Empty(t, appDocTypeNames(benchPath, "absent", 100))
}
