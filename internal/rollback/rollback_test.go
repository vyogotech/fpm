package rollback

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"fpm/internal/snapshot"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRollback_LIFO_And_PreservePreExisting(t *testing.T) {
	bench := t.TempDir()

	// Pre-existing app: "payments"
	paymentsDir := filepath.Join(bench, "apps", "payments", "payments")
	require.NoError(t, os.MkdirAll(paymentsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(paymentsDir, "__init__.py"), []byte("__version__ = '1.0.0'\n"), 0o644))

	// Pre-existing sites/apps.txt
	sitesDir := filepath.Join(bench, "sites")
	require.NoError(t, os.MkdirAll(sitesDir, 0o755))
	appsTxtPath := filepath.Join(sitesDir, "apps.txt")
	require.NoError(t, os.WriteFile(appsTxtPath, []byte("frappe\npayments\n"), 0o644))

	// Mock pip script
	envBin := filepath.Join(bench, "env", "bin")
	require.NoError(t, os.MkdirAll(envBin, 0o755))
	pipLog := filepath.Join(bench, "pip_uninstall.log")
	pipScript := "#!/bin/sh\necho \"$@\" >> " + pipLog + "\nexit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(envBin, "pip"), []byte(pipScript), 0o755))

	// Take snapshot
	snap, err := snapshot.Take(bench, "")
	require.NoError(t, err)

	// Now simulate installing new apps during a session:
	// 1. New app: "erpnext"
	erpnextLink := filepath.Join(bench, "apps", "erpnext")
	require.NoError(t, os.Symlink(t.TempDir(), erpnextLink))
	require.NoError(t, os.WriteFile(appsTxtPath, []byte("frappe\npayments\nerpnext\n"), 0o644))

	// 2. New app: "hrms"
	hrmsLink := filepath.Join(bench, "apps", "hrms")
	require.NoError(t, os.Symlink(t.TempDir(), hrmsLink))
	require.NoError(t, os.WriteFile(appsTxtPath, []byte("frappe\npayments\nerpnext\nhrms\n"), 0o644))

	// Build journal
	journal := NewJournal(snap)
	// Add actions for payments (pre-existing)
	journal.Record(&SymlinkAction{BenchPath: bench, App: "payments", Snapshot: snap})
	journal.Record(&PipInstallAction{BenchPath: bench, App: "payments", Snapshot: snap})
	// Add actions for erpnext (new)
	journal.Record(&SymlinkAction{BenchPath: bench, App: "erpnext", Snapshot: snap})
	journal.Record(&PipInstallAction{BenchPath: bench, App: "erpnext", Snapshot: snap})
	journal.Record(&AppsTxtAction{BenchPath: bench, App: "erpnext", Snapshot: snap})
	// Add actions for hrms (new)
	journal.Record(&SymlinkAction{BenchPath: bench, App: "hrms", Snapshot: snap})
	journal.Record(&PipInstallAction{BenchPath: bench, App: "hrms", Snapshot: snap})
	journal.Record(&AppsTxtAction{BenchPath: bench, App: "hrms", Snapshot: snap})

	var logBuf bytes.Buffer
	require.NoError(t, journal.Rollback(&logBuf, true))

	// Assertions:
	// 1. "hrms" and "erpnext" symlinks were removed
	_, err = os.Lstat(hrmsLink)
	assert.True(t, os.IsNotExist(err), "hrms symlink should be removed by rollback")
	_, err = os.Lstat(erpnextLink)
	assert.True(t, os.IsNotExist(err), "erpnext symlink should be removed by rollback")

	// 2. Pre-existing "payments" directory was NOT removed
	_, err = os.Stat(paymentsDir)
	assert.NoError(t, err, "payments directory must remain intact")

	// 3. apps.txt was restored to snapshot ("frappe\npayments\n")
	restoredAppsTxt, err := os.ReadFile(appsTxtPath)
	require.NoError(t, err)
	assert.Equal(t, "frappe\npayments\n", string(restoredAppsTxt))

	// 4. pip was invoked for erpnext and hrms, but NOT payments
	pipOutput, _ := os.ReadFile(pipLog)
	assert.Contains(t, string(pipOutput), "hrms")
	assert.Contains(t, string(pipOutput), "erpnext")
	assert.NotContains(t, string(pipOutput), "payments")

	// 5. Log contains preservation message
	assert.Contains(t, logBuf.String(), "Preserved pre-existing app: payments")
}
