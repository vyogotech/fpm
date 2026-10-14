package assets

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFiles(t *testing.T, root string, files ...string) {
	t.Helper()
	for _, f := range files {
		p := filepath.Join(root, filepath.FromSlash(f))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
	}
}

func TestBundleKey(t *testing.T) {
	cases := map[string]string{
		"marketplace.bundle.6SCSPSGQ.js": "marketplace.bundle.js",
		"desk.bundle.PMBWXH53.css":       "desk.bundle.css",
		"my.app.bundle.ABCDEFGH.js":      "my.app.bundle.js",
	}
	for in, want := range cases {
		got, ok := BundleKey(in)
		require.True(t, ok, in)
		assert.Equal(t, want, got, in)
	}
	_, ok := BundleKey("plain.js")
	assert.False(t, ok)
}

// TestBundles mirrors esbuild.js update_assets_obj: glob public/dist for
// *.bundle.*.{js,css}, drop the hash from the key, prefix /assets/<app>/dist, and
// route -rtl paths to the RTL manifest with an rtl_ key prefix.
func TestBundles(t *testing.T) {
	root := t.TempDir()
	module := filepath.Join(root, "myapp")
	writeFiles(t, module,
		"public/dist/js/myapp.bundle.6SCSPSGQ.js",
		"public/dist/js/myapp.bundle.6SCSPSGQ.js.map", // maps are not entries
		"public/dist/css/myapp.bundle.PMBWXH53.css",
		"public/dist/css-rtl/myapp.bundle.T5LQ2BXA.css",
		"public/dist/js/not-a-bundle.js",
		"public/js/source.bundle.js", // sources outside dist are not built output
	)

	ltr, rtl, err := Bundles(module, "myapp")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"myapp.bundle.js":  "/assets/myapp/dist/js/myapp.bundle.6SCSPSGQ.js",
		"myapp.bundle.css": "/assets/myapp/dist/css/myapp.bundle.PMBWXH53.css",
	}, ltr)
	assert.Equal(t, map[string]string{
		"rtl_myapp.bundle.css": "/assets/myapp/dist/css-rtl/myapp.bundle.T5LQ2BXA.css",
	}, rtl)

	// No dist at all: nothing to record, not an error.
	ltr, rtl, err = Bundles(filepath.Join(root, "other"), "other")
	require.NoError(t, err)
	assert.Empty(t, ltr)
	assert.Empty(t, rtl)
}

// TestBundlesKeyScriptEntriesBySourceName: a real esbuild run keys a script bundle by
// its entry point's basename (write_assets_json: path.basename(info.entryPoint)), so a
// TypeScript or JSX entry keeps its own extension even though the output is .js, and
// hooks.py asks for it by that name (app_include_js = ["frappe_ai.bundle.ts"]).
// Stylesheets are compiled to a temporary <name>.css first, so .scss/.less entries
// are keyed .css. Expected values were taken from frappe v16.33.0's esbuild.
func TestBundlesKeyScriptEntriesBySourceName(t *testing.T) {
	module := filepath.Join(t.TempDir(), "myapp")
	writeFiles(t, module,
		"public/js/typed.bundle.ts",
		"public/js/nested/deep/my.dotted.bundle.ts", // esbuild flattens entry dirs
		"public/js/react.bundle.jsx",
		"public/js/plain.bundle.js",
		"public/scss/sassy.bundle.scss",
		"public/less/lessy.bundle.less",
		"public/dist/js/typed.bundle.DBWEI4ZR.js",
		"public/dist/js/my.dotted.bundle.AMSTKLF6.js",
		"public/dist/react.bundle.OOBQO52T.js", // .jsx output lands in dist/, not dist/js/
		"public/dist/js/plain.bundle.A3KQ2FVU.js",
		"public/dist/css/sassy.bundle.Y77R3MJT.css",
		"public/dist/css/lessy.bundle.XG4ATQTA.css",
		"public/dist/css-rtl/sassy.bundle.XPV4FWHM.css",
	)

	ltr, rtl, err := Bundles(module, "myapp")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"typed.bundle.ts":     "/assets/myapp/dist/js/typed.bundle.DBWEI4ZR.js",
		"my.dotted.bundle.ts": "/assets/myapp/dist/js/my.dotted.bundle.AMSTKLF6.js",
		"react.bundle.jsx":    "/assets/myapp/dist/react.bundle.OOBQO52T.js",
		"plain.bundle.js":     "/assets/myapp/dist/js/plain.bundle.A3KQ2FVU.js",
		"sassy.bundle.css":    "/assets/myapp/dist/css/sassy.bundle.Y77R3MJT.css",
		"lessy.bundle.css":    "/assets/myapp/dist/css/lessy.bundle.XG4ATQTA.css",
	}, ltr)
	assert.Equal(t, map[string]string{
		"rtl_sassy.bundle.css": "/assets/myapp/dist/css-rtl/sassy.bundle.XPV4FWHM.css",
	}, rtl)
}

// TestBundlesWithoutSourcesKeepTheBuiltName: a package that ships only compiled
// output has nothing to recover the entry's extension from, so the key stays the
// built name, as `bench build --using-cached` would record it. Files under dist/ and
// node_modules/ are not entry points and must not decide the key.
func TestBundlesWithoutSourcesKeepTheBuiltName(t *testing.T) {
	module := filepath.Join(t.TempDir(), "myapp")
	writeFiles(t, module,
		"public/dist/js/typed.bundle.DBWEI4ZR.js",
		"public/dist/js/typed.bundle.ts", // stray copy inside dist
		"public/node_modules/somelib/typed.bundle.ts",
	)

	ltr, _, err := Bundles(module, "myapp")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"typed.bundle.js": "/assets/myapp/dist/js/typed.bundle.DBWEI4ZR.js",
	}, ltr)
}

// TestBundlesAmbiguousSourceKeepsTheBuiltName: a .js and a .ts entry with the same
// name compile to the same dist/js output (esbuild.js warns "Duplicate output file"),
// so the output cannot say which one it came from.
func TestBundlesAmbiguousSourceKeepsTheBuiltName(t *testing.T) {
	module := filepath.Join(t.TempDir(), "myapp")
	writeFiles(t, module,
		"public/js/dup.bundle.js",
		"public/ts/dup.bundle.ts",
		"public/dist/js/dup.bundle.AAAAAAAA.js",
	)

	ltr, _, err := Bundles(module, "myapp")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"dup.bundle.js": "/assets/myapp/dist/js/dup.bundle.AAAAAAAA.js",
	}, ltr)
}

// TestManifestFormat pins the exact bytes JSON.stringify(obj, null, 4) produces.
func TestManifestFormat(t *testing.T) {
	m := &Manifest{values: map[string]string{}}
	assert.Equal(t, "{}", string(m.Bytes()))

	m.Set("desk.bundle.js", "/assets/frappe/dist/js/desk.bundle.4NHPGYKM.js")
	m.Set("desk.bundle.css", "/assets/frappe/dist/css/desk.bundle.PMBWXH53.css")
	want := "{\n" +
		"    \"desk.bundle.js\": \"/assets/frappe/dist/js/desk.bundle.4NHPGYKM.js\",\n" +
		"    \"desk.bundle.css\": \"/assets/frappe/dist/css/desk.bundle.PMBWXH53.css\"\n" +
		"}"
	assert.Equal(t, want, string(m.Bytes()))
	assert.False(t, strings.HasSuffix(string(m.Bytes()), "\n"), "JSON.stringify writes no trailing newline")

	// What we write, JavaScript can read back to the same object.
	var back map[string]string
	require.NoError(t, json.Unmarshal(m.Bytes(), &back))
	assert.Equal(t, m.Map(), back)
}

// TestMergePreservesOrderAndOtherApps is the Object.assign contract: existing keys keep
// their position and are overwritten in place, new keys are appended, nothing is pruned.
func TestMergePreservesOrderAndOtherApps(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ManifestFileName)
	existing := "{\n" +
		"    \"libs.bundle.js\": \"/assets/frappe/dist/js/libs.bundle.OLD11111.js\",\n" +
		"    \"myapp.bundle.js\": \"/assets/myapp/dist/js/myapp.bundle.OLD22222.js\",\n" +
		"    \"erpnext.bundle.js\": \"/assets/erpnext/dist/js/erpnext.bundle.6SCSPSGQ.js\"\n" +
		"}"
	require.NoError(t, os.WriteFile(p, []byte(existing), 0o644))

	m, err := ReadManifest(p)
	require.NoError(t, err)
	m.Merge(map[string]string{
		"myapp.bundle.js":  "/assets/myapp/dist/js/myapp.bundle.NEW33333.js",
		"myapp.bundle.css": "/assets/myapp/dist/css/myapp.bundle.NEW44444.css",
	})
	require.NoError(t, m.Write(p))

	got, _ := os.ReadFile(p)
	want := "{\n" +
		"    \"libs.bundle.js\": \"/assets/frappe/dist/js/libs.bundle.OLD11111.js\",\n" +
		"    \"myapp.bundle.js\": \"/assets/myapp/dist/js/myapp.bundle.NEW33333.js\",\n" +
		"    \"erpnext.bundle.js\": \"/assets/erpnext/dist/js/erpnext.bundle.6SCSPSGQ.js\",\n" +
		"    \"myapp.bundle.css\": \"/assets/myapp/dist/css/myapp.bundle.NEW44444.css\"\n" +
		"}"
	assert.Equal(t, want, string(got))
}

func TestReadManifestMissingAndInvalid(t *testing.T) {
	dir := t.TempDir()
	m, err := ReadManifest(filepath.Join(dir, "absent.json"))
	require.NoError(t, err)
	assert.Equal(t, 0, m.Len())

	bad := filepath.Join(dir, "bad.json")
	require.NoError(t, os.WriteFile(bad, []byte(`["not", "an", "object"]`), 0o644))
	_, err = ReadManifest(bad)
	require.Error(t, err)

	nonString := filepath.Join(dir, "nonstring.json")
	require.NoError(t, os.WriteFile(nonString, []byte(`{"a": 1}`), 0o644))
	_, err = ReadManifest(nonString)
	require.Error(t, err)
}

func TestLinkAppAssets(t *testing.T) {
	bench := t.TempDir()
	store := t.TempDir()
	module := filepath.Join(store, "myapp")
	writeFiles(t, module, "public/dist/js/myapp.bundle.AAAAAAAA.js", "docs/index.md")
	writeFiles(t, store, "node_modules/x/index.js")

	// A stale real directory (what the old copy-based deploy left behind) and a
	// dangling link must both be cleaned up, as frappe does.
	assetsDir := AssetsDir(bench)
	writeFiles(t, assetsDir, "myapp/dist/js/stale.bundle.OLDOLDOL.js")
	require.NoError(t, os.Symlink(filepath.Join(bench, "nowhere"), filepath.Join(assetsDir, "dangling")))

	require.NoError(t, LinkAppAssets(bench, "myapp", module))

	for _, sub := range []string{"js", "css"} {
		info, err := os.Stat(filepath.Join(assetsDir, sub))
		require.NoError(t, err)
		assert.True(t, info.IsDir())
	}
	target, err := os.Readlink(filepath.Join(assetsDir, "myapp"))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(module, "public"), target)
	assert.True(t, filepath.IsAbs(target), "frappe writes absolute link targets")

	nm, err := os.Readlink(filepath.Join(assetsDir, "myapp", "node_modules"))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(store, "node_modules"), nm)

	docs, err := os.Readlink(filepath.Join(assetsDir, "myapp_docs"))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(module, "docs"), docs)

	_, err = os.Lstat(filepath.Join(assetsDir, "dangling"))
	assert.True(t, os.IsNotExist(err), "dangling link removed")

	// The built file is now served through the link.
	_, err = os.Stat(filepath.Join(assetsDir, "myapp", "dist", "js", "myapp.bundle.AAAAAAAA.js"))
	assert.NoError(t, err)

	// Idempotent.
	require.NoError(t, LinkAppAssets(bench, "myapp", module))
}

func TestLinkAppAssetsWithoutPublicDir(t *testing.T) {
	bench := t.TempDir()
	module := filepath.Join(t.TempDir(), "noassets")
	writeFiles(t, module, "hooks.py")
	require.NoError(t, LinkAppAssets(bench, "noassets", module))
	_, err := os.Lstat(filepath.Join(AssetsDir(bench), "noassets"))
	assert.True(t, os.IsNotExist(err))
}

func TestDeployEndToEnd(t *testing.T) {
	bench := t.TempDir()
	module := filepath.Join(t.TempDir(), "myapp")
	writeFiles(t, module,
		"public/dist/js/myapp.bundle.6SCSPSGQ.js",
		"public/dist/css-rtl/myapp.bundle.T5LQ2BXA.css",
	)
	// Another app's entries already present.
	require.NoError(t, os.MkdirAll(AssetsDir(bench), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(AssetsDir(bench), ManifestFileName),
		[]byte("{\n    \"libs.bundle.js\": \"/assets/frappe/dist/js/libs.bundle.WGSJP7XT.js\"\n}"), 0o644))

	d, err := Deploy(bench, "myapp", module, nil)
	require.NoError(t, err)
	assert.True(t, d.Linked)
	assert.Equal(t, "/assets/myapp/dist/js/myapp.bundle.6SCSPSGQ.js", d.LTR["myapp.bundle.js"])

	ltr, err := ReadManifest(filepath.Join(AssetsDir(bench), ManifestFileName))
	require.NoError(t, err)
	assert.Equal(t, []string{"libs.bundle.js", "myapp.bundle.js"}, ltr.Keys())

	rtl, err := ReadManifest(filepath.Join(AssetsDir(bench), RTLManifestFileName))
	require.NoError(t, err)
	v, _ := rtl.Get("rtl_myapp.bundle.css")
	assert.Equal(t, "/assets/myapp/dist/css-rtl/myapp.bundle.T5LQ2BXA.css", v)

	// The served path resolves through the link to the real built file.
	_, err = os.Stat(filepath.Join(bench, "sites", strings.TrimPrefix(d.LTR["myapp.bundle.js"], "/")))
	assert.NoError(t, err)
}

// TestInvalidateCache speaks RESP to a fake redis and checks the exact command frappe
// sends: DEL assets_json, unprefixed.
func TestInvalidateCache(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	received := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 256)
		n, _ := conn.Read(buf)
		received <- string(buf[:n])
		conn.Write([]byte(":1\r\n"))
	}()

	bench := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(bench, "sites"), 0o755))
	cfg := `{"redis_cache": "redis://` + ln.Addr().String() + `"}`
	require.NoError(t, os.WriteFile(filepath.Join(bench, "sites", "common_site_config.json"), []byte(cfg), 0o644))

	require.NoError(t, InvalidateCache(bench))
	assert.Equal(t, "*2\r\n$3\r\nDEL\r\n$11\r\nassets_json\r\n", <-received)

	// Unreachable redis is reported, not swallowed, so the caller can warn.
	ln.Close()
	assert.Error(t, InvalidateCache(bench))
}

func TestManifest_Delete(t *testing.T) {
	m := &Manifest{values: map[string]string{}}
	m.Set("a", "1")
	m.Set("b", "2")
	m.Set("c", "3")

	assert.Equal(t, []string{"a", "b", "c"}, m.Keys())
	m.Delete("b")
	assert.Equal(t, []string{"a", "c"}, m.Keys())
	_, ok := m.Get("b")
	assert.False(t, ok)

	// Deleting nonexistent key is a no-op
	m.Delete("nonexistent")
	assert.Equal(t, []string{"a", "c"}, m.Keys())
}

func TestUndeploy(t *testing.T) {
	bench := t.TempDir()
	assetsDir := AssetsDir(bench)
	require.NoError(t, os.MkdirAll(assetsDir, 0o755))

	// Deploy app1 and app2 manifest entries
	p := filepath.Join(assetsDir, ManifestFileName)
	manifestData := "{\n" +
		"    \"app1.bundle.js\": \"/assets/app1/dist/js/app1.111.js\",\n" +
		"    \"app2.bundle.js\": \"/assets/app2/dist/js/app2.222.js\"\n" +
		"}"
	require.NoError(t, os.WriteFile(p, []byte(manifestData), 0o644))

	// Create symlinks for app1 and app2
	app1Dir := filepath.Join(assetsDir, "app1")
	app2Dir := filepath.Join(assetsDir, "app2")
	require.NoError(t, os.MkdirAll(app1Dir, 0o755))
	require.NoError(t, os.MkdirAll(app2Dir, 0o755))

	// Undeploy app1
	require.NoError(t, Undeploy(bench, "app1"))

	// Verify app1 symlink is removed, app2 symlink is kept
	_, err := os.Lstat(app1Dir)
	assert.True(t, os.IsNotExist(err), "app1 dir/symlink should be removed")
	_, err = os.Lstat(app2Dir)
	assert.NoError(t, err, "app2 dir/symlink should remain")

	// Verify app1 is deleted from manifest, app2 remains
	m, err := ReadManifest(p)
	require.NoError(t, err)
	assert.Equal(t, []string{"app2.bundle.js"}, m.Keys())
	val, ok := m.Get("app2.bundle.js")
	assert.True(t, ok)
	assert.Equal(t, "/assets/app2/dist/js/app2.222.js", val)
}

// TestBundlesUseRecordedKeysWhenSourcesAreNotShipped: a prod package leaves its
// entry points out, so the built output alone cannot say that typed.bundle.HASH.js
// came from typed.bundle.ts. The package recorded that when it was built.
func TestBundlesUseRecordedKeysWhenSourcesAreNotShipped(t *testing.T) {
	module := filepath.Join(t.TempDir(), "myapp")
	writeFiles(t, module,
		"public/dist/js/typed.bundle.DBWEI4ZR.js",
		"public/dist/react.bundle.OOBQO52T.js",
		"public/dist/css/sassy.bundle.Y77R3MJT.css",
		"public/dist/css-rtl/sassy.bundle.XPV4FWHM.css",
	)
	recorded := map[string]string{
		"typed.bundle.ts":      "/assets/myapp/dist/js/typed.bundle.DBWEI4ZR.js",
		"react.bundle.jsx":     "/assets/myapp/dist/react.bundle.OOBQO52T.js",
		"sassy.bundle.css":     "/assets/myapp/dist/css/sassy.bundle.Y77R3MJT.css",
		"rtl_sassy.bundle.css": "/assets/myapp/dist/css-rtl/sassy.bundle.XPV4FWHM.css",
		// Recorded for a file the package does not hold: never point assets.json at it.
		"gone.bundle.ts": "/assets/myapp/dist/js/gone.bundle.AAAAAAAA.js",
	}

	ltr, rtl, err := BundlesWithRecorded(module, "myapp", recorded)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"typed.bundle.ts":  "/assets/myapp/dist/js/typed.bundle.DBWEI4ZR.js",
		"react.bundle.jsx": "/assets/myapp/dist/react.bundle.OOBQO52T.js",
		"sassy.bundle.css": "/assets/myapp/dist/css/sassy.bundle.Y77R3MJT.css",
	}, ltr)
	assert.Equal(t, map[string]string{
		"rtl_sassy.bundle.css": "/assets/myapp/dist/css-rtl/sassy.bundle.XPV4FWHM.css",
	}, rtl)
}

// TestBundlesPreferShippedSourcesOverRecordedKeys: packages built before fpm keyed
// bundles by their source recorded `frappe_ai.bundle.js` for a .ts entry, but they
// still ship the entry, and the entry is the truth.
func TestBundlesPreferShippedSourcesOverRecordedKeys(t *testing.T) {
	module := filepath.Join(t.TempDir(), "frappe_ai")
	writeFiles(t, module,
		"public/js/frappe_ai.bundle.ts",
		"public/dist/js/frappe_ai.bundle.SVDF5L4P.js",
	)
	recorded := map[string]string{
		"frappe_ai.bundle.js": "/assets/frappe_ai/dist/js/frappe_ai.bundle.SVDF5L4P.js",
	}

	ltr, _, err := BundlesWithRecorded(module, "frappe_ai", recorded)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"frappe_ai.bundle.ts": "/assets/frappe_ai/dist/js/frappe_ai.bundle.SVDF5L4P.js",
	}, ltr)
}
