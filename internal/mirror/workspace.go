package mirror

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"fpm/internal/frontend"
)

// Workspace is the persistent build cache: git checkouts reused across runs,
// plus the pip and npm/yarn caches every subprocess is pointed at. Reusing all
// three is what keeps a full-catalog run from re-downloading the world.
//
// The checkouts are laid out as a real bench — <cache>/bench/apps/<slug> beside
// <cache>/bench/sites — because that is what frappe app frontends assume. They reach
// the bench by relative path (crm's socket module imports
// ../../../../sites/common_site_config.json), frappe-ui's vite plugin derives an app's
// name and its www template from the apps/<app> segment, and helpdesk's build script
// runs `cd ../../frappe/ui`. A checkout in a flat src/ directory satisfies none of
// that, and each app fails differently and confusingly.
type Workspace struct {
	CacheDir string
	NoClean  bool // keep checkout state between builds, for debugging
}

// NewWorkspace creates the cache layout under root.
func NewWorkspace(root string, noClean bool) (*Workspace, error) {
	w := &Workspace{CacheDir: root, NoClean: noClean}
	for _, dir := range []string{w.srcRoot(), w.sitesDir(), w.pipCache(), w.npmCache(), w.yarnCache()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("failed to create cache directory %s: %w", dir, err)
		}
	}
	// The one file that makes the layout a bench as far as an app build is concerned.
	// Never overwritten, so a caller pointing --cache-dir at a real bench keeps its own.
	config := filepath.Join(w.sitesDir(), "common_site_config.json")
	if _, err := os.Stat(config); os.IsNotExist(err) {
		data, err := frontend.DefaultSiteConfigJSON()
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(config, data, 0o644); err != nil {
			return nil, fmt.Errorf("failed to write %s: %w", config, err)
		}
	}
	return w, nil
}

// BenchRoot is the bench-shaped directory the checkouts live under.
func (w *Workspace) BenchRoot() string { return filepath.Join(w.CacheDir, "bench") }

func (w *Workspace) srcRoot() string   { return filepath.Join(w.BenchRoot(), "apps") }
func (w *Workspace) sitesDir() string  { return filepath.Join(w.BenchRoot(), "sites") }
func (w *Workspace) pipCache() string  { return filepath.Join(w.CacheDir, "pip") }
func (w *Workspace) npmCache() string  { return filepath.Join(w.CacheDir, "npm") }
func (w *Workspace) yarnCache() string { return filepath.Join(w.CacheDir, "npm", "yarn") }

// BuildEnv is the environment every build subprocess runs with: the caller's
// environment plus the shared caches. PIP_CACHE_DIR reaches the pip that
// `fpm package` shells out to for wheel vendoring; the npm/yarn variables
// reach catalog build scripts.
func (w *Workspace) BuildEnv() []string {
	return append(os.Environ(),
		"PIP_CACHE_DIR="+w.pipCache(),
		"npm_config_cache="+w.npmCache(),
		"YARN_CACHE_FOLDER="+w.yarnCache(),
	)
}

// Checkout puts the app's persistent clone at the requested ref and returns
// the checkout directory.
//
// The clone is created once with --filter=blob:none — full history and tag
// metadata, blobs fetched on demand — and reused by every later run. Unless
// NoClean is set, the tree is scrubbed with clean -fdx after checkout so
// leftovers from a previous build (node_modules, dist, a stale
// compiled_assets) cannot leak into the next package.
func (w *Workspace) Checkout(slug, repoURL, ref string, isBranch bool) (string, error) {
	dir := filepath.Join(w.srcRoot(), slug)

	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if err := gitRun("", "clone", "--filter=blob:none", repoURL, dir); err != nil {
			return "", err
		}
	}

	if isBranch {
		if err := gitRun(dir, "fetch", "origin", ref); err != nil {
			return "", err
		}
		if err := gitRun(dir, "checkout", "--detach", "-f", "FETCH_HEAD"); err != nil {
			return "", err
		}
	} else {
		if err := gitRun(dir, "fetch", "--tags", "--prune", "origin"); err != nil {
			return "", err
		}
		if err := gitRun(dir, "checkout", "--detach", "-f", "refs/tags/"+ref); err != nil {
			return "", err
		}
	}

	if !w.NoClean {
		if err := gitRun(dir, "clean", "-fdx"); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func gitRun(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %v: %w\n%s", args, err, tail(string(out), 800))
	}
	return nil
}

// tail keeps the end of subprocess output, where the actual error usually is.
func tail(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return "…" + s[len(s)-max:]
}

// EnsureBuildDependency puts another app's source beside the one being built, at
// <bench>/apps/<slug>, and returns its directory.
//
// This is a build-time dependency, which is a different thing from the package
// dependencies fpm already resolves. `required_apps` are resolved to pinned versions at
// packaging time (metadata only — no code is fetched, because packaging never runs the
// app) and cascade-installed at install time. Neither helps a *build* that reads another
// app's source off disk: helpdesk's desk build runs
//
//	[ -f ../../frappe/ui/node_modules/.yarn-integrity ] || (cd ../../frappe/ui && yarn install)
//
// which needs frappe checked out as a sibling, exactly as a real bench has it. The app is
// fetched to be read, never built or published — it may not even be in the catalog's
// enabled set, as frappe no longer is.
//
// Unlike Checkout, the tree is not scrubbed: a dependency's node_modules is the point of
// keeping it around, and nothing here is packaged.
func (w *Workspace) EnsureBuildDependency(slug, repoURL, ref string) (string, error) {
	dir := filepath.Join(w.srcRoot(), slug)

	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if err := gitRun("", "clone", "--filter=blob:none", repoURL, dir); err != nil {
			return "", fmt.Errorf("build dependency %s: %w", slug, err)
		}
	}
	if ref == "" {
		return dir, nil
	}
	// A ref that is already checked out costs one cheap rev-parse rather than a fetch.
	if current, err := gitOutput(dir, "rev-parse", "HEAD"); err == nil {
		if resolved, err := gitOutput(dir, "rev-parse", ref+"^{commit}"); err == nil && current == resolved {
			return dir, nil
		}
	}
	if err := gitRun(dir, "fetch", "--tags", "--prune", "origin"); err != nil {
		return "", fmt.Errorf("build dependency %s: %w", slug, err)
	}
	if err := gitRun(dir, "checkout", "--detach", "-f", ref); err != nil {
		// A branch pin such as "version-15" names no local ref in a fresh clone; the
		// fetch above put it at origin/version-15.
		if err2 := gitRun(dir, "checkout", "--detach", "-f", "origin/"+ref); err2 != nil {
			return "", fmt.Errorf("build dependency %s at %s: %w", slug, ref, err)
		}
	}

	// The ref just changed, so whatever node_modules the previous ref installed no
	// longer belongs to this tree. Consumers guard on an install marker rather than a
	// version — helpdesk's build runs `yarn install` in frappe/ui only when
	// node_modules/.yarn-integrity is absent — so a stale tree is silently kept and
	// the build then fails on a package the new ref expects. Clearing them makes that
	// guard do the right thing.
	if err := clearNodeModules(dir); err != nil {
		return "", fmt.Errorf("build dependency %s: %w", slug, err)
	}
	return dir, nil
}

// clearNodeModules removes installed node dependencies from a checkout, at the root and
// one level down, which is where a frappe app keeps them (frappe/ui, apps/<x>/frontend).
func clearNodeModules(dir string) error {
	candidates := []string{filepath.Join(dir, "node_modules")}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if e.IsDir() && e.Name() != "node_modules" && !strings.HasPrefix(e.Name(), ".") {
				candidates = append(candidates, filepath.Join(dir, e.Name(), "node_modules"))
			}
		}
	}
	for _, path := range candidates {
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("cannot clear %s: %w", path, err)
		}
	}
	return nil
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}
