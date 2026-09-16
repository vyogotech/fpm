package mirror

import (
	"fmt"
	"os"
	"os/exec"
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

	_, _, _, err = runner.packageApp(BuildItem{Slug: "wiki", Version: "3.0.0"}, t.TempDir(), "/cache/bench")
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

// TestPackageAppRetriesWithoutAssets: an old tag whose stylesheets no longer compile
// against the frappe this run builds with must not fail the whole app. It is published
// the way every package was before assets were compiled at all — and marked, because
// such a package installs and then renders nothing.
func TestPackageAppRetriesWithoutAssets(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "args.log")
	out := filepath.Join(dir, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	// Fails with the asset-build contract (exit 4) unless told to allow unbuilt assets.
	fpmBin := filepath.Join(dir, "fpm")
	script := `#!/bin/sh
echo "$@" >> ` + logPath + `
case "$*" in
  *--allow-unbuilt-assets*)
    d=$(echo "$@" | tr ' ' '\n' | grep -A0 '^/.*fpm-mirror' | head -1)
    for a in "$@"; do case "$a" in /*fpm-mirror*) d="$a";; esac; done
    : > "$d/wiki-1.0.0.fpm"
    exit 0
    ;;
esac
echo "Error: asset build failed: frappe build for 'wiki' exited with error" >&2
exit 4
`
	if err := os.WriteFile(fpmBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ws, err := NewWorkspace(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	var logged []string
	runner := &Runner{FPMBin: fpmBin, Workspace: ws, OutputPath: out,
		Log: func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }}

	artifact, noDeps, noAssets, err := runner.packageApp(
		BuildItem{Slug: "wiki", Version: "1.0.0", BundleDeps: false}, t.TempDir(), "/cache/bench")
	if err != nil {
		t.Fatalf("an app whose assets cannot be compiled should still package: %v", err)
	}
	if !noAssets {
		t.Fatal("the package must be marked as shipping no compiled assets")
	}
	if noDeps {
		t.Fatal("wheels were not the problem")
	}
	if artifact == "" {
		t.Fatal("no artifact returned")
	}
	if !strings.Contains(strings.Join(logged, "\n"), "retrying without compiled assets") {
		t.Fatalf("the retry must be reported: %v", logged)
	}

	// The retry must drop the bench as well: --allow-unbuilt-assets permits a package
	// with no compiled bundles, but --bench-path is what runs the build that failed, so
	// keeping it repeats the failure. This is what made the first fix a no-op in CI.
	invocations, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	lines := strings.Split(strings.TrimSpace(string(invocations)), "\n")
	retry := lines[len(lines)-1]
	if strings.Contains(retry, "--bench-path") {
		t.Fatalf("the retry still asks for an asset build: %s", retry)
	}
	if !strings.Contains(retry, "--allow-unbuilt-assets") {
		t.Fatalf("the retry must permit a package without compiled assets: %s", retry)
	}
	// And it must switch the asset build off outright. Dropping --bench-path is not
	// enough: `fpm package` also compiles in a bench the checkout already lives in, and
	// every mirror checkout does, so the retry would run the build that just failed.
	if !strings.Contains(retry, "--build-assets=false") {
		t.Fatalf("the retry must turn the asset build off, not just drop the bench: %s", retry)
	}
}

func TestWithoutFlag(t *testing.T) {
	got := withoutFlag([]string{"package", "--bench-path", "/b", "--version", "1.0.0"}, "--bench-path")
	if strings.Join(got, " ") != "package --version 1.0.0" {
		t.Fatalf("flag and its value must both go: %v", got)
	}
	got = withoutFlag([]string{"package", "--bench-path=/b", "--version", "1.0.0"}, "--bench-path")
	if strings.Join(got, " ") != "package --version 1.0.0" {
		t.Fatalf("the --flag=value spelling must go too: %v", got)
	}
	got = withoutFlag([]string{"package", "--version", "1.0.0"}, "--bench-path")
	if strings.Join(got, " ") != "package --version 1.0.0" {
		t.Fatalf("an absent flag must change nothing: %v", got)
	}
}

// TestPackageAppKeepsOtherFailuresFatal: only an asset-build failure degrades. Anything
// else is a real failure and must not be published in a lesser form.
func TestPackageAppKeepsOtherFailuresFatal(t *testing.T) {
	dir := t.TempDir()
	fpmBin := filepath.Join(dir, "fpm")
	if err := os.WriteFile(fpmBin, []byte("#!/bin/sh\necho 'Error: something else broke' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ws, err := NewWorkspace(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{FPMBin: fpmBin, Workspace: ws, OutputPath: t.TempDir(), Log: func(string, ...any) {}}

	_, _, noAssets, err := runner.packageApp(
		BuildItem{Slug: "wiki", Version: "1.0.0"}, t.TempDir(), "/cache/bench")
	if err == nil {
		t.Fatal("a non-asset failure must stay fatal")
	}
	if noAssets {
		t.Fatal("nothing was published, so nothing can be marked")
	}
}

func TestIsAssetBuildFailure(t *testing.T) {
	if !isAssetBuildFailure("Error: asset build failed: frappe build for 'wiki' exited") {
		t.Fatal("the message form must be recognised")
	}
	if !isAssetBuildFailure("fpm: exit status 4") {
		t.Fatal("the exit code contract must be recognised")
	}
	if isAssetBuildFailure("Error: required app could not be resolved") {
		t.Fatal("an unrelated failure must not degrade the package")
	}
}

// TestNoAssetsRetryClearsPartialOutput: esbuild writes bundles one at a time, so a
// build that fails part-way leaves some behind. Packaging those would discover them as
// if they were a deliberate prebuild and publish a partial asset set — which is worse
// than none, because that is what the bench's assets.json would then advertise, and it
// would contradict the published-noassets the report carries.
func TestNoAssetsRetryClearsPartialOutput(t *testing.T) {
	dir := t.TempDir()
	checkout := t.TempDir()
	dist := filepath.Join(checkout, "wiki", "public", "dist", "js")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	half := filepath.Join(dist, "wiki.bundle.HALFBILT.js")
	if err := os.WriteFile(half, []byte("// written before the build failed"), 0o644); err != nil {
		t.Fatal(err)
	}

	fpmBin := filepath.Join(dir, "fpm")
	script := `#!/bin/sh
case "$*" in
  *--allow-unbuilt-assets*)
    for a in "$@"; do case "$a" in /*fpm-mirror*) d="$a";; esac; done
    : > "$d/wiki-1.0.0.fpm"
    exit 0
    ;;
esac
echo "Error: asset build failed" >&2
exit 4
`
	if err := os.WriteFile(fpmBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ws, err := NewWorkspace(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{FPMBin: fpmBin, Workspace: ws, OutputPath: t.TempDir(), Log: func(string, ...any) {}}

	_, _, noAssets, err := runner.packageApp(BuildItem{Slug: "wiki", Version: "1.0.0"}, checkout, "/cache/bench")
	if err != nil {
		t.Fatal(err)
	}
	if !noAssets {
		t.Fatal("the package must be marked as shipping no compiled assets")
	}
	if _, statErr := os.Stat(half); !os.IsNotExist(statErr) {
		t.Fatal("the failed build's partial output must not be packaged")
	}
}

func TestAppNameOf(t *testing.T) {
	if got := appNameOf(BuildItem{Slug: "marley", AppName: "healthcare"}); got != "healthcare" {
		t.Fatalf("the catalog override wins, got %q", got)
	}
	if got := appNameOf(BuildItem{Slug: "wiki"}); got != "wiki" {
		t.Fatalf("the slug is the fallback, got %q", got)
	}
}

// TestInstallCheckDisabledByDefault: the zero value must not try to start a container,
// so a mirror run without an image configured behaves exactly as before.
func TestInstallCheckDisabledByDefault(t *testing.T) {
	var c InstallCheck
	if c.Enabled() {
		t.Fatal("an unconfigured install check must be disabled")
	}
	if err := c.Verify("/tmp/x.fpm", "wiki", "3.1.0"); err != nil {
		t.Fatalf("a disabled check must be a no-op, got %v", err)
	}
}

// TestInstallCheckBlocksPublishing is the point of the check: a package that does not
// install must not reach the registry. The stub fpm rejects the install, so the run
// must fail at the check and never call publish.
func TestInstallCheckBlocksPublishing(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "args.log")
	fpmBin := filepath.Join(dir, "fpm")
	// Packages fine, refuses to install — the shape of a broken artifact.
	script := `#!/bin/sh
echo "$@" >> ` + logPath + `
case "$1" in
  package) for a in "$@"; do case "$a" in /*fpm-mirror*) d="$a";; esac; done; : > "$d/wiki-3.1.0.fpm"; exit 0;;
  install) echo "Error: the package does not install" >&2; exit 1;;
  publish) echo "PUBLISHED"; exit 0;;
esac
exit 0
`
	if err := os.WriteFile(fpmBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ws, err := NewWorkspace(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	// A container that starts, a bench that answers, and an install that refuses — the
	// shape this test is about. A container that cannot start is deliberately not it:
	// that is the host's problem and is skipped, so using it here would have passed
	// without ever exercising the refusal.
	stub := t.TempDir()
	podman := "#!/bin/sh\ncase \"$*\" in\n" +
		"  *'fpm install'*) echo 'Error: the package does not install' >&2; exit 1 ;;\n" +
		"  run*) echo container-id; exit 0 ;;\n" +
		"  exec*) echo wiki; exit 0 ;;\nesac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(stub, "podman"), []byte(podman), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stub+string(os.PathListSeparator)+os.Getenv("PATH"))

	runner := &Runner{
		FPMBin: fpmBin, Workspace: ws, OutputPath: t.TempDir(), Log: func(string, ...any) {},
		InstallCheck: InstallCheck{Image: "example/bench:latest", Site: "dev.localhost", FPMBin: fpmBin},
	}
	err = runner.InstallCheck.Verify(filepath.Join(dir, "wiki-3.1.0.fpm"), "wiki", "3.1.0")
	if err == nil {
		t.Fatal("a package that cannot be installed must fail the check")
	}
	logged, _ := os.ReadFile(logPath)
	if strings.Contains(string(logged), "publish") {
		t.Fatalf("publishing must not happen once the check has failed: %s", logged)
	}
}

// newStubRepo makes a local git repo with one tagged commit. Workspace.Checkout clones
// by URL, and a path is a URL git accepts, so this drives runOne end to end without
// reaching the network.
func newStubRepo(t *testing.T, appName string) string {
	t.Helper()
	repo := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(repo, appName), 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(appName, "hooks.py"), "app_name = \""+appName+"\"\n")
	write("README.md", "stub\n")
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
		{"add", "-A"},
		{"commit", "-qm", "initial"},
		{"tag", "v1.0.0"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return repo
}

// stubFPMWheelFailure writes an `fpm` whose `package` fails unless wheel bundling is
// switched off — the shape of a dependency that publishes no wheel for the target — and
// whose `publish` records that it was called.
func stubFPMWheelFailure(t *testing.T, dir, appName, version, logPath string) string {
	t.Helper()
	fpmBin := filepath.Join(dir, "fpm")
	script := `#!/bin/sh
echo "$@" >> ` + logPath + `
case "$1" in
  package)
    for a in "$@"; do case "$a" in /*fpm-mirror*) d="$a";; esac; done
    case "$*" in
      *--bundle-deps=false*)
        python3 - "$d/` + appName + `-` + version + `.fpm" <<'PY'
import json, sys, zipfile
with zipfile.ZipFile(sys.argv[1], "w") as z:
    z.writestr("app_metadata.json", json.dumps(
        {"package_name": "` + appName + `", "package_version": "` + version + `", "app_name": "` + appName + `"}))
PY
        exit 0
        ;;
    esac
    echo "Error: could not vendor wheels: no matching distribution for the target platform" >&2
    exit 1
    ;;
  publish) echo "PUBLISHED"; exit 0;;
esac
exit 0
`
	if err := os.WriteFile(fpmBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return fpmBin
}

// TestWheelVendoringFailureIsWithheld is the frappe/drive regression. When wheels could
// not be vendored, packageApp retried with --bundle-deps=false and the run published
// the result anyway as published-nodeps. That retry succeeds for exactly the reason
// that makes the artifact dangerous — pip can still resolve at install time — so a
// pooled bench, whose serving pods never pip-install, took a 500 across the whole desk.
// Assets already had this gate; wheels did not, and that is how a 25 MB dependency-less
// drive package reached the registry beside the 97 MB good one.
func TestWheelVendoringFailureIsWithheld(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "args.log")
	fpmBin := stubFPMWheelFailure(t, dir, "drive", "1.0.0", logPath)
	ws, err := NewWorkspace(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{
		FPMBin: fpmBin, Workspace: ws, OutputPath: t.TempDir(),
		RepoNames: []string{"ghcr"}, Log: func(string, ...any) {},
	}
	item := BuildItem{
		Slug: "drive", AppName: "drive", Repo: newStubRepo(t, "drive"),
		Ref: "v1.0.0", Version: "1.0.0", BundleDeps: true,
	}

	results := runner.Run(&Plan{Items: []BuildItem{item}})
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	if results[0].Action != ActionWithheldNoDeps {
		t.Fatalf("a package whose wheels did not vendor must be withheld, got %q (%s)",
			results[0].Action, results[0].Detail)
	}
	logged, _ := os.ReadFile(logPath)
	if strings.Contains(string(logged), "publish") {
		t.Fatalf("a withheld package must never be published:\n%s", logged)
	}
	if !AnyFailed(results) {
		t.Fatal("withholding must fail the run: a green run whose registry is missing the app is how this went unnoticed")
	}
}

// TestWheelVendoringFailurePublishesWhenAllowed: the gate is a default, not a wall. A
// caller who knows the destination bench pip-installs can still ship the package, and
// it is reported distinctly so the run says what went out.
func TestWheelVendoringFailurePublishesWhenAllowed(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "args.log")
	fpmBin := stubFPMWheelFailure(t, dir, "drive", "1.0.0", logPath)
	ws, err := NewWorkspace(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{
		FPMBin: fpmBin, Workspace: ws, OutputPath: t.TempDir(),
		RepoNames: []string{"ghcr"}, AllowUnvendoredDeps: true, Log: func(string, ...any) {},
	}
	item := BuildItem{
		Slug: "drive", AppName: "drive", Repo: newStubRepo(t, "drive"),
		Ref: "v1.0.0", Version: "1.0.0", BundleDeps: true,
	}

	results := runner.Run(&Plan{Items: []BuildItem{item}})
	if results[0].Action != ActionPublishedNoDeps {
		t.Fatalf("--allow-unvendored-deps must publish, reported as published-nodeps; got %q (%s)",
			results[0].Action, results[0].Detail)
	}
	logged, _ := os.ReadFile(logPath)
	if !strings.Contains(string(logged), "publish") {
		t.Fatalf("it must actually reach the registry:\n%s", logged)
	}
	if AnyFailed(results) {
		t.Fatal("publishing what was asked for is not a failed run")
	}
}
