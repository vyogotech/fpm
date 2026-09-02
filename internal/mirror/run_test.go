package mirror

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAutoBuildFrontendAssets(t *testing.T) {
	checkout := t.TempDir()
	appModule := filepath.Join(checkout, "testapp")
	requireNoError := func(err error) {
		if err != nil {
			t.Helper()
			t.Fatal(err)
		}
	}

	requireNoError(os.MkdirAll(appModule, 0o755))
	frontendDir := filepath.Join(checkout, "frontend")
	requireNoError(os.MkdirAll(frontendDir, 0o755))

	// Create package.json with a build script that writes mock bundles
	mockBuildScript := "mkdir -p dist/js dist/css && echo 'console.log(1)' > dist/js/testapp.bundle.12345678.js && echo 'body{}' > dist/css/testapp.bundle.87654321.css"
	pkgJSON := `{"name": "testapp-frontend", "scripts": {"build": "` + mockBuildScript + `"}}`
	requireNoError(os.WriteFile(filepath.Join(frontendDir, "package.json"), []byte(pkgJSON), 0o644))

	ws, err := NewWorkspace(t.TempDir(), false)
	requireNoError(err)

	runner := &Runner{
		Workspace: ws,
		Log:       func(format string, args ...any) {},
	}

	buildRoot, cleanup, err := runner.autoBuildFrontendAssets("testapp", checkout)
	t.Cleanup(cleanup)
	requireNoError(err)
	if buildRoot != checkout {
		t.Fatalf("buildRoot = %q, want the checkout %q", buildRoot, checkout)
	}

	// A build that wrote beside its own project is adopted into the app module, where
	// frappe can serve it. The destination is public/frontend, not public/dist: dist is
	// where frappe's esbuild puts the hashed *.bundle.* files that go into assets.json,
	// and this output is not that.
	destJS := filepath.Join(appModule, "public", "frontend", "js", "testapp.bundle.12345678.js")
	if _, err := os.Stat(destJS); os.IsNotExist(err) {
		t.Fatalf("expected built asset at %s, but file was not found", destJS)
	}

	destCSS := filepath.Join(appModule, "public", "frontend", "css", "testapp.bundle.87654321.css")
	if _, err := os.Stat(destCSS); os.IsNotExist(err) {
		t.Fatalf("expected built asset at %s, but file was not found", destCSS)
	}
}

// TestAutoBuildFrontendAssetsRequiresARealAppName guards the failure that took down a
// whole mirror run: BuildItem.AppName is the catalog's *override* and is empty for
// every app whose module is named after its slug — which is most of the catalog. Passing
// it straight through made the frontend build reject every app with "app name is
// required" before anything was built.
func TestAutoBuildFrontendAssetsRequiresARealAppName(t *testing.T) {
	checkout := t.TempDir()
	if err := os.MkdirAll(filepath.Join(checkout, "wiki"), 0o755); err != nil {
		t.Fatal(err)
	}
	ws, err := NewWorkspace(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{Workspace: ws, Log: func(string, ...any) {}}

	// An app with no frontend is a no-op, but only if it was given a name at all.
	if _, cleanup, err := runner.autoBuildFrontendAssets("wiki", checkout); err != nil {
		t.Fatalf("a named app with no frontend must not error: %v", err)
	} else {
		cleanup()
	}

	_, cleanup, err := runner.autoBuildFrontendAssets("", checkout)
	cleanup()
	if err == nil {
		t.Fatal("an empty app name must be rejected loudly rather than silently building nothing")
	}
	if !strings.Contains(err.Error(), "app name is required") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestEnsureAssetBenchSkipsAppsWithNothingToCompile: an app with no esbuild entry
// points (an SPA-only app, or one with no assets) must not drag frappe's whole
// checkout into the build.
func TestEnsureAssetBenchSkipsAppsWithNothingToCompile(t *testing.T) {
	checkout := t.TempDir()
	if err := os.MkdirAll(filepath.Join(checkout, "crm", "public", "frontend"), 0o755); err != nil {
		t.Fatal(err)
	}
	ws, err := NewWorkspace(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{Workspace: ws, Log: func(string, ...any) {}}

	bench, err := runner.ensureAssetBench("crm", "crm", checkout)
	if err != nil {
		t.Fatal(err)
	}
	if bench != "" {
		t.Fatalf("no bundles to compile, so no asset bench is needed; got %q", bench)
	}
}

// TestEnsureAssetBenchNeedsFrappeInTheCatalog: an app that does have bundles to
// compile and no frappe to compile them with fails with that reason, rather than
// producing a package whose desk UI never renders (issue #9).
func TestEnsureAssetBenchNeedsFrappeInTheCatalog(t *testing.T) {
	checkout := t.TempDir()
	dir := filepath.Join(checkout, "wiki", "public", "js")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wiki.bundle.js"), []byte("// entry"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := NewWorkspace(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{Workspace: ws, Log: func(string, ...any) {}, CatalogRepos: map[string]string{}}

	_, err = runner.ensureAssetBench("wiki", "wiki", checkout)
	if err == nil {
		t.Fatal("expected a failure when there is no frappe to build with")
	}
	if !strings.Contains(err.Error(), "frappe is not in the catalog") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestFrappeRefPrecedence: the catalog's per-app pin wins over the run's flag, which
// wins over the default. An app whose module name differs from its slug is found
// under either.
func TestFrappeRefPrecedence(t *testing.T) {
	runner := &Runner{
		FrappeRef: "version-15",
		BuildDepRefs: map[string]map[string]string{
			"helpdesk":    {"frappe": "develop"},
			"pos_awesome": {"frappe": "version-14"},
		},
	}
	if got := runner.frappeRef("helpdesk", "helpdesk"); got != "develop" {
		t.Fatalf("catalog pin should win, got %q", got)
	}
	if got := runner.frappeRef("posawesome", "pos_awesome"); got != "version-14" {
		t.Fatalf("a pin keyed by slug must apply to the app it names, got %q", got)
	}
	if got := runner.frappeRef("wiki", "wiki"); got != "version-15" {
		t.Fatalf("the run's --frappe-ref should apply, got %q", got)
	}
	if got := (&Runner{}).frappeRef("wiki", "wiki"); got != DefaultFrappeRef {
		t.Fatalf("default = %q, want %q", got, DefaultFrappeRef)
	}
}

// TestPackageArgsCarryTheAssetBench: the bench only reaches `fpm package` as
// --bench-path, which is what makes it run frappe's asset build.
func TestPackageArgsCarryTheAssetBench(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "args.log")
	fpmBin := filepath.Join(t.TempDir(), "fpm")
	script := "#!/bin/sh\necho \"$@\" > " + logPath + "\nexit 1\n" // exit 1: no artifact to produce
	if err := os.WriteFile(fpmBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ws, err := NewWorkspace(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{FPMBin: fpmBin, Workspace: ws, OutputPath: t.TempDir(), Log: func(string, ...any) {},
		RepoNames: []string{"ghcr"}}

	_, _, err = runner.packageApp(BuildItem{Slug: "wiki", Version: "3.0.0"}, t.TempDir(), "/cache/bench")
	if err == nil {
		t.Fatal("the stub fpm fails; packageApp must report it")
	}
	logged, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(logged), "--bench-path /cache/bench") {
		t.Fatalf("fpm package must be given the asset bench: %s", logged)
	}
	if strings.Contains(string(logged), "--requires-from-local-store") {
		t.Fatalf("with a registry configured, pins come from it, not the build host's store: %s", logged)
	}
	if !strings.Contains(string(logged), "--repo ghcr") {
		t.Fatalf("pins must be resolved against the registry this run publishes to: %s", logged)
	}
}
