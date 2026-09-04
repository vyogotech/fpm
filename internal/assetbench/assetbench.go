// Package assetbench materialises the smallest thing frappe's esbuild will run in, so
// `fpm package` can compile an app's classic desk bundles without being handed a bench.
//
// Frappe has two asset schemes and an app frequently ships both (see internal/frontend
// for the split). The SPA half builds from the app's own checkout; the classic half —
// `<app>/public/**/*.bundle.{js,css,scss,…}` compiled into `<app>/public/dist/` — is
// built by frappe's own esbuild, which lives in frappe's checkout and resolves an app
// through the `sites/assets/<app>` symlink. Until this package existed the only way to
// get that was --bench-path: the caller had to already have a bench. Apps that ship
// bundle sources (wiki, lms, webshop, drive) therefore packaged with their desk assets
// uncompiled, installed cleanly, and rendered nothing.
//
// What esbuild actually needs is narrow, and none of it is a working bench:
//
//	<bench>/apps/frappe/esbuild/     frappe's bundler
//	<bench>/apps/frappe/node_modules frappe's npm tree
//	<bench>/apps/frappe/frappe/public  reachable from an app's SCSS (see below)
//	<bench>/sites/common_site_config.json
//	<bench>/sites/assets/<app> -> apps/<app>/<app>/public   (internal/benchbuild links it)
//
// No virtualenv, no database, no site. internal/benchbuild already drives esbuild
// directly through node for exactly this case; this package supplies the tree it runs
// in, cached between runs so frappe is fetched once rather than per app.
//
// Only a sparse slice of frappe is checked out, because the rest of the repository is
// never read by a build: a full blobless clone is ~210 MB and takes minutes, the slice
// is ~1 MB and takes seconds. What the slice has to contain is not a guess:
//
//   - esbuild/ is the pipeline itself, and everything it requires is inside it.
//   - package.json and yarn.lock are the npm tree, and they are load-bearing for apps,
//     not just for frappe. An app's desk bundle resolves bare imports through esbuild's
//     nodePaths, which includes apps/frappe/node_modules — frappe/print_designer's
//     bundle imports vue, pinia, @vueuse/core and ace-builds while declaring none of
//     them. Cone-mode sparse checkout always includes root files, so these come along.
//   - frappe/public is reachable from an app's SCSS: esbuild/sass_options.js puts
//     <bench>/apps/<app> on sass includePaths for every app, so `@import
//     "frappe/public/scss/..."` resolves through apps/frappe. 13 MB of insurance.
//
// node_modules is the one part that stays large, and no amount of narrowing removes it:
// it is what app bundles import from.
package assetbench

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"fpm/internal/frontend"
)

// DefaultFrappeRef is the frappe branch an app's desk bundles are compiled with when
// the caller names none. It matches internal/mirror's default so a package built here
// and one built by a catalog run compile against the same bundler.
const DefaultFrappeRef = "version-16"

// DefaultFrappeRepo is where frappe is fetched from.
const DefaultFrappeRepo = "https://github.com/frappe/frappe"

// ErrUnavailable wraps every failure to produce a usable bench, so a caller can tell
// "there is nowhere to build" from "the build itself failed".
var ErrUnavailable = errors.New("asset bench unavailable")

// Options configures the bench.
type Options struct {
	// CacheDir is where the bench is kept between runs. Empty means DefaultCacheDir.
	CacheDir string
	// FrappeRef is the ref to compile against. Empty means DefaultFrappeRef.
	FrappeRef string
	// FrappeRepo is the repository to fetch frappe from. Empty means DefaultFrappeRepo.
	FrappeRepo string
	// SiteConfig is the contents of sites/common_site_config.json. Empty means
	// frappe's own defaults, which is what internal/frontend synthesizes too.
	SiteConfig []byte
	// Stdout receives progress; nil means os.Stdout.
	Stdout io.Writer
}

// benchDir is the bench's name inside the cache. It is deliberately not "bench", which
// is what `fpm mirror`'s workspace calls its own at the same cache root: that one holds
// a checkout of every catalog app under apps/, and frappe's esbuild requires the app it
// compiles to sit at <bench>/apps/<app>. Sharing the directory would mean `fpm package`
// staging an app on top of the mirror's checkout of it. One extra frappe checkout is
// cheaper than that class of bug.
const benchDir = "package-bench"

// DefaultCacheDir is where the bench lives when the caller names no CacheDir:
// $FPM_BUILD_CACHE, else <home>/.fpm/build-cache. It sits beside the app store rather
// than in a temporary directory because the frappe checkout and its node_modules are
// the expensive part, and re-fetching them for every package would dominate the build.
func DefaultCacheDir() (string, error) {
	if env := strings.TrimSpace(os.Getenv("FPM_BUILD_CACHE")); env != "" {
		return env, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("%w: cannot locate the home directory for the build cache: %v", ErrUnavailable, err)
	}
	return filepath.Join(home, ".fpm", "build-cache"), nil
}

// Ensure returns a bench root that internal/benchbuild can compile in, fetching or
// updating frappe as needed. It is safe to call repeatedly: a checkout already at the
// requested ref costs one rev-parse.
func Ensure(opts Options) (string, error) {
	out := opts.Stdout
	if out == nil {
		out = os.Stdout
	}
	if _, err := exec.LookPath("git"); err != nil {
		return "", fmt.Errorf("%w: git is not on PATH, so frappe's asset pipeline cannot be fetched; "+
			"pass --bench-path <bench> to build against a bench you already have", ErrUnavailable)
	}

	cache := opts.CacheDir
	if cache == "" {
		var err error
		if cache, err = DefaultCacheDir(); err != nil {
			return "", err
		}
	}
	bench, err := filepath.Abs(filepath.Join(cache, benchDir))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}

	if err := ensureSiteConfig(bench, opts.SiteConfig); err != nil {
		return "", err
	}
	if err := clearStagedApps(bench); err != nil {
		return "", err
	}

	ref := opts.FrappeRef
	if ref == "" {
		ref = DefaultFrappeRef
	}
	repo := opts.FrappeRepo
	if repo == "" {
		repo = DefaultFrappeRepo
	}
	if err := ensureFrappe(bench, repo, ref, out); err != nil {
		return "", err
	}
	return bench, nil
}

// clearStagedApps removes app checkouts left in the bench by an earlier run.
//
// internal/benchbuild stages the app being packaged at <bench>/apps/<app> and removes it
// afterwards, but a build that was interrupted — Ctrl-C, an OOM kill, a runner timeout —
// leaves it behind, and the next run refuses to stage over a directory it does not
// recognise as the same checkout. In a bench the caller owns that refusal is right; this
// one is fpm's own, created here and holding nothing but frappe, so anything else under
// apps/ is debris.
func clearStagedApps(bench string) error {
	apps := filepath.Join(bench, "apps")
	entries, err := os.ReadDir(apps)
	if err != nil {
		return nil // no apps/ yet, or unreadable — ensureFrappe reports the real problem
	}
	for _, entry := range entries {
		if entry.Name() == "frappe" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(apps, entry.Name())); err != nil {
			return fmt.Errorf("%w: could not clear a previous build's %s from %s: %v",
				ErrUnavailable, entry.Name(), apps, err)
		}
	}
	return nil
}

// ensureSiteConfig writes the one file an app's build may import from the bench. It is
// never overwritten: a cache the caller has pointed at a real bench's config keeps it.
func ensureSiteConfig(bench string, config []byte) error {
	sites := filepath.Join(bench, "sites")
	if err := os.MkdirAll(sites, 0o755); err != nil {
		return fmt.Errorf("%w: cannot create %s: %v", ErrUnavailable, sites, err)
	}
	path := filepath.Join(sites, "common_site_config.json")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if len(config) == 0 {
		var err error
		if config, err = frontend.DefaultSiteConfigJSON(); err != nil {
			return fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
	}
	if err := os.WriteFile(path, config, 0o644); err != nil {
		return fmt.Errorf("%w: cannot write %s: %v", ErrUnavailable, path, err)
	}
	return nil
}

// sparsePaths are the directories checked out of frappe. Cone-mode sparse checkout
// always adds the repository's root files, which is where package.json and yarn.lock
// are, so they need no entry here.
var sparsePaths = []string{"esbuild", "frappe/public"}

// refMarkerName records which ref the slice was checked out at.
//
// A shallow clone cannot answer that from git alone: `git rev-parse origin/version-16`
// does not resolve after `fetch --depth 1 origin <ref>`, which writes only FETCH_HEAD,
// so comparing HEAD against the ref name is unreliable exactly where it matters. The
// marker is written after a successful checkout and read on the next call, which is
// what makes packaging a second app cost nothing.
const refMarkerName = ".fpm-frappe-ref"

// ensureFrappe checks out the slice of frappe the asset build reads, at ref.
func ensureFrappe(bench, repo, ref string, out io.Writer) error {
	dir := filepath.Join(bench, "apps", "frappe")
	marker := filepath.Join(bench, refMarkerName)

	fresh := false
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		fmt.Fprintf(out, "Fetching frappe's asset pipeline (%s at %s) into %s; this is cached for later builds\n",
			repo, ref, dir)
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		// --branch takes a branch or tag, which is what a frappe ref almost always is.
		// A commit SHA is not accepted there, so fall back to cloning the default head
		// and fetching the ref below.
		if err := git("", "clone", "--depth", "1", "--filter=blob:none", "--sparse", "--branch", ref, repo, dir); err != nil {
			os.RemoveAll(dir)
			if err2 := git("", "clone", "--depth", "1", "--filter=blob:none", "--sparse", repo, dir); err2 != nil {
				return fmt.Errorf("%w: could not fetch frappe from %s: %v", ErrUnavailable, repo, err2)
			}
		}
		if err := git(dir, append([]string{"sparse-checkout", "set"}, sparsePaths...)...); err != nil {
			return fmt.Errorf("%w: could not narrow the frappe checkout in %s: %v", ErrUnavailable, dir, err)
		}
		fresh = true
	}

	if fresh || readMarker(marker) != ref {
		if !fresh {
			fmt.Fprintf(out, "Checking out frappe %s in %s\n", ref, dir)
			if err := git(dir, "fetch", "--depth", "1", "origin", ref); err != nil {
				return fmt.Errorf("%w: frappe has no ref %q in %s: %v", ErrUnavailable, ref, repo, err)
			}
			if err := git(dir, "checkout", "--detach", "-f", "FETCH_HEAD"); err != nil {
				return fmt.Errorf("%w: could not check out frappe %s in %s: %v", ErrUnavailable, ref, dir, err)
			}
			// node_modules belongs to the ref that installed it. esbuild's own imports
			// move between frappe versions, so a tree left over from the previous ref
			// builds against the wrong bundler; internal/benchbuild reinstalls when it
			// is absent.
			if err := os.RemoveAll(filepath.Join(dir, "node_modules")); err != nil {
				return fmt.Errorf("%w: could not clear frappe's node_modules in %s: %v", ErrUnavailable, dir, err)
			}
		}
		if err := os.WriteFile(marker, []byte(ref+"\n"), 0o644); err != nil {
			return fmt.Errorf("%w: could not record the frappe ref in %s: %v", ErrUnavailable, marker, err)
		}
	}

	if _, err := os.Stat(filepath.Join(dir, "esbuild", "esbuild.js")); err != nil {
		return fmt.Errorf("%w: frappe %s has no asset pipeline at %s, so there is nothing to compile bundles with",
			ErrUnavailable, ref, filepath.Join(dir, "esbuild"))
	}
	return nil
}

// FrappeCommit is the exact commit of the frappe inside a bench, or "" when it cannot
// be determined. It works for any bench, not only one this package built, so a package
// made with --bench-path records the same evidence as one made here.
func FrappeCommit(benchPath string) string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = filepath.Join(benchPath, "apps", "frappe")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func readMarker(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func git(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
