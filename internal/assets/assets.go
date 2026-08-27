// Package assets deploys a packaged Frappe app's built JS/CSS into a bench exactly
// the way `bench build` leaves them, so Frappe serves them without a rebuild.
//
// This is a port of the relevant parts of frappe/build.py and esbuild/esbuild.js
// (Frappe v15+/develop), not a new scheme:
//
//   - `bench build` writes one global manifest, <bench>/sites/assets/assets.json, plus
//     <bench>/sites/assets/assets-rtl.json for right-to-left stylesheets. There is no
//     per-app fragment. Each key is a bundle's source name (`desk.bundle.js`,
//     `desk.bundle.css`, `rtl_desk.bundle.css`) and each value the hashed path Frappe
//     serves (`/assets/<app>/dist/js/desk.bundle.4NHPGYKM.js`).
//   - The build merges into the existing manifest (`Object.assign({}, existing, new)`)
//     and never prunes: keys of other apps are preserved verbatim. `bench build
//     --app X` only touches X's keys. The file is written with
//     `JSON.stringify(obj, null, 4)`: four-space indent, no trailing newline.
//   - With `--using-cached` the build does not run esbuild at all. It reconstructs
//     the entries by globbing `apps/<app>/<app>/public/dist/**/*.bundle.*.{js,css}`:
//     key = filename with the hash segment removed, value = `/assets/<app>/dist/<rel>`,
//     entries whose path contains `-rtl` go to assets-rtl.json under an `rtl_` prefix.
//     That is the algorithm Bundles implements.
//   - Built files live inside the app: `apps/<app>/<app>/public/dist/`. They are served
//     because `frappe.build.make_asset_dirs` symlinks `sites/assets/<app>` to
//     `apps/<app>/<app>/public` (and `sites/assets/<app>/node_modules`,
//     `sites/assets/<app>_docs` when those exist). LinkAppAssets replicates that.
//   - Frappe caches the merged manifest in redis_cache under the unprefixed key
//     `assets_json`; the build deletes that key. InvalidateCache does the same.
package assets

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ManifestFileName is the LTR manifest, relative to <bench>/sites/assets.
const ManifestFileName = "assets.json"

// RTLManifestFileName is the RTL manifest, relative to <bench>/sites/assets.
const RTLManifestFileName = "assets-rtl.json"

// AssetsDir returns <bench>/sites/assets.
func AssetsDir(benchPath string) string {
	return filepath.Join(benchPath, "sites", "assets")
}

// Bundles scans <appModuleDir>/public/dist for built bundles and returns the manifest
// entries frappe's `bench build --using-cached` records for the app: ltr entries for
// assets.json and rtl entries (already `rtl_`-prefixed) for assets-rtl.json.
//
// Mirrors esbuild.js update_assets_obj: for `js/x.bundle.HASH.js` the key is
// `x.bundle.js` and the value `/assets/<app>/dist/js/x.bundle.HASH.js`. Entries are
// returned in sorted path order so a deployment is deterministic.
func Bundles(appModuleDir, appName string) (ltr, rtl map[string]string, err error) {
	ltr, rtl = map[string]string{}, map[string]string{}
	dist := filepath.Join(appModuleDir, "public", "dist")
	if info, statErr := os.Stat(dist); statErr != nil || !info.IsDir() {
		return ltr, rtl, nil
	}

	var files []string
	err = filepath.WalkDir(dist, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(dist, p)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if isBundleFile(rel) {
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to scan built assets in %s: %w", dist, err)
	}
	sort.Strings(files)

	prefix := path.Join("/", "assets", appName, "dist")
	for _, file := range files {
		key, ok := BundleKey(path.Base(file))
		if !ok {
			continue
		}
		value := path.Join(prefix, file)
		if strings.Contains(file, "-rtl") {
			rtl["rtl_"+key] = value
		} else {
			ltr[key] = value
		}
	}
	return ltr, rtl, nil
}

// isBundleFile matches the glob `**/*.bundle.*.{js,css}`.
func isBundleFile(rel string) bool {
	base := path.Base(rel)
	if !strings.HasSuffix(base, ".js") && !strings.HasSuffix(base, ".css") {
		return false
	}
	// name.bundle.HASH.ext -> at least four dot-separated parts with "bundle" second last-but-one.
	parts := strings.Split(base, ".")
	if len(parts) < 4 {
		return false
	}
	return parts[len(parts)-3] == "bundle"
}

// BundleKey derives the manifest key from a built filename, dropping the hash
// segment: `marketplace.bundle.6SCSPSGQ.js` -> `marketplace.bundle.js`.
func BundleKey(base string) (string, bool) {
	parts := strings.Split(base, ".")
	if len(parts) < 3 {
		return "", false
	}
	key := append(append([]string{}, parts[:len(parts)-2]...), parts[len(parts)-1])
	return strings.Join(key, "."), true
}

// Manifest is assets.json with its key order preserved, because Frappe writes the
// file from a JavaScript object and object key order is what `bench build` leaves
// behind: existing keys stay where they were, new keys are appended.
type Manifest struct {
	keys   []string
	values map[string]string
}

// ReadManifest loads a manifest. A missing file is an empty manifest, as in
// esbuild.js get_assets_json_path_and_obj.
func ReadManifest(p string) (*Manifest, error) {
	m := &Manifest{values: map[string]string{}}
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return m, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", p, err)
	}
	if err := m.parse(data); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", p, err)
	}
	return m, nil
}

// parse decodes a flat JSON object of strings while recording key order.
func (m *Manifest) parse(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("expected a JSON object")
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("expected a string key")
		}
		var value string
		if err := dec.Decode(&value); err != nil {
			return fmt.Errorf("value of %q is not a string: %w", key, err)
		}
		m.Set(key, value)
	}
	if _, err := dec.Token(); err != nil { // closing brace
		return err
	}
	return nil
}

// Set assigns a value, appending the key if new — the `Object.assign` semantics.
func (m *Manifest) Set(key, value string) {
	if _, exists := m.values[key]; !exists {
		m.keys = append(m.keys, key)
	}
	m.values[key] = value
}

// Delete removes a key from the manifest if present, preserving relative order of others.
func (m *Manifest) Delete(key string) {
	if _, exists := m.values[key]; !exists {
		return
	}
	delete(m.values, key)
	newKeys := make([]string, 0, len(m.keys)-1)
	for _, k := range m.keys {
		if k != key {
			newKeys = append(newKeys, k)
		}
	}
	m.keys = newKeys
}

// Get returns a value.
func (m *Manifest) Get(key string) (string, bool) {
	v, ok := m.values[key]
	return v, ok
}

// Keys returns keys in file order.
func (m *Manifest) Keys() []string { return append([]string(nil), m.keys...) }

// Len reports the number of entries.
func (m *Manifest) Len() int { return len(m.keys) }

// Map returns a copy of the entries.
func (m *Manifest) Map() map[string]string {
	out := make(map[string]string, len(m.values))
	for k, v := range m.values {
		out[k] = v
	}
	return out
}

// Merge applies entries in sorted key order. Existing keys are overwritten in place.
func (m *Manifest) Merge(entries map[string]string) {
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		m.Set(k, entries[k])
	}
}

// Bytes renders the manifest as `JSON.stringify(obj, null, 4)` does: four-space
// indent, `"key": "value"` pairs, and no trailing newline.
func (m *Manifest) Bytes() []byte {
	if len(m.keys) == 0 {
		return []byte("{}")
	}
	var b bytes.Buffer
	b.WriteString("{\n")
	for i, k := range m.keys {
		b.WriteString("    ")
		b.Write(jsString(k))
		b.WriteString(": ")
		b.Write(jsString(m.values[k]))
		if i < len(m.keys)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("}")
	return b.Bytes()
}

// jsString encodes a string the way JSON.stringify does (no HTML escaping).
func jsString(s string) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return bytes.TrimRight(b.Bytes(), "\n")
}

// Write saves the manifest, creating the directory if needed.
func (m *Manifest) Write(p string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("failed to create %s: %w", filepath.Dir(p), err)
	}
	if err := os.WriteFile(p, m.Bytes(), 0o644); err != nil {
		return fmt.Errorf("failed to write %s: %w", p, err)
	}
	return nil
}

// MergeManifests merges an app's entries into both bench manifests, writing each the
// way esbuild.js does (both files are always written, even when empty, matching
// update_assets_json_from_built_assets).
func MergeManifests(benchPath string, ltr, rtl map[string]string) error {
	dir := AssetsDir(benchPath)
	for _, f := range []struct {
		name    string
		entries map[string]string
	}{{ManifestFileName, ltr}, {RTLManifestFileName, rtl}} {
		p := filepath.Join(dir, f.name)
		m, err := ReadManifest(p)
		if err != nil {
			return err
		}
		m.Merge(f.entries)
		if err := m.Write(p); err != nil {
			return err
		}
	}
	return nil
}

// LinkAppAssets replicates frappe.build.make_asset_dirs for one app:
//
//	sites/assets/js, sites/assets/css     created
//	sites/assets/<app>                    -> <appModuleDir>/public
//	sites/assets/<app>/node_modules       -> <appModuleDir>/../node_modules  (if present)
//	sites/assets/<app>_docs               -> <appModuleDir>/docs or www/docs (if present)
//
// An existing real directory at the link path is removed first, as
// frappe.build.link_assets_dir does, and dangling links directly under
// sites/assets are cleared. Targets are absolute paths, as frappe writes them.
func LinkAppAssets(benchPath, appName, appModuleDir string) error {
	assetsDir := AssetsDir(benchPath)
	for _, sub := range []string{"js", "css"} {
		if err := os.MkdirAll(filepath.Join(assetsDir, sub), 0o755); err != nil {
			return fmt.Errorf("failed to create %s: %w", filepath.Join(assetsDir, sub), err)
		}
	}
	if err := clearBrokenSymlinks(assetsDir); err != nil {
		return err
	}

	absModule, err := filepath.Abs(appModuleDir)
	if err != nil {
		return err
	}
	public := filepath.Join(absModule, "public")
	if info, err := os.Stat(public); err != nil || !info.IsDir() {
		// An app without a public directory has nothing to serve; frappe skips it.
		return nil
	}
	if err := linkAssetsDir(public, filepath.Join(assetsDir, appName)); err != nil {
		return err
	}

	nodeModules := filepath.Join(filepath.Dir(absModule), "node_modules")
	if info, err := os.Stat(nodeModules); err == nil && info.IsDir() {
		if err := linkAssetsDir(nodeModules, filepath.Join(assetsDir, appName, "node_modules")); err != nil {
			return err
		}
	}

	for _, docs := range []string{filepath.Join(absModule, "docs"), filepath.Join(absModule, "www", "docs")} {
		if info, err := os.Stat(docs); err == nil && info.IsDir() {
			if err := linkAssetsDir(docs, filepath.Join(assetsDir, appName+"_docs")); err != nil {
				return err
			}
			break
		}
	}
	return nil
}

// linkAssetsDir mirrors frappe.build.link_assets_dir + symlink(overwrite=True):
// remove whatever is at target, then create the link atomically via a temp name.
func linkAssetsDir(source, target string) error {
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			if err := os.Remove(target); err != nil {
				return fmt.Errorf("failed to remove old link %s: %w", target, err)
			}
		} else if err := os.RemoveAll(target); err != nil {
			return fmt.Errorf("failed to remove %s: %w", target, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	tmp := target + ".fpm-tmp"
	os.Remove(tmp)
	if err := os.Symlink(source, tmp); err != nil {
		return fmt.Errorf("failed to symlink %s -> %s: %w", target, source, err)
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("failed to move link into place at %s: %w", target, err)
	}
	return nil
}

// clearBrokenSymlinks mirrors frappe.build.clear_broken_symlinks.
func clearBrokenSymlinks(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.Type()&os.ModeSymlink == 0 {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if _, err := os.Stat(p); err != nil {
			if err := os.Remove(p); err != nil {
				return fmt.Errorf("failed to remove broken link %s: %w", p, err)
			}
		}
	}
	return nil
}

// Deployed reports what Deploy wrote.
type Deployed struct {
	Linked bool
	LTR    map[string]string
	RTL    map[string]string
}

// Deploy makes a packaged app's assets servable from benchPath: it links
// sites/assets/<app> to the app's public directory and records the app's built
// bundles in both manifests. It does exactly what `bench build --app <app>
// --using-cached` does with a prebuilt public/dist, and nothing more.
func Deploy(benchPath, appName, appModuleDir string) (Deployed, error) {
	var out Deployed
	if err := LinkAppAssets(benchPath, appName, appModuleDir); err != nil {
		return out, err
	}
	if _, err := os.Lstat(filepath.Join(AssetsDir(benchPath), appName)); err == nil {
		out.Linked = true
	}
	ltr, rtl, err := Bundles(appModuleDir, appName)
	if err != nil {
		return out, err
	}
	out.LTR, out.RTL = ltr, rtl
	if err := MergeManifests(benchPath, ltr, rtl); err != nil {
		return out, err
	}
	return out, nil
}

// Undeploy removes an app's deployed assets from benchPath during rollback:
// 1. Removes symlinks under sites/assets/ (<app>, <app>_docs, <app>/node_modules).
// 2. Removes entries from assets.json and assets-rtl.json whose value path starts
//    with "/assets/<appName>/dist/" or "/assets/<appName>/".
// 3. Invalidates the redis_cache assets_json key.
func Undeploy(benchPath, appName string) error {
	assetsDir := AssetsDir(benchPath)

	// 1. Remove symlinks
	for _, link := range []string{
		filepath.Join(assetsDir, appName),
		filepath.Join(assetsDir, appName+"_docs"),
	} {
		if _, err := os.Lstat(link); err == nil {
			_ = os.RemoveAll(link)
		}
	}

	// 2. Remove entries from manifests
	prefix := fmt.Sprintf("/assets/%s/", appName)
	for _, manifestName := range []string{ManifestFileName, RTLManifestFileName} {
		p := filepath.Join(assetsDir, manifestName)
		m, err := ReadManifest(p)
		if err != nil || m.Len() == 0 {
			continue
		}
		toDelete := make([]string, 0)
		for _, key := range m.Keys() {
			val, _ := m.Get(key)
			if strings.HasPrefix(val, prefix) {
				toDelete = append(toDelete, key)
			}
		}
		if len(toDelete) > 0 {
			for _, key := range toDelete {
				m.Delete(key)
			}
			_ = m.Write(p)
		}
	}

	// 3. Best-effort cache invalidation
	_ = InvalidateCache(benchPath)
	return nil
}

// InvalidateCache deletes the `assets_json` key from the bench's redis_cache, as
// esbuild.js update_assets_json_in_cache does, so a running Frappe picks up the new
// manifest. It is best effort: a bench whose redis is down (or not yet started, as
// during an image build) is not an error for the caller to fail on, so the returned
// error is meant to be reported as a warning.
func InvalidateCache(benchPath string) error {
	addr, password, err := redisCacheAddr(benchPath)
	if err != nil {
		return err
	}
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return fmt.Errorf("cannot connect to redis_cache at %s: %w", addr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	if password != "" {
		if err := respCommand(conn, "AUTH", password); err != nil {
			return fmt.Errorf("redis AUTH failed: %w", err)
		}
	}
	if err := respCommand(conn, "DEL", "assets_json"); err != nil {
		return fmt.Errorf("redis DEL assets_json failed: %w", err)
	}
	return nil
}

// redisCacheAddr reads redis_cache from sites/common_site_config.json.
func redisCacheAddr(benchPath string) (addr, password string, err error) {
	cfgPath := filepath.Join(benchPath, "sites", "common_site_config.json")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return "", "", fmt.Errorf("cannot read %s: %w", cfgPath, err)
	}
	var cfg struct {
		RedisCache string `json:"redis_cache"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", "", fmt.Errorf("cannot parse %s: %w", cfgPath, err)
	}
	if cfg.RedisCache == "" {
		return "", "", fmt.Errorf("no redis_cache configured in %s", cfgPath)
	}
	u, err := url.Parse(cfg.RedisCache)
	if err != nil {
		return "", "", fmt.Errorf("invalid redis_cache URL %q: %w", cfg.RedisCache, err)
	}
	host := u.Hostname()
	if host == "" {
		host = "localhost"
	}
	port := u.Port()
	if port == "" {
		port = "6379"
	}
	if u.User != nil {
		password, _ = u.User.Password()
	}
	return net.JoinHostPort(host, port), password, nil
}

// respCommand sends one RESP command and checks for an error reply.
func respCommand(conn net.Conn, args ...string) error {
	var b bytes.Buffer
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	if _, err := conn.Write(b.Bytes()); err != nil {
		return err
	}
	reply := make([]byte, 512)
	n, err := conn.Read(reply)
	if err != nil {
		return err
	}
	if n > 0 && reply[0] == '-' {
		return fmt.Errorf("%s", strings.TrimSpace(string(reply[1:n])))
	}
	return nil
}
