package snapshot

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSnapshot_TakeAndRestore(t *testing.T) {
	bench := t.TempDir()

	// Setup existing app in bench: apps/existing_app
	existingAppDir := filepath.Join(bench, "apps", "existing_app", "existing_app")
	require.NoError(t, os.MkdirAll(existingAppDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(existingAppDir, "__init__.py"), []byte("__version__ = '1.2.3'\n"), 0o644))

	// Setup sites/apps.txt with existing_app
	sitesDir := filepath.Join(bench, "sites")
	require.NoError(t, os.MkdirAll(sitesDir, 0o755))
	appsTxtPath := filepath.Join(sitesDir, "apps.txt")
	require.NoError(t, os.WriteFile(appsTxtPath, []byte("frappe\nexisting_app\n"), 0o644))

	// Setup sites/assets/assets.json
	assetsDir := filepath.Join(sitesDir, "assets")
	require.NoError(t, os.MkdirAll(assetsDir, 0o755))
	ltrPath := filepath.Join(assetsDir, "assets.json")
	initialLTR := []byte("{\n    \"existing.bundle.js\": \"/assets/existing_app/dist/js/existing.123.js\"\n}")
	require.NoError(t, os.WriteFile(ltrPath, initialLTR, 0o644))

	// Take snapshot
	snap, err := Take(bench, "test-site")
	require.NoError(t, err)
	require.NotNil(t, snap)

	// Check pre-existing app assertions
	assert.True(t, snap.WasPresentInBench("existing_app"))
	assert.False(t, snap.WasPresentInBench("new_app"))
	assert.Equal(t, "1.2.3", snap.PreExistingVersion("existing_app"))
	assert.Equal(t, "", snap.PreExistingVersion("new_app"))
	assert.True(t, snap.WasInAppsTxt("existing_app"))
	assert.True(t, snap.WasInAppsTxt("frappe"))
	assert.False(t, snap.WasInAppsTxt("new_app"))

	// Simulate mutations made by an install session
	require.NoError(t, os.WriteFile(appsTxtPath, []byte("frappe\nexisting_app\nnew_app\n"), 0o644))
	mutatedLTR := []byte("{\n    \"existing.bundle.js\": \"/assets/existing_app/dist/js/existing.123.js\",\n    \"new.bundle.js\": \"/assets/new_app/dist/js/new.456.js\"\n}")
	require.NoError(t, os.WriteFile(ltrPath, mutatedLTR, 0o644))

	// Restore apps.txt and assets manifests from snapshot
	require.NoError(t, snap.RestoreAppsTxt())
	require.NoError(t, snap.RestoreAssetManifests())

	// Verify restored state matches snapshot exactly
	data, err := os.ReadFile(appsTxtPath)
	require.NoError(t, err)
	assert.Equal(t, "frappe\nexisting_app\n", string(data))

	data, err = os.ReadFile(ltrPath)
	require.NoError(t, err)
	assert.Equal(t, string(initialLTR), string(data))
}
