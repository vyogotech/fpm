package mirror

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Workspace is the persistent build cache: git checkouts reused across runs,
// plus the pip and npm/yarn caches every subprocess is pointed at. Reusing all
// three is what keeps a full-catalog run from re-downloading the world.
type Workspace struct {
	CacheDir string
	NoClean  bool // keep checkout state between builds, for debugging
}

// NewWorkspace creates the cache layout under root.
func NewWorkspace(root string, noClean bool) (*Workspace, error) {
	w := &Workspace{CacheDir: root, NoClean: noClean}
	for _, dir := range []string{w.srcRoot(), w.pipCache(), w.npmCache(), w.yarnCache()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("failed to create cache directory %s: %w", dir, err)
		}
	}
	return w, nil
}

func (w *Workspace) srcRoot() string   { return filepath.Join(w.CacheDir, "src") }
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
