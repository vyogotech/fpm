package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requiresApp writes a Frappe app whose hooks.py declares required_apps.
func requiresApp(t *testing.T, baseDir, appName string, required ...string) string {
	t.Helper()
	quoted := make([]string, 0, len(required))
	for _, r := range required {
		quoted = append(quoted, `"`+r+`"`)
	}
	hooks := "app_name = \"" + appName + "\"\nrequired_apps = [" + strings.Join(quoted, ", ") + "]\n"
	return SharedCreateMinimalAppForPackage(t, baseDir, appName, map[string]string{
		filepath.Join(appName, "hooks.py"): hooks,
	})
}

// seedStore puts an extracted package into a local FPM store, which is what
// resolution reads.
func seedStore(t *testing.T, base, org, app, version string) {
	t.Helper()
	module := filepath.Join(base, org, app, version, app)
	require.NoError(t, os.MkdirAll(module, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(module, "hooks.py"), []byte("app_name = \""+app+"\"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(base, org, app, version, "app_metadata.json"),
		[]byte(`{"org":"`+org+`","app_name":"`+app+`","package_version":"`+version+`"}`), 0o644))
}

// isolatedFPMHome points config and the app store at temporary directories, so a
// test never reads the developer's real store.
func isolatedFPMHome(t *testing.T) string {
	t.Helper()
	store := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FPM_APPS_BASE_PATH", store)
	return store
}

// TestPackageProdRefusesAmbientStorePin is issue #14: a prod package must not pin
// required_apps to whatever this machine happens to hold, because the same source
// then produces different packages on different machines and days.
func TestPackageProdRefusesAmbientStorePin(t *testing.T) {
	store := isolatedFPMHome(t)
	seedStore(t, store, "frappe", "erpnext", "15.120.0")
	source := requiresApp(t, t.TempDir(), "hrms", "frappe/erpnext")

	resetPackageCmdFlags()
	out, err := SharedExecuteCommand(rootCmd, "package", source,
		"--version", "16.16.0", "--org", "frappe", "--app-name", "hrms",
		"--bundle-deps=false", "--skip-local-install", "--output-path", t.TempDir())

	require.Error(t, err, "output was: %s", out)
	assert.Contains(t, err.Error(), "erpnext")
	assert.Contains(t, err.Error(), "--requires")
	assert.Contains(t, err.Error(), "--repo")
	assert.Contains(t, err.Error(), "not reproducible")
}

// TestPackageDevStillPinsFromLocalStore: a development package is a local
// iteration artifact, and the local store is exactly what it should use.
func TestPackageDevStillPinsFromLocalStore(t *testing.T) {
	store := isolatedFPMHome(t)
	seedStore(t, store, "frappe", "erpnext", "15.120.0")
	source := requiresApp(t, t.TempDir(), "hrms", "frappe/erpnext")

	meta, _ := runPackageAndGetMeta(t, source, "hrms", "16.16.0", "dev", "--skip-local-install")
	require.Len(t, meta.RequiredApps, 1)
	assert.Equal(t, "15.120.0", meta.RequiredApps[0].Version)
	assert.Equal(t, "local-store", meta.RequiredApps[0].ResolvedFrom)
}

// TestPackageRequiresFlagPinsExactly: an explicit pin needs no source at all, and
// is recorded as having come from the flag so the package stays auditable.
func TestPackageRequiresFlagPinsExactly(t *testing.T) {
	isolatedFPMHome(t)
	source := requiresApp(t, t.TempDir(), "hrms", "frappe/erpnext")

	meta, _ := runPackageAndGetMeta(t, source, "hrms", "16.16.0", "prod",
		"--skip-local-install", "--requires", "frappe/erpnext==16.30.0")

	require.Len(t, meta.RequiredApps, 1)
	pin := meta.RequiredApps[0]
	assert.Equal(t, "frappe", pin.Org)
	assert.Equal(t, "16.30.0", pin.Version)
	assert.Equal(t, "", pin.VersionSpec, "an explicit == is an exact pin")
	assert.Equal(t, "flag:--requires", pin.ResolvedFrom)
	assert.Equal(t, "frappe/erpnext", pin.Requirement)
}

// TestPackageRequiresFlagAcceptsRange records a release line, which is what keeps a
// package installable after the bench takes a patch upgrade.
func TestPackageRequiresFlagAcceptsRange(t *testing.T) {
	isolatedFPMHome(t)
	source := requiresApp(t, t.TempDir(), "hrms", "frappe/erpnext")

	meta, _ := runPackageAndGetMeta(t, source, "hrms", "16.16.0", "prod",
		"--skip-local-install", "--requires", "frappe/erpnext>=16.0.0,<17.0.0")

	require.Len(t, meta.RequiredApps, 1)
	pin := meta.RequiredApps[0]
	assert.Equal(t, ">=16.0.0,<17.0.0", pin.VersionSpec)
	assert.True(t, pin.Accepts("16.30.0"))
	assert.False(t, pin.Accepts("15.120.0"))
	assert.Equal(t, ">=16.0.0,<17.0.0", meta.Dependencies["frappe/erpnext"],
		"the published dependency constraint is the range, not one version")
}

// TestPackageRecordsReleaseLineByDefault: a resolved pin is recorded as its release
// line, so an erpnext patch upgrade does not invalidate this package, while the
// exact version it was built against stays recorded.
func TestPackageRecordsReleaseLineByDefault(t *testing.T) {
	store := isolatedFPMHome(t)
	seedStore(t, store, "frappe", "erpnext", "16.16.0")
	source := requiresApp(t, t.TempDir(), "hrms", "frappe/erpnext")

	meta, _ := runPackageAndGetMeta(t, source, "hrms", "16.16.0", "prod",
		"--skip-local-install", "--requires-from-local-store")

	require.Len(t, meta.RequiredApps, 1)
	pin := meta.RequiredApps[0]
	assert.Equal(t, "16.16.0", pin.Version, "the version built against is still recorded")
	assert.Equal(t, ">=16.0.0-0,<17.0.0", pin.VersionSpec)
	assert.True(t, pin.Accepts("16.30.0"), "a bench that moved on by a patch still satisfies it")
	assert.False(t, pin.Accepts("17.0.0-dev"), "a prerelease of the next line does not")
}

// TestPackageRequiresExactOptsOut keeps the old exact-pin behaviour available.
func TestPackageRequiresExactOptsOut(t *testing.T) {
	store := isolatedFPMHome(t)
	seedStore(t, store, "frappe", "erpnext", "16.16.0")
	source := requiresApp(t, t.TempDir(), "hrms", "frappe/erpnext")

	meta, _ := runPackageAndGetMeta(t, source, "hrms", "16.16.0", "prod",
		"--skip-local-install", "--requires-from-local-store", "--requires-exact")

	require.Len(t, meta.RequiredApps, 1)
	assert.Equal(t, "16.16.0", meta.RequiredApps[0].Version)
	assert.Equal(t, "", meta.RequiredApps[0].VersionSpec)
}

// TestPackageRequiresUnknownAppIsAnError catches a typo in the flag rather than
// silently packaging without the pin the caller thought they gave.
func TestPackageRequiresUnknownAppIsAnError(t *testing.T) {
	isolatedFPMHome(t)
	source := requiresApp(t, t.TempDir(), "hrms", "frappe/erpnext")

	resetPackageCmdFlags()
	_, err := SharedExecuteCommand(rootCmd, "package", source,
		"--version", "16.16.0", "--org", "frappe", "--app-name", "hrms",
		"--bundle-deps=false", "--skip-local-install", "--output-path", t.TempDir(),
		"--requires", "frappe/erpnextt==16.30.0")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not require")
}

func TestParseRequiresOverrides(t *testing.T) {
	pins, err := parseRequiresOverrides([]string{"frappe/erpnext==16.30.0", "payments>=0.0.0-0,<1.0.0", "hrms"})
	require.NoError(t, err)
	require.Len(t, pins, 3)
	assert.Equal(t, "frappe", pins[0].Org)
	assert.Equal(t, "16.30.0", pins[0].Version)
	assert.Equal(t, "payments", pins[1].Name)
	assert.Equal(t, ">=0.0.0-0,<1.0.0", pins[1].VersionSpec)
	assert.Equal(t, "hrms", pins[2].Name)
	assert.Equal(t, "", pins[2].Version, "an unpinned override accepts any version")

	_, err = parseRequiresOverrides([]string{"erpnext==16.0.0", "frappe/erpnext==17.0.0"})
	require.Error(t, err, "one app cannot be pinned twice")

	_, err = parseRequiresOverrides([]string{"erpnext>=not-a-version"})
	require.Error(t, err)
}
