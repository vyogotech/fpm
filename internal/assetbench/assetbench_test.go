package assetbench

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// fakeFrappe is a git repository shaped like frappe as far as this package cares:
// a checkout carrying esbuild/esbuild.js on a branch named like frappe's release lines.
func fakeFrappe(t *testing.T, branch string, withPipeline bool) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", branch)
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	if withPipeline {
		write(t, filepath.Join(dir, "esbuild", "esbuild.js"), "// frappe's pipeline")
	} else {
		write(t, filepath.Join(dir, "README.md"), "no pipeline here")
	}
	run("add", "-A")
	run("commit", "-qm", "initial")
	return dir
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestEnsureLaysOutABenchFrappesEsbuildCanRunIn is the whole point of the package:
// before it, an app with *.bundle.* entry points could only ship them compiled if the
// caller already had a bench, so wiki, lms, webshop and drive were published with their
// desk assets uncompiled and rendered nothing once installed.
func TestEnsureLaysOutABenchFrappesEsbuildCanRunIn(t *testing.T) {
	cache := t.TempDir()
	bench, err := Ensure(Options{
		CacheDir:   cache,
		FrappeRepo: fakeFrappe(t, "version-16", true),
		FrappeRef:  "version-16",
		Stdout:     io.Discard,
	})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	for _, required := range []string{
		filepath.Join("apps", "frappe", "esbuild", "esbuild.js"),
		filepath.Join("sites", "common_site_config.json"),
	} {
		if _, err := os.Stat(filepath.Join(bench, required)); err != nil {
			t.Fatalf("the bench is missing %s: %v", required, err)
		}
	}
}

// TestEnsureKeepsClearOfTheMirrorWorkspace: `fpm mirror` keeps its own bench at
// <cache>/bench with a checkout of every catalog app under apps/. frappe's esbuild
// requires the app it compiles to sit at <bench>/apps/<app>, so sharing that directory
// would mean `fpm package` staging an app over the mirror's checkout of it.
func TestEnsureKeepsClearOfTheMirrorWorkspace(t *testing.T) {
	cache := t.TempDir()
	mirrorApp := filepath.Join(cache, "bench", "apps", "wiki", "wiki")
	write(t, filepath.Join(mirrorApp, "hooks.py"), "app_name = \"wiki\"\n")

	bench, err := Ensure(Options{
		CacheDir:   cache,
		FrappeRepo: fakeFrappe(t, "version-16", true),
		FrappeRef:  "version-16",
		Stdout:     io.Discard,
	})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if bench == filepath.Join(cache, "bench") {
		t.Fatal("the package bench must not be the mirror workspace's bench")
	}
	if _, err := os.Stat(filepath.Join(mirrorApp, "hooks.py")); err != nil {
		t.Fatalf("the mirror's own checkout was disturbed: %v", err)
	}
}

// TestEnsureClearsAppsLeftByAnInterruptedBuild: internal/benchbuild refuses to stage an
// app over a directory it does not recognise as the same checkout, which is right for a
// bench the caller owns. This one is fpm's own, so a copy left behind by a build that
// was killed is debris — and without this it would break every later build of that app.
func TestEnsureClearsAppsLeftByAnInterruptedBuild(t *testing.T) {
	cache := t.TempDir()
	repo := fakeFrappe(t, "version-16", true)
	bench, err := Ensure(Options{CacheDir: cache, FrappeRepo: repo, FrappeRef: "version-16", Stdout: io.Discard})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	stale := filepath.Join(bench, "apps", "wiki")
	write(t, filepath.Join(stale, "wiki", "hooks.py"), "app_name = \"wiki\"\n")

	if _, err := Ensure(Options{CacheDir: cache, FrappeRepo: repo, FrappeRef: "version-16", Stdout: io.Discard}); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("a previous build's staged app must be cleared, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(bench, "apps", "frappe", "esbuild", "esbuild.js")); err != nil {
		t.Fatalf("clearing debris must not touch frappe: %v", err)
	}
}

// TestEnsureRejectsARefWithoutTheAssetPipeline: a ref that carries no esbuild is not a
// bench that can compile anything, and saying so beats failing later inside node.
func TestEnsureRejectsARefWithoutTheAssetPipeline(t *testing.T) {
	_, err := Ensure(Options{
		CacheDir:   t.TempDir(),
		FrappeRepo: fakeFrappe(t, "version-16", false),
		FrappeRef:  "version-16",
		Stdout:     io.Discard,
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}

// TestEnsureIsIdempotent: a catalogue run packages app after app, and re-resolving a
// checkout that is already on the ref must not refetch it.
func TestEnsureIsIdempotent(t *testing.T) {
	cache := t.TempDir()
	repo := fakeFrappe(t, "version-16", true)
	first, err := Ensure(Options{CacheDir: cache, FrappeRepo: repo, FrappeRef: "version-16", Stdout: io.Discard})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// Removing the origin proves the second call reached the network for nothing: a
	// checkout already at the ref must not fetch.
	cmd := exec.Command("git", "remote", "remove", "origin")
	cmd.Dir = filepath.Join(first, "apps", "frappe")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote remove: %v\n%s", err, out)
	}
	second, err := Ensure(Options{CacheDir: cache, FrappeRepo: repo, FrappeRef: "version-16", Stdout: io.Discard})
	if err != nil {
		t.Fatalf("a checkout already at the ref must need no fetch: %v", err)
	}
	if second != first {
		t.Fatalf("bench moved between calls: %q then %q", first, second)
	}
}

// TestDefaultCacheDirHonoursTheEnvironment lets a build host put the cache on the disk
// it has room on, which for a frappe checkout plus node_modules matters.
func TestDefaultCacheDirHonoursTheEnvironment(t *testing.T) {
	t.Setenv("FPM_BUILD_CACHE", "/somewhere/else")
	dir, err := DefaultCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != "/somewhere/else" {
		t.Fatalf("DefaultCacheDir = %q", dir)
	}
}
