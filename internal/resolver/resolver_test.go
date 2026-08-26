package resolver

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"fpm/internal/config"
	"fpm/internal/metadata"
	"fpm/internal/repository"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// storeApp extracts a fake package into the store layout fpm install uses.
func storeApp(t *testing.T, base, org, app, version string, required ...metadata.RequiredApp) {
	t.Helper()
	dir := filepath.Join(base, org, app, version)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, app), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, app, "hooks.py"), []byte("app_name = \""+app+"\"\n"), 0o644))
	require.NoError(t, metadata.SaveAppMetadata(dir, &metadata.AppMetadata{
		Org: org, AppName: app, PackageVersion: version, RequiredApps: required,
	}))
}

func TestStoreHelpers(t *testing.T) {
	base := t.TempDir()
	storeApp(t, base, "acme", "erpnext", "15.2.0")
	storeApp(t, base, "acme", "erpnext", "15.10.0")
	require.NoError(t, os.MkdirAll(filepath.Join(base, "acme", "erpnext", "broken"), 0o755)) // no module inside

	assert.Equal(t, []string{"15.2.0", "15.10.0"}, StoreVersions(base, "acme", "erpnext"), "semver order, broken dir ignored")
	assert.True(t, InStore(base, "acme", "erpnext", "15.10.0"))
	assert.False(t, InStore(base, "acme", "erpnext", "broken"))
	assert.Equal(t, []string{"acme"}, StoreOrgs(base, "erpnext"))
	assert.Nil(t, StoreOrgs(base, "nothing"))
}

func TestResolveRequiredAppsFromLocalStore(t *testing.T) {
	base := t.TempDir()
	storeApp(t, base, "frappe", "erpnext", "15.2.0")
	storeApp(t, base, "frappe", "erpnext", "15.10.0")
	storeApp(t, base, "acme", "payments", "1.0.0")
	cfg := &config.FPMConfig{AppsBasePath: base}

	pins, err := ResolveRequiredApps([]string{"frappe", "erpnext", "https://github.com/acme/payments.git@main", "erpnext"}, Options{Cfg: cfg})
	require.NoError(t, err)
	require.Len(t, pins, 2, "frappe skipped, duplicate erpnext collapsed")
	assert.Equal(t, "frappe/erpnext==15.10.0", pins[0].Identifier(), "latest by semver, not string order")
	assert.Equal(t, "local-store", pins[0].ResolvedFrom)
	assert.Equal(t, "erpnext", pins[0].Requirement)
	assert.Equal(t, "acme/payments==1.0.0", pins[1].Identifier())
	assert.Equal(t, "https://github.com/acme/payments.git@main", pins[1].Requirement)
}

func TestResolveRequiredAppsFailsHard(t *testing.T) {
	cfg := &config.FPMConfig{AppsBasePath: t.TempDir()}
	pins, err := ResolveRequiredApps([]string{"erpnext", "hrms"}, Options{Cfg: cfg})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrUnresolved))
	assert.Contains(t, err.Error(), "erpnext")
	assert.Contains(t, err.Error(), "hrms", "every failure is reported, not just the first")
	assert.Empty(t, pins)
}

func TestResolveRequiredAppsAmbiguousOrg(t *testing.T) {
	base := t.TempDir()
	storeApp(t, base, "one", "shared", "1.0.0")
	storeApp(t, base, "two", "shared", "1.0.0")
	cfg := &config.FPMConfig{AppsBasePath: base}
	_, err := ResolveRequiredApps([]string{"shared"}, Options{Cfg: cfg})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ambiguous")

	pins, err := ResolveRequiredApps([]string{"two/shared"}, Options{Cfg: cfg})
	require.NoError(t, err)
	assert.Equal(t, "two/shared==1.0.0", pins[0].Identifier())
}

func TestResolveRequiredAppsFromRepository(t *testing.T) {
	cfg := &config.FPMConfig{
		AppsBasePath: t.TempDir(),
		Repositories: map[string]config.RepositoryConfig{
			"main": {Name: "main", URL: "http://repo.example", Priority: 1},
		},
	}
	metaByOrg := map[string]*repository.PackageMetadata{
		"frappe/erpnext": {Org: "frappe", AppName: "erpnext", LatestVersion: "15.10.0",
			Versions: map[string]repository.PackageVersionMetadata{"15.10.0": {}, "15.2.0": {}}},
		"acme/hrms": {Org: "acme", AppName: "hrms",
			Versions: map[string]repository.PackageVersionMetadata{"1.0.0": {}, "1.2.0": {}}},
	}
	opts := Options{
		Cfg: cfg, Remote: true,
		fetchMetadata: func(_ config.RepositoryConfig, org, app string) (*repository.PackageMetadata, bool, error) {
			m, ok := metaByOrg[org+"/"+app]
			return m, ok, nil
		},
		fetchIndex: func(config.RepositoryConfig) (*repository.RepositoryIndex, bool, error) {
			return &repository.RepositoryIndex{Packages: []repository.IndexEntry{
				{Org: "frappe", AppName: "erpnext"}, {Org: "acme", AppName: "hrms"},
			}}, true, nil
		},
	}

	pins, err := ResolveRequiredApps([]string{"erpnext", "acme/hrms"}, opts)
	require.NoError(t, err)
	require.Len(t, pins, 2)
	assert.Equal(t, "frappe/erpnext==15.10.0", pins[0].Identifier(), "org found via index, version from latest_version")
	assert.Equal(t, "repo:main", pins[0].ResolvedFrom)
	assert.Equal(t, "acme/hrms==1.2.0", pins[1].Identifier(), "no latest_version: highest published")

	_, err = ResolveRequiredApps([]string{"nothing"}, opts)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrUnresolved))

	// Without --remote, repositories are never consulted.
	_, err = ResolveRequiredApps([]string{"erpnext"}, Options{Cfg: cfg})
	require.Error(t, err)
}

func TestCheckLocalClosure(t *testing.T) {
	base := t.TempDir()
	// custom -> erpnext -> payments ; custom -> hrms (missing)
	storeApp(t, base, "acme", "payments", "1.0.0")
	storeApp(t, base, "frappe", "erpnext", "15.10.0",
		metadata.RequiredApp{Name: "payments", Org: "acme", Version: "1.0.0"},
		metadata.RequiredApp{Name: "frappe"})

	required := []metadata.RequiredApp{
		{Name: "erpnext", Org: "frappe", Version: "15.10.0"},
		{Name: "hrms", Org: "frappe", Version: "16.0.0"},
		{Name: "frappe"},
	}
	closure, missing, err := CheckLocalClosure(base, required, "acme/custom==1.0.0")
	require.NoError(t, err)

	require.Len(t, closure, 2)
	assert.Equal(t, "acme/payments==1.0.0", closure[0].App.Identifier(), "deepest dependency first")
	assert.Equal(t, "frappe/erpnext==15.10.0", closure[0].RequiredBy)
	assert.Equal(t, "frappe/erpnext==15.10.0", closure[1].App.Identifier())
	assert.True(t, closure[1].Present)

	require.Len(t, missing, 1)
	assert.Equal(t, "frappe/hrms==16.0.0", missing[0].App.Identifier())
	assert.Equal(t, "acme/custom==1.0.0", missing[0].RequiredBy)

	err = MissingError("acme/custom==1.0.0", missing)
	assert.True(t, errors.Is(err, ErrMissing))
	assert.Contains(t, err.Error(), "frappe/hrms==16.0.0")
	assert.Contains(t, err.Error(), "nothing is fetched")
}

func TestCheckLocalClosureWrongVersionAndUnpinned(t *testing.T) {
	base := t.TempDir()
	storeApp(t, base, "frappe", "erpnext", "15.2.0")

	_, missing, err := CheckLocalClosure(base, []metadata.RequiredApp{{Name: "erpnext", Org: "frappe", Version: "15.10.0"}}, "x")
	require.NoError(t, err)
	require.Len(t, missing, 1)
	assert.Contains(t, missing[0].Reason, "have 15.2.0", "an exact pin is required; a different version does not satisfy it")

	closure, missing, err := CheckLocalClosure(base, []metadata.RequiredApp{{Name: "erpnext"}}, "x")
	require.NoError(t, err)
	assert.Empty(t, missing)
	require.Len(t, closure, 1)
	assert.Equal(t, "frappe/erpnext==15.2.0", closure[0].App.Identifier(), "unpinned, unqualified requirement resolves to what the store has")
}

func TestCheckLocalClosureCycle(t *testing.T) {
	base := t.TempDir()
	storeApp(t, base, "a", "one", "1.0.0", metadata.RequiredApp{Name: "two", Org: "a", Version: "1.0.0"})
	storeApp(t, base, "a", "two", "1.0.0", metadata.RequiredApp{Name: "one", Org: "a", Version: "1.0.0"})
	closure, missing, err := CheckLocalClosure(base, []metadata.RequiredApp{{Name: "one", Org: "a", Version: "1.0.0"}}, "root")
	require.NoError(t, err)
	assert.Empty(t, missing)
	assert.Len(t, closure, 2, "a cycle is visited once, not forever")
}

// benchApp puts an app into a bench the way bench get-app would leave it.
func benchApp(t *testing.T, bench, name, version string) {
	t.Helper()
	module := filepath.Join(bench, "apps", name, name)
	require.NoError(t, os.MkdirAll(module, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(module, "hooks.py"), []byte("app_name = \""+name+"\"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(module, "__init__.py"), []byte("__version__ = \""+version+"\"\n"), 0o644))
}

// TestResolveFromBench: an app already in the build bench (an image's erpnext, say)
// pins to the version its module declares, after the store and before repositories.
func TestResolveFromBench(t *testing.T) {
	bench := t.TempDir()
	benchApp(t, bench, "erpnext", "17.0.0-dev")
	cfg := &config.FPMConfig{AppsBasePath: t.TempDir()}

	pins, err := ResolveRequiredApps([]string{"frappe/erpnext"}, Options{Cfg: cfg, BenchPath: bench})
	require.NoError(t, err)
	require.Len(t, pins, 1)
	assert.Equal(t, "frappe/erpnext==17.0.0-dev", pins[0].Identifier())
	assert.Equal(t, "bench:"+bench, pins[0].ResolvedFrom)

	v, ok := BenchAppVersion(bench, "erpnext")
	assert.True(t, ok)
	assert.Equal(t, "17.0.0-dev", v)
	_, ok = BenchAppVersion(bench, "nothing")
	assert.False(t, ok)

	_, err = ResolveRequiredApps([]string{"hrms"}, Options{Cfg: cfg, BenchPath: bench})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "or bench")
}

// TestCheckClosureBenchProvided: at install time an app present in the bench at the
// pinned version satisfies the requirement without a package; a different version
// does not.
func TestCheckClosureBenchProvided(t *testing.T) {
	bench := t.TempDir()
	benchApp(t, bench, "erpnext", "17.0.0-dev")
	store := t.TempDir()

	closure, missing, err := CheckClosure(store, bench, []metadata.RequiredApp{{Name: "erpnext", Org: "frappe", Version: "17.0.0-dev"}}, "frappe/hrms==17.0.0-dev")
	require.NoError(t, err)
	assert.Empty(t, missing)
	require.Len(t, closure, 1)
	assert.True(t, closure[0].ProvidedByBench)
	assert.Equal(t, filepath.Join(bench, "apps", "erpnext"), closure[0].StorePath)

	_, missing, err = CheckClosure(store, bench, []metadata.RequiredApp{{Name: "erpnext", Org: "frappe", Version: "16.0.0"}}, "x")
	require.NoError(t, err)
	require.Len(t, missing, 1)
	assert.Contains(t, missing[0].Reason, `bench has erpnext version "17.0.0-dev"`)

	// Unpinned requirement: whatever the bench has.
	closure, missing, err = CheckClosure(store, bench, []metadata.RequiredApp{{Name: "erpnext"}}, "x")
	require.NoError(t, err)
	assert.Empty(t, missing)
	assert.Equal(t, "17.0.0-dev", closure[0].App.Version)

	// The store still wins when it has the exact pin.
	storeApp(t, store, "frappe", "erpnext", "17.0.0-dev")
	closure, _, err = CheckClosure(store, bench, []metadata.RequiredApp{{Name: "erpnext", Org: "frappe", Version: "17.0.0-dev"}}, "x")
	require.NoError(t, err)
	assert.False(t, closure[0].ProvidedByBench)
}
