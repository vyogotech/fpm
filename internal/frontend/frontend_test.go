package frontend

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// crmLayout writes the directory shape of frappe/crm at the commit this package was
// written against: a delegating root package.json, the Vite project in frontend/, and
// a python module whose public/frontend and www/<app>.html are build outputs listed in
// the app's .gitignore (so they are absent from a fresh checkout).
func crmLayout(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "package.json"), `{
	  "private": true,
	  "name": "crm",
	  "scripts": {
	    "postinstall": "cd frontend && yarn install --check-files",
	    "dev": "cd frontend && yarn dev",
	    "build": "cd frontend && yarn build"
	  }
	}`)
	write(t, filepath.Join(root, "yarn.lock"), "# yarn lockfile v1\n")
	write(t, filepath.Join(root, "frontend", "package.json"), `{
	  "name": "crm-ui",
	  "scripts": {
	    "build": "vite build --base=/assets/crm/frontend/ && yarn copy-html-entry",
	    "copy-html-entry": "cp ../crm/public/frontend/index.html ../crm/www/crm.html"
	  }
	}`)
	write(t, filepath.Join(root, "frontend", "yarn.lock"), "# yarn lockfile v1\n")
	write(t, filepath.Join(root, "crm", "hooks.py"), `app_name = "crm"
website_route_rules = [
    {"from_route": "/crm/<path:app_path>", "to_route": "crm"},
]
`)
	write(t, filepath.Join(root, "crm", "public", ".gitkeep"), "")
	write(t, filepath.Join(root, "crm", "www", "crm.py"), "import frappe\n")
	return root
}

// spaHTML is what a built Vite index.html looks like: it links its bundles by the
// /assets/<app>/<dir>/ URL the sites/assets symlink serves. That reference is how a
// route template is told apart from an unrelated www page.
func spaHTML(app, dir string) string {
	return "<!doctype html><script type=module src=/assets/" + app + "/" + dir +
		"/assets/index-A1B2C3.js></script><div id=app></div>"
}

// erpnextLayout is erpnext's shape: a banking SPA under public/banking, alongside two
// dozen DocType portal routes and hand-maintained static assets that must not be
// mistaken for build output.
func erpnextLayout(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "erpnext", "hooks.py"), `app_name = "erpnext"
website_route_rules = [
	{"from_route": "/orders", "to_route": "Sales Order"},
	{"from_route": "/orders/<path:name>", "to_route": "order"},
	{"from_route": "/addresses", "to_route": "Address"},
	{"from_route": "/banking/<path:app_path>", "to_route": "banking"},
]
`)
	write(t, filepath.Join(root, "erpnext", "public", "js", "erpnext.js"), "// hand written, not build output")
	write(t, filepath.Join(root, "erpnext", "www", "order.html"), "<h1>a real portal page, not the SPA</h1>")
	write(t, filepath.Join(root, "erpnext", "www", "support.html"), "<h1>support</h1>")
	return root
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestDetectPrefersDelegatingRootOverSubdirectory(t *testing.T) {
	// crm's root build script is `cd frontend && yarn build`. Building the root and
	// frontend/ separately would run the same Vite build twice, so the root — the
	// app's own declared entry point — must win and detection must stop there.
	root := crmLayout(t)

	project, err := Detect(root, "crm")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if project == nil {
		t.Fatal("expected crm's frontend to be detected")
	}
	if project.Rel != "." {
		t.Errorf("expected the checkout root to win, got %q", project.Rel)
	}
	if project.BuildScript != "cd frontend && yarn build" {
		t.Errorf("BuildScript = %q", project.BuildScript)
	}
	if project.PkgManager != "yarn" {
		t.Errorf("PkgManager = %q, want yarn (root yarn.lock)", project.PkgManager)
	}
	if !project.HasLockfile {
		t.Error("expected the root yarn.lock to be seen")
	}
}

func TestDetectFallsThroughToSubdirectoryWhenRootHasNoBuildScript(t *testing.T) {
	// An app whose root package.json exists only for tooling (husky, commitlint) must
	// not shadow the real Vite project in frontend/.
	root := t.TempDir()
	write(t, filepath.Join(root, "package.json"), `{"scripts":{"lint":"eslint ."}}`)
	write(t, filepath.Join(root, "frontend", "package.json"), `{"scripts":{"build":"vite build"}}`)
	write(t, filepath.Join(root, "frontend", "pnpm-lock.yaml"), "lockfileVersion: '9.0'\n")

	project, err := Detect(root, "myapp")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if project == nil {
		t.Fatal("expected frontend/ to be detected")
	}
	if project.Rel != "frontend" {
		t.Errorf("Rel = %q, want frontend", project.Rel)
	}
	if project.PkgManager != "pnpm" {
		t.Errorf("PkgManager = %q, want pnpm (frontend/pnpm-lock.yaml)", project.PkgManager)
	}
}

func TestDetectFindsSubdirectoryWithNoRootPackageJSON(t *testing.T) {
	// frappe_ai's shape: no root package.json at all, Vite project in frontend/.
	root := t.TempDir()
	write(t, filepath.Join(root, "frontend", "package.json"), `{"scripts":{"build":"vite build"}}`)

	project, err := Detect(root, "frappe_ai")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if project == nil || project.Rel != "frontend" {
		t.Fatalf("expected frontend/, got %+v", project)
	}
}

func TestDetectFindsHelpdeskStyleDeskDirectory(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "desk", "package.json"), `{"scripts":{"build":"vite build"}}`)
	write(t, filepath.Join(root, "desk", "package-lock.json"), `{"lockfileVersion":3}`)

	project, err := Detect(root, "helpdesk")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if project == nil || project.Rel != "desk" {
		t.Fatalf("expected desk/, got %+v", project)
	}
	if project.PkgManager != "npm" {
		t.Errorf("PkgManager = %q, want npm", project.PkgManager)
	}
}

func TestDetectReturnsNilForAppWithoutFrontend(t *testing.T) {
	// A classic app (erpnext-style: only *.bundle.js under public/) has no JS project
	// of its own. That is not an error — packaging carries on.
	root := t.TempDir()
	write(t, filepath.Join(root, "erpnext", "public", "js", "erpnext.bundle.js"), "// bundle\n")

	project, err := Detect(root, "erpnext")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if project != nil {
		t.Fatalf("expected no frontend, got %+v", project)
	}
}

func TestDetectIgnoresPackageJSONWithoutBuildScript(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "frontend", "package.json"), `{"scripts":{"dev":"vite"}}`)

	project, err := Detect(root, "app")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if project != nil {
		t.Fatalf("a frontend with no build script is not buildable, got %+v", project)
	}
}

func TestDetectReportsMalformedPackageJSON(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "package.json"), `{not json`)

	if _, err := Detect(root, "app"); err == nil {
		t.Fatal("expected a parse error for a malformed package.json")
	}
}

func TestDetectUsesPackageManagerFieldWhenNoLockfile(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "frontend", "package.json"),
		`{"packageManager":"pnpm@9.1.0","scripts":{"build":"vite build"}}`)

	project, err := Detect(root, "app")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if project.PkgManager != "pnpm" {
		t.Errorf("PkgManager = %q, want pnpm from the packageManager field", project.PkgManager)
	}
	if project.HasLockfile {
		t.Error("HasLockfile must be false without a lockfile, so the install is not frozen")
	}
}

func TestDetectFallsBackToRootLockfileForDelegatedSubdirectory(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "pnpm-lock.yaml"), "lockfileVersion: '9.0'\n")
	write(t, filepath.Join(root, "frontend", "package.json"), `{"scripts":{"build":"vite build"}}`)

	project, err := Detect(root, "app")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if project.PkgManager != "pnpm" {
		t.Errorf("PkgManager = %q, want pnpm from the root lockfile", project.PkgManager)
	}
}

func TestInstallArgsAreFrozenOnlyWithALockfile(t *testing.T) {
	cases := []struct {
		pm       string
		lock     bool
		wantArgs string
	}{
		{"pnpm", true, "pnpm install --prod=false --frozen-lockfile"},
		{"pnpm", false, "pnpm install --prod=false"},
		{"npm", true, "npm ci --include=dev --no-audit --no-fund"},
		{"npm", false, "npm install --include=dev --no-audit --no-fund"},
		{"yarn", true, "yarn install --check-files --non-interactive --production=false"},
		{"yarn", false, "yarn install --check-files --non-interactive --production=false"},
	}
	for _, c := range cases {
		got := strings.Join(installArgs(&Project{PkgManager: c.pm, HasLockfile: c.lock}), " ")
		if got != c.wantArgs {
			t.Errorf("installArgs(%s, lock=%v) = %q, want %q", c.pm, c.lock, got, c.wantArgs)
		}
	}
}

func TestEnsureWWWEntryWritesTheRouteTemplate(t *testing.T) {
	// An app whose build script stops at `vite build` (no copy-html-entry) leaves the
	// SPA unroutable. fpm writes the template frappe renders at /<app>.
	root := crmLayout(t)
	write(t, filepath.Join(root, "crm", "public", "frontend", "index.html"), spaHTML("crm", "frontend"))

	written, err := EnsureWWWEntry(root, "crm")
	if err != nil {
		t.Fatalf("EnsureWWWEntry: %v", err)
	}
	if len(written) != 1 || written[0] != "crm/www/crm.html" {
		t.Fatalf("written = %v, want [crm/www/crm.html]", written)
	}
	got, err := os.ReadFile(filepath.Join(root, "crm", "www", "crm.html"))
	if err != nil {
		t.Fatalf("read www template: %v", err)
	}
	if string(got) != spaHTML("crm", "frontend") {
		t.Errorf("www template content = %q", got)
	}
}

func TestEnsureWWWEntryNeverOverwritesAnExistingTemplate(t *testing.T) {
	// crm's own build already wrote it, or the app hand-maintains one with its own
	// jinja context. Either way the app owns that file.
	root := crmLayout(t)
	write(t, filepath.Join(root, "crm", "public", "frontend", "index.html"), spaHTML("crm", "frontend"))
	write(t, filepath.Join(root, "crm", "www", "crm.html"), "hand written")

	written, err := EnsureWWWEntry(root, "crm")
	if err != nil {
		t.Fatalf("EnsureWWWEntry: %v", err)
	}
	if len(written) != 0 {
		t.Errorf("written = %v, want none (nothing to do)", written)
	}
	got, _ := os.ReadFile(filepath.Join(root, "crm", "www", "crm.html"))
	if string(got) != "hand written" {
		t.Errorf("the existing template was overwritten: %q", got)
	}
}

func TestEnsureWWWEntryDoesNothingWithoutAnSPAEntry(t *testing.T) {
	root := crmLayout(t)
	written, err := EnsureWWWEntry(root, "crm")
	if err != nil {
		t.Fatalf("EnsureWWWEntry: %v", err)
	}
	if len(written) != 0 {
		t.Errorf("written = %v, want none", written)
	}
}

func TestOutputsDescribesABuiltSPA(t *testing.T) {
	root := crmLayout(t)
	write(t, filepath.Join(root, "crm", "public", "frontend", "index.html"), spaHTML("crm", "frontend"))
	write(t, filepath.Join(root, "crm", "public", "frontend", "assets", "index-A1B2C3.js"), "console.log(1)")
	write(t, filepath.Join(root, "crm", "www", "crm.html"), spaHTML("crm", "frontend"))

	out, err := Outputs(root, "crm")
	if err != nil {
		t.Fatalf("Outputs: %v", err)
	}
	if got := strings.Join(out.Dirs, ","); got != "crm/public/frontend" {
		t.Errorf("Dirs = %q", got)
	}
	if got := strings.Join(out.Entries, ","); got != "crm/public/frontend/index.html" {
		t.Errorf("Entries = %q", got)
	}
	if got := strings.Join(out.Routes, ","); got != "crm/www/crm.html" {
		t.Errorf("Routes = %q", got)
	}
	if out.Files != 2 {
		t.Errorf("Files = %d, want 2", out.Files)
	}
	if !out.Any() {
		t.Error("Any() must be true for a built SPA")
	}
}

func TestOutputsIsEmptyForAnUnbuiltCheckout(t *testing.T) {
	out, err := Outputs(crmLayout(t), "crm")
	if err != nil {
		t.Fatalf("Outputs: %v", err)
	}
	if out.Any() {
		t.Errorf("expected nothing built, got %+v", out)
	}
}

func TestOutputsSeesClassicBundlesToo(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "erpnext", "public", "dist", "js", "erpnext.bundle.ABC.js"), "//x")

	out, err := Outputs(root, "erpnext")
	if err != nil {
		t.Fatalf("Outputs: %v", err)
	}
	if got := strings.Join(out.Dirs, ","); got != "erpnext/public/dist" {
		t.Errorf("Dirs = %q", got)
	}
	if !out.Any() {
		t.Error("Any() must be true when classic bundles exist")
	}
}

// fakePackageManager puts an executable named `name` on PATH that appends each
// invocation to a log and, when told to build, creates outputs. It lets these tests
// exercise the full install→build→verify path without node installed.
func fakePackageManager(t *testing.T, name string, outputs []string) (logPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake package manager is a POSIX shell script")
	}
	binDir := t.TempDir()
	logPath = filepath.Join(binDir, "invocations.log")

	// $1 is the subcommand: `install` records only; anything else (`build`, `run
	// build`) records and materialises the build outputs, whose paths are given
	// relative to $FAKE_ROOT.
	script := `#!/bin/sh
echo "$PWD|$@" >> "` + logPath + `"
case "$1" in
  install|ci) exit 0 ;;
esac
# Stands in for the frontend's import of the bench config: the build must fail if the
# file is not there at the moment the bundler runs.
if [ -n "$FAKE_REQUIRE" ] && [ ! -f "$FAKE_REQUIRE" ]; then
  echo "Could not resolve \"../../../../sites/common_site_config.json\"" >&2
  exit 1
fi
if [ "$FAKE_FAIL" = "1" ]; then
  echo "vite: Rollup failed to resolve import" >&2
  exit 1
fi
IFS=':'
for out in $FAKE_OUTPUTS; do
  [ -z "$out" ] && continue
  mkdir -p "$FAKE_ROOT/$(dirname "$out")"
  printf 'built' > "$FAKE_ROOT/$out"
done
exit 0
`
	write(t, filepath.Join(binDir, name), script)
	if err := os.Chmod(filepath.Join(binDir, name), 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_OUTPUTS", strings.Join(outputs, ":"))
	return logPath
}

func TestBuildRunsInstallThenBuildAndRecordsTheOutput(t *testing.T) {
	root := crmLayout(t)
	t.Setenv("FAKE_ROOT", root)
	logPath := fakePackageManager(t, "yarn", []string{
		"crm/public/frontend/index.html",
		"crm/public/frontend/assets/index-A1B2C3.js",
	})

	var out strings.Builder
	res, err := Build(BuildOptions{SourcePath: root, AppName: "crm", Stdout: &out})
	if err != nil {
		t.Fatalf("Build: %v\n%s", err, out.String())
	}
	if !res.Built {
		t.Fatal("expected Built")
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read invocation log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(log)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected exactly one install and one build (no double build), got %d:\n%s", len(lines), log)
	}
	if !strings.Contains(lines[0], "|install --check-files --non-interactive") {
		t.Errorf("first invocation = %q, want the yarn install", lines[0])
	}
	if !strings.HasSuffix(lines[1], "|build") {
		t.Errorf("second invocation = %q, want the yarn build", lines[1])
	}
	// Both must run in the checkout root, since crm's root script delegates.
	for _, line := range lines {
		cwd := strings.SplitN(line, "|", 2)[0]
		if resolved, _ := filepath.EvalSymlinks(cwd); resolved != mustEval(t, root) {
			t.Errorf("ran in %q, want the checkout root %q", cwd, root)
		}
	}

	if got := strings.Join(res.Output.Dirs, ","); got != "crm/public/frontend" {
		t.Errorf("Dirs = %q", got)
	}
	// The fake build did not run copy-html-entry, so fpm must supply the route.
	if got := strings.Join(res.Output.Routes, ","); got != "crm/www/crm.html" {
		t.Errorf("Routes = %q, want the template fpm writes", got)
	}
	if _, err := os.Stat(filepath.Join(root, "crm", "www", "crm.html")); err != nil {
		t.Errorf("the SPA route template was not written: %v", err)
	}
}

func TestBuildFailsWhenTheBuildProducesNothing(t *testing.T) {
	// The failure this package exists to prevent: a package that installs cleanly and
	// then serves a blank page. A build that writes nothing under <app>/public must
	// stop packaging rather than ship.
	root := crmLayout(t)
	t.Setenv("FAKE_ROOT", root)
	fakePackageManager(t, "yarn", nil)

	var out strings.Builder
	_, err := Build(BuildOptions{SourcePath: root, AppName: "crm", Stdout: &out})
	if err == nil {
		t.Fatal("expected an error when the build produced no assets")
	}
	if !errors.Is(err, ErrBuildFailed) {
		t.Errorf("error does not wrap ErrBuildFailed: %v", err)
	}
	if !strings.Contains(err.Error(), "blank page") {
		t.Errorf("error should say what breaks: %v", err)
	}
}

func TestBuildSurfacesTheBuildLogOnFailure(t *testing.T) {
	root := crmLayout(t)
	t.Setenv("FAKE_ROOT", root)
	fakePackageManager(t, "yarn", nil)
	t.Setenv("FAKE_FAIL", "1")

	var out strings.Builder
	_, err := Build(BuildOptions{SourcePath: root, AppName: "crm", Stdout: &out})
	if err == nil {
		t.Fatal("expected the failing build to be an error")
	}
	if !errors.Is(err, ErrBuildFailed) {
		t.Errorf("error does not wrap ErrBuildFailed: %v", err)
	}
	if !strings.Contains(err.Error(), "Rollup failed to resolve import") {
		t.Errorf("the package manager's output must reach the user: %v", err)
	}
}

func TestBuildIsANoOpForAnAppWithoutAFrontend(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "erpnext", "hooks.py"), "app_name = \"erpnext\"\n")

	var out strings.Builder
	res, err := Build(BuildOptions{SourcePath: root, AppName: "erpnext", Stdout: &out})
	if err != nil {
		t.Fatalf("an app without a frontend must not be an error: %v", err)
	}
	if res.Built || res.Project != nil {
		t.Errorf("expected nothing built, got %+v", res)
	}
	if out.String() != "" {
		t.Errorf("expected no output, got %q", out.String())
	}
}

func TestBuildReportsAMissingPackageManager(t *testing.T) {
	root := crmLayout(t)
	// An empty PATH makes every package manager unresolvable.
	t.Setenv("PATH", t.TempDir())

	_, err := Build(BuildOptions{SourcePath: root, AppName: "crm", Stdout: &strings.Builder{}})
	if err == nil {
		t.Fatal("expected an error when the package manager is missing")
	}
	if !strings.Contains(err.Error(), "not on PATH") || !strings.Contains(err.Error(), "--build-frontend=false") {
		t.Errorf("the error should name the missing tool and the escape hatch: %v", err)
	}
}

func TestBuildEnvSetsDefaultsWithoutOverridingTheCaller(t *testing.T) {
	t.Setenv("NODE_OPTIONS", "--max-old-space-size=8192")
	env := buildEnv([]string{"VITE_BASE=/assets/crm/frontend/"})

	if got := lastValue(env, "NODE_OPTIONS"); got != "--max-old-space-size=8192" {
		t.Errorf("NODE_OPTIONS = %q, an explicit value must win", got)
	}
	// NODE_ENV must not be forced to production: yarn would then skip the
	// devDependencies (autoprefixer, postcss, tailwindcss) the build needs.
	if got := lastValue(env, "NODE_ENV"); got == "production" {
		t.Error("NODE_ENV must not be set to production; it makes yarn skip devDependencies")
	}
	if got := lastValue(env, "VITE_BASE"); got != "/assets/crm/frontend/" {
		t.Errorf("caller env was dropped: %q", got)
	}
}

func lastValue(env []string, key string) string {
	value := ""
	for _, kv := range env {
		if strings.HasPrefix(kv, key+"=") {
			value = kv[len(key)+1:]
		}
	}
	return value
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", p, err)
	}
	return resolved
}

// TestOutputsDiscoversANonFrontendOutputDirectory is erpnext: its SPA lives in
// banking/, builds into erpnext/public/banking with --base=/assets/erpnext/banking/,
// and routes at erpnext/www/banking.html. Assuming the directory is called "frontend"
// would report erpnext as having built nothing and fail packaging outright.
func TestOutputsDiscoversANonFrontendOutputDirectory(t *testing.T) {
	root := erpnextLayout(t)
	write(t, filepath.Join(root, "erpnext", "public", "banking", "index.html"), spaHTML("erpnext", "banking"))
	write(t, filepath.Join(root, "erpnext", "public", "banking", "assets", "app-XYZ.js"), "//x")
	write(t, filepath.Join(root, "erpnext", "www", "banking.html"), spaHTML("erpnext", "banking"))

	out, err := Outputs(root, "erpnext")
	if err != nil {
		t.Fatalf("Outputs: %v", err)
	}
	if got := strings.Join(out.Dirs, ","); got != "erpnext/public/banking" {
		t.Errorf("Dirs = %q, want erpnext/public/banking", got)
	}
	if got := strings.Join(out.Routes, ","); got != "erpnext/www/banking.html" {
		t.Errorf("Routes = %q; erpnext's 23 DocType portal routes must not be listed as "+
			"frontend routes, only the template that loads the built SPA", got)
	}
	if out.Files != 2 {
		t.Errorf("Files = %d, want 2 (static public/js must not be counted)", out.Files)
	}
}

// TestEnsureWWWEntryTakesTheRouteNameFromHooks: the route name follows no convention.
// crm routes at crm, but insights routes at _insights, builder at _builder and gameplan
// at g — none of which can be derived from the app name or the output directory. Only
// the app's own hooks.py knows, and a template frappe does not route to is a dead page.
func TestEnsureWWWEntryTakesTheRouteNameFromHooks(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "insights", "hooks.py"), `app_name = "insights"
website_route_rules = [
    {"from_route": f"/{insights_path}/<path:app_path>", "to_route": "_insights"},
    {"from_route": f"/{insights_path}", "to_route": "_insights"},
]
`)
	write(t, filepath.Join(root, "insights", "public", "frontend", "index.html"), spaHTML("insights", "frontend"))

	written, err := EnsureWWWEntry(root, "insights")
	if err != nil {
		t.Fatalf("EnsureWWWEntry: %v", err)
	}
	if len(written) != 1 || written[0] != "insights/www/_insights.html" {
		t.Fatalf("written = %v, want [insights/www/_insights.html] — not insights.html", written)
	}
}

// TestEnsureWWWEntryRefusesWhenTheRouteIsAmbiguous: erpnext declares 24 to_routes, most
// of them DocTypes rather than templates. Filling in every one without a file would
// scatter copies of the banking SPA across a dozen unrelated URLs.
func TestEnsureWWWEntryRefusesWhenTheRouteIsAmbiguous(t *testing.T) {
	root := erpnextLayout(t)
	write(t, filepath.Join(root, "erpnext", "public", "banking", "index.html"), spaHTML("erpnext", "banking"))

	written, err := EnsureWWWEntry(root, "erpnext")
	if err != nil {
		t.Fatalf("EnsureWWWEntry: %v", err)
	}
	if len(written) != 0 {
		t.Fatalf("written = %v; with 24 declared routes fpm cannot know which is the SPA's", written)
	}
	if _, err := os.Stat(filepath.Join(root, "erpnext", "www", "Sales Order.html")); err == nil {
		t.Error("a DocType to_route must never become a www template")
	}
}

// TestEnsureWWWEntryDoesNothingWithoutADeclaredRoute: an app that declares no
// website_route_rules gives fpm no name to use, and inventing one produces a page
// frappe never serves.
func TestEnsureWWWEntryDoesNothingWithoutADeclaredRoute(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "app", "hooks.py"), "app_name = \"app\"\n")
	write(t, filepath.Join(root, "app", "public", "frontend", "index.html"), spaHTML("app", "frontend"))

	written, err := EnsureWWWEntry(root, "app")
	if err != nil {
		t.Fatalf("EnsureWWWEntry: %v", err)
	}
	if len(written) != 0 {
		t.Fatalf("written = %v, want none", written)
	}
}

// TestOutputsTreatsLibraryModeOutputAsBuilt is frappe_ai: its vite outDir is
// frappe_ai/public/frontend/dist in library mode, so there is no index.html anywhere.
// Requiring one would fail that app's packaging.
func TestOutputsTreatsLibraryModeOutputAsBuilt(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "frappe_ai", "public", "frontend", "dist", "js", "frappe_ai.js"), "//lib")

	out, err := Outputs(root, "frappe_ai")
	if err != nil {
		t.Fatalf("Outputs: %v", err)
	}
	if !out.Any() {
		t.Fatalf("library-mode output must count as built, got %+v", out)
	}
	if got := strings.Join(out.Dirs, ","); got != "frappe_ai/public/frontend/dist" {
		t.Errorf("Dirs = %q", got)
	}
	if len(out.Entries) != 0 {
		t.Errorf("Entries = %v, a library build has no index.html", out.Entries)
	}
}

// TestOutputsCollapsesNestedOutputRoots keeps a dist/ inside an SPA root from being
// reported as a second, separate frontend.
func TestOutputsCollapsesNestedOutputRoots(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "app", "public", "frontend", "index.html"), "<!doctype html>")
	write(t, filepath.Join(root, "app", "public", "frontend", "dist", "chunk.js"), "//x")

	out, err := Outputs(root, "app")
	if err != nil {
		t.Fatalf("Outputs: %v", err)
	}
	if got := strings.Join(out.Dirs, ","); got != "app/public/frontend" {
		t.Errorf("Dirs = %q, want only the SPA root", got)
	}
}

// TestOutputsIgnoresNodeModulesUnderPublic: some apps (wiki) keep an install-time
// node_modules under public/. It is not build output and must not be measured.
func TestOutputsIgnoresNodeModulesUnderPublic(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "wiki", "public", "node_modules", "pkg", "index.html"), "<!doctype html>")
	write(t, filepath.Join(root, "wiki", "public", "frontend", "index.html"), "<!doctype html>")

	out, err := Outputs(root, "wiki")
	if err != nil {
		t.Fatalf("Outputs: %v", err)
	}
	if got := strings.Join(out.Dirs, ","); got != "wiki/public/frontend" {
		t.Errorf("Dirs = %q, node_modules must be skipped", got)
	}
}

// TestInstallArgsAlwaysIncludeDevDependencies guards a real failure: crm keeps
// autoprefixer, postcss and tailwindcss in devDependencies, so an install that honours
// an inherited NODE_ENV=production succeeds and the build then dies with
// "Cannot find module 'autoprefixer'".
func TestInstallArgsAlwaysIncludeDevDependencies(t *testing.T) {
	for _, pm := range []string{"yarn", "npm", "pnpm"} {
		for _, lock := range []bool{true, false} {
			args := strings.Join(installArgs(&Project{PkgManager: pm, HasLockfile: lock}), " ")
			switch pm {
			case "yarn":
				if !strings.Contains(args, "--production=false") {
					t.Errorf("yarn install (lock=%v) must force devDependencies: %q", lock, args)
				}
			case "npm":
				if !strings.Contains(args, "--include=dev") {
					t.Errorf("npm install (lock=%v) must force devDependencies: %q", lock, args)
				}
			case "pnpm":
				if !strings.Contains(args, "--prod=false") {
					t.Errorf("pnpm install (lock=%v) must force devDependencies: %q", lock, args)
				}
			}
		}
	}
}

// TestBuildEnvDoesNotForceProductionNodeEnv is the environment half of the same bug.
func TestBuildEnvDoesNotForceProductionNodeEnv(t *testing.T) {
	os.Unsetenv("NODE_ENV")
	if got := lastValue(buildEnv(nil), "NODE_ENV"); got != "" {
		t.Errorf("NODE_ENV = %q; the build must not set it, or yarn skips devDependencies", got)
	}
}

// TestBuildFailureExplainsTheBenchRequirement covers the one frontend failure whose
// cause is fpm's own doing. crm's frontend/src/socket.js imports
// ../../../../sites/common_site_config.json, so the build only works from
// <bench>/apps/crm. Raw rollup output gives the user no way to know that.
func TestBuildFailureExplainsTheBenchRequirement(t *testing.T) {
	root := crmLayout(t)
	t.Setenv("FAKE_ROOT", root)
	fakePackageManager(t, "yarn", nil)
	t.Setenv("FAKE_FAIL", "1")
	t.Setenv("FAKE_FAIL_MESSAGE", "")

	_, err := Build(BuildOptions{SourcePath: root, AppName: "crm", Stdout: &strings.Builder{}})
	if err == nil {
		t.Fatal("expected the failing build to be an error")
	}
	// The fake emits "Rollup failed to resolve import", which contains no bench
	// marker, so no hint is expected here — only the raw failure.
	if strings.Contains(err.Error(), "--bench-path") {
		t.Errorf("an unrelated build failure must not blame the bench: %v", err)
	}
}

func TestBenchHintFiresOnlyOutsideABenchAndForBenchErrors(t *testing.T) {
	outside := t.TempDir()
	benchErr := `Could not resolve "../../../../sites/common_site_config.json" from "src/socket.js"`

	if hint := benchHint(outside, "crm", benchErr); !strings.Contains(hint, "--bench-path") {
		t.Errorf("a bench-resolution failure outside a bench must point at --bench-path, got %q", hint)
	}
	if hint := benchHint(outside, "crm", "TypeError: undefined is not a function"); hint != "" {
		t.Errorf("an unrelated failure must not blame the bench, got %q", hint)
	}

	// A checkout that already lives at <bench>/apps/<app> is correctly located, so the
	// hint would be wrong even for a bench-shaped error.
	bench := t.TempDir()
	inside := filepath.Join(bench, "apps", "crm")
	write(t, filepath.Join(bench, "sites", "common_site_config.json"), "{}")
	write(t, filepath.Join(inside, "crm", "hooks.py"), "app_name = \"crm\"\n")
	if !InsideBench(inside) {
		t.Fatal("InsideBench must recognise <bench>/apps/<app>")
	}
	if hint := benchHint(inside, "crm", benchErr); hint != "" {
		t.Errorf("a checkout already inside a bench must not be told to use --bench-path, got %q", hint)
	}
}

// TestBuildRecognisesOutputWithNoIndexHTMLOrDist covers the false negative the scan
// heuristics alone produce: a build that writes <app>/public/frontend/js/app.js has no
// index.html and no dist/ directory, so nothing about it looks like build output — yet
// it is exactly what the build just wrote. Build knows because it compared before and
// after; failing here would reject a perfectly good package.
func TestBuildRecognisesOutputWithNoIndexHTMLOrDist(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "frontend", "package.json"), `{"scripts":{"build":"x"}}`)
	write(t, filepath.Join(root, "app", "hooks.py"), "app_name = \"app\"\n")
	// A hand-maintained static asset that exists before the build and must not be
	// mistaken for output.
	write(t, filepath.Join(root, "app", "public", "js", "legacy.js"), "// hand written")

	t.Setenv("FAKE_ROOT", root)
	fakePackageManager(t, "yarn", []string{"app/public/frontend/js/app.js"})

	var out strings.Builder
	res, err := Build(BuildOptions{SourcePath: root, AppName: "app", Stdout: &out})
	if err != nil {
		t.Fatalf("Build: %v\n%s", err, out.String())
	}
	if !res.Built {
		t.Fatal("expected Built")
	}
	if got := strings.Join(res.Output.Dirs, ","); got != "app/public/frontend" {
		t.Errorf("Dirs = %q, want app/public/frontend (and not the pre-existing public/js)", got)
	}
}

// TestBuildAdoptsOutputLeftBesideTheProject: a build whose outDir is its own dist/
// leaves nothing under <app>/public, and only <app>/public is linked into
// sites/assets — so frappe could not serve it. It is moved into the app module, and to
// public/<project>, never public/dist, which is reserved for the hashed *.bundle.*
// files that go into assets.json.
func TestBuildAdoptsOutputLeftBesideTheProject(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "frontend", "package.json"), `{"scripts":{"build":"x"}}`)
	write(t, filepath.Join(root, "app", "hooks.py"), "app_name = \"app\"\n")

	t.Setenv("FAKE_ROOT", root)
	fakePackageManager(t, "yarn", []string{"frontend/dist/js/app.js"})

	var out strings.Builder
	res, err := Build(BuildOptions{SourcePath: root, AppName: "app", Stdout: &out})
	if err != nil {
		t.Fatalf("Build: %v\n%s", err, out.String())
	}
	if got := strings.Join(res.Output.Dirs, ","); got != "app/public/frontend" {
		t.Errorf("Dirs = %q, want app/public/frontend", got)
	}
	if _, err := os.Stat(filepath.Join(root, "app", "public", "frontend", "js", "app.js")); err != nil {
		t.Errorf("output was not moved into the app module: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "app", "public", "dist")); err == nil {
		t.Error("output must not land in public/dist; that is the esbuild bundle directory")
	}
}

// benchImportingApp is crm's real shape for this purpose: a frontend whose socket
// module imports the bench's config, in a checkout that is not inside a bench.
func benchImportingApp(t *testing.T, parent string) string {
	t.Helper()
	root := filepath.Join(parent, "repos", "crm")
	write(t, filepath.Join(root, "package.json"), `{"scripts":{"build":"cd frontend && yarn build"}}`)
	write(t, filepath.Join(root, "yarn.lock"), "# yarn lockfile v1\n")
	write(t, filepath.Join(root, "frontend", "package.json"), `{"scripts":{"build":"vite build"}}`)
	write(t, filepath.Join(root, "frontend", "src", "socket.js"),
		"import { socketio_port } from '../../../../sites/common_site_config.json'\n")
	write(t, filepath.Join(root, "crm", "hooks.py"), `app_name = "crm"
website_route_rules = [
    {"from_route": "/crm/<path:app_path>", "to_route": "crm"},
]
`)
	return root
}

func TestRequiresBenchDetectsTheConfigImport(t *testing.T) {
	root := benchImportingApp(t, t.TempDir())
	if !RequiresBench(root) {
		t.Error("a frontend importing sites/common_site_config.json needs a bench")
	}
	// insights and builder have no such import and build anywhere.
	plain := t.TempDir()
	write(t, filepath.Join(plain, "frontend", "src", "main.js"), "import {createApp} from 'vue'\n")
	if RequiresBench(plain) {
		t.Error("a frontend with no bench import must not be forced through a scaffold")
	}
}

func TestRequiresBenchIgnoresBuiltOutputAndDependencies(t *testing.T) {
	// A previous build's output under public/ embeds the import's text in a bundle,
	// and node_modules is full of unrelated code. Neither is a source file.
	root := t.TempDir()
	write(t, filepath.Join(root, "app", "public", "frontend", "assets", "index-A1.js"),
		"//sites/common_site_config.json\n")
	write(t, filepath.Join(root, "frontend", "node_modules", "pkg", "index.js"),
		"require('../../../../sites/common_site_config.json')\n")
	if RequiresBench(root) {
		t.Error("built output and node_modules must not be scanned")
	}
}

// TestBuildWritesTheMissingBenchConfigInPlace is the cheap path: the checkout needs
// exactly one file two levels up to resolve its import, so fpm writes it rather than
// copying the tree, and removes it afterwards.
func TestBuildWritesTheMissingBenchConfigInPlace(t *testing.T) {
	parent := t.TempDir()
	root := benchImportingApp(t, parent)
	configPath := filepath.Join(parent, "sites", "common_site_config.json")

	t.Setenv("FAKE_ROOT", root)
	// The build asserts the config is readable at the moment it runs, which is the
	// whole point of writing it.
	fakePackageManager(t, "yarn", []string{"crm/public/frontend/index.html"})
	t.Setenv("FAKE_REQUIRE", configPath)

	var out strings.Builder
	res, err := Build(BuildOptions{SourcePath: root, AppName: "crm", Stdout: &out})
	if err != nil {
		t.Fatalf("Build: %v\n%s", err, out.String())
	}
	if !res.Built {
		t.Fatal("expected Built")
	}
	if res.BuildRoot != root {
		t.Errorf("BuildRoot = %q; the in-place path builds the checkout itself", res.BuildRoot)
	}

	var cfg map[string]any
	if data, readErr := os.ReadFile(configPath); readErr != nil {
		t.Fatalf("the config was not written during the build: %v", readErr)
	} else if json.Unmarshal(data, &cfg) != nil {
		t.Fatal("the written config is not valid JSON")
	}
	if cfg["socketio_port"] != float64(9000) {
		t.Errorf("socketio_port = %v, want frappe's default 9000", cfg["socketio_port"])
	}

	res.Cleanup()
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Error("the synthesized config must be removed after the build")
	}
	if _, err := os.Stat(filepath.Join(parent, "sites")); !os.IsNotExist(err) {
		t.Error("the sites directory fpm created must be removed too")
	}
}

// TestBuildNeverOverwritesARealBenchConfig: a checkout that already sits in a bench
// must build against that bench's values, not synthesized ones.
func TestBuildNeverOverwritesARealBenchConfig(t *testing.T) {
	bench := t.TempDir()
	root := filepath.Join(bench, "apps", "crm")
	write(t, filepath.Join(root, "package.json"), `{"scripts":{"build":"cd frontend && yarn build"}}`)
	write(t, filepath.Join(root, "frontend", "package.json"), `{"scripts":{"build":"vite build"}}`)
	write(t, filepath.Join(root, "frontend", "src", "socket.js"),
		"import { socketio_port } from '../../../../sites/common_site_config.json'\n")
	write(t, filepath.Join(root, "crm", "hooks.py"), "app_name = \"crm\"\n")
	real := `{"socketio_port": 9123}`
	write(t, filepath.Join(bench, "sites", "common_site_config.json"), real)

	t.Setenv("FAKE_ROOT", root)
	fakePackageManager(t, "yarn", []string{"crm/public/frontend/index.html"})

	var out strings.Builder
	res, err := Build(BuildOptions{SourcePath: root, AppName: "crm", Stdout: &out})
	if err != nil {
		t.Fatalf("Build: %v\n%s", err, out.String())
	}
	res.Cleanup()

	got, _ := os.ReadFile(filepath.Join(bench, "sites", "common_site_config.json"))
	if string(got) != real {
		t.Errorf("the bench's own config was modified: %q", got)
	}
}

// TestBuildUsesASuppliedSiteConfig covers --frontend-site-config: a real bench's
// values, for a deployment where the compiled-in port does matter.
func TestBuildUsesASuppliedSiteConfig(t *testing.T) {
	parent := t.TempDir()
	root := benchImportingApp(t, parent)
	supplied := filepath.Join(t.TempDir(), "common_site_config.json")
	write(t, supplied, `{"socketio_port": 9345, "webserver_port": 8001}`)

	t.Setenv("FAKE_ROOT", root)
	fakePackageManager(t, "yarn", []string{"crm/public/frontend/index.html"})

	var out strings.Builder
	res, err := Build(BuildOptions{SourcePath: root, AppName: "crm", Stdout: &out, SiteConfigPath: supplied})
	if err != nil {
		t.Fatalf("Build: %v\n%s", err, out.String())
	}
	data, readErr := os.ReadFile(filepath.Join(parent, "sites", "common_site_config.json"))
	if readErr != nil {
		t.Fatalf("config not written: %v", readErr)
	}
	var cfg map[string]any
	json.Unmarshal(data, &cfg)
	if cfg["socketio_port"] != float64(9345) {
		t.Errorf("socketio_port = %v, want the supplied 9345", cfg["socketio_port"])
	}
	res.Cleanup()
}

func TestBuildRefusesToScaffoldWhenAskedNotTo(t *testing.T) {
	root := benchImportingApp(t, t.TempDir())
	t.Setenv("FAKE_ROOT", root)
	fakePackageManager(t, "yarn", nil)

	_, err := Build(BuildOptions{SourcePath: root, AppName: "crm", Stdout: &strings.Builder{}, NoScaffold: true})
	if err == nil {
		t.Fatal("expected --no-bench-scaffold to refuse")
	}
	if !errors.Is(err, ErrBuildFailed) || !strings.Contains(err.Error(), "resolves the bench from its own path") {
		t.Errorf("error should explain why: %v", err)
	}
}

// TestBuildStagesABenchWhenTheConfigCannotBeWritten covers the fallback: a checkout
// whose parent cannot be written (near the filesystem root, a read-only mount) gets
// staged into a throwaway bench instead, and the caller must package from there —
// packaging the original would ship none of what was built.
func TestBuildStagesABenchWhenTheConfigCannotBeWritten(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory permissions this test relies on")
	}
	parent := t.TempDir()
	root := benchImportingApp(t, parent)
	// ensureBenchConfig would create parent/sites; deny that.
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0o755) })

	t.Setenv("FAKE_ROOT", root)
	fakePackageManager(t, "yarn", []string{"crm/public/frontend/index.html"})
	// The staged copy is built in place, so the fake writes relative to its own
	// working directory rather than the original checkout.
	t.Setenv("FAKE_ROOT", ".")

	var out strings.Builder
	res, err := Build(BuildOptions{SourcePath: root, AppName: "crm", Stdout: &out})
	if err != nil {
		t.Fatalf("Build: %v\n%s", err, out.String())
	}
	if !res.Built {
		t.Fatal("expected Built")
	}
	if res.BuildRoot == root || res.BuildRoot == "" {
		t.Fatalf("BuildRoot = %q, want a staged copy elsewhere", res.BuildRoot)
	}
	// The staged tree is a real bench: the import four levels up must resolve.
	if !InsideBench(res.BuildRoot) {
		t.Errorf("%s is not inside a bench, so the frontend's import would not resolve", res.BuildRoot)
	}
	// The build output is in the staged copy, which is why it must be packaged from.
	if _, err := os.Stat(filepath.Join(res.BuildRoot, "crm", "public", "frontend", "index.html")); err != nil {
		t.Errorf("build output missing from the staged tree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "crm", "public", "frontend", "index.html")); err == nil {
		t.Error("the original checkout must be left untouched")
	}
	// Nothing was written next to the checkout.
	if _, err := os.Stat(filepath.Join(parent, "sites")); err == nil {
		t.Error("no sites directory should exist next to an unwritable checkout")
	}

	staged := res.BuildRoot
	res.Cleanup()
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Error("the staged bench must be removed by Cleanup")
	}
}

// TestBuildCleanupIsAlwaysSafeToCall: callers defer Cleanup before checking the error,
// so it must be non-nil on every return path, including the ones that never staged.
func TestBuildCleanupIsAlwaysSafeToCall(t *testing.T) {
	cases := map[string]func(t *testing.T) BuildOptions{
		"no frontend": func(t *testing.T) BuildOptions {
			root := t.TempDir()
			write(t, filepath.Join(root, "app", "hooks.py"), "app_name = \"app\"\n")
			return BuildOptions{SourcePath: root, AppName: "app", Stdout: &strings.Builder{}}
		},
		"missing package manager": func(t *testing.T) BuildOptions {
			root := crmLayout(t)
			t.Setenv("PATH", t.TempDir())
			return BuildOptions{SourcePath: root, AppName: "crm", Stdout: &strings.Builder{}}
		},
		"build fails": func(t *testing.T) BuildOptions {
			root := crmLayout(t)
			t.Setenv("FAKE_ROOT", root)
			fakePackageManager(t, "yarn", nil)
			t.Setenv("FAKE_FAIL", "1")
			return BuildOptions{SourcePath: root, AppName: "crm", Stdout: &strings.Builder{}}
		},
		"refused scaffold": func(t *testing.T) BuildOptions {
			root := benchImportingApp(t, t.TempDir())
			t.Setenv("FAKE_ROOT", root)
			fakePackageManager(t, "yarn", nil)
			return BuildOptions{SourcePath: root, AppName: "crm", Stdout: &strings.Builder{}, NoScaffold: true}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			res, _ := Build(setup(t))
			if res.Cleanup == nil {
				t.Fatal("Cleanup must never be nil; callers defer it before checking the error")
			}
			res.Cleanup()
			res.Cleanup() // and must tolerate being run twice
		})
	}
}

// TestDetectSkipsFrappesOwnBundler: frappe's root build script is `node esbuild` —
// the framework's bundler, which compiles every app in a bench and reads
// sites/apps.txt to know which. It is `bench build`, not an app frontend. Treating it
// as one failed frappe's mirror shard with "ENOENT: sites/apps.txt".
func TestDetectSkipsFrappesOwnBundler(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "package.json"), `{"name":"frappe","scripts":{"build":"node esbuild"}}`)
	write(t, filepath.Join(root, "esbuild", "esbuild.js"), "// frappe's bundler\n")
	write(t, filepath.Join(root, "frappe", "hooks.py"), "app_name = \"frappe\"\n")

	project, err := Detect(root, "frappe")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if project != nil {
		t.Fatalf("frappe's own bundler must not be treated as an app frontend, got %+v", project)
	}
}

// TestDetectStillBuildsAnAppThatUsesEsbuildItself: the skip needs both signals, so an
// app bundling its own frontend with esbuild is not mistaken for the framework.
func TestDetectStillBuildsAnAppThatUsesEsbuildItself(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "frontend", "package.json"),
		`{"scripts":{"build":"esbuild src/main.js --bundle --outfile=../app/public/frontend/app.js"}}`)
	write(t, filepath.Join(root, "app", "hooks.py"), "app_name = \"app\"\n")

	project, err := Detect(root, "app")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if project == nil || project.Rel != "frontend" {
		t.Fatalf("an app's own esbuild frontend must still build, got %+v", project)
	}
}

func TestUnfrozenInstallArgsOnlyWhereFreezingExists(t *testing.T) {
	if args, ok := unfrozenInstallArgs(&Project{PkgManager: "pnpm"}); !ok ||
		!strings.Contains(strings.Join(args, " "), "--no-frozen-lockfile") {
		t.Errorf("pnpm = %v (ok=%v)", args, ok)
	}
	if args, ok := unfrozenInstallArgs(&Project{PkgManager: "npm"}); !ok ||
		strings.Contains(strings.Join(args, " "), " ci ") {
		t.Errorf("npm must fall back off `npm ci`: %v (ok=%v)", args, ok)
	}
	// yarn 1 never freezes, so there is nothing to relax and nothing to retry.
	if _, ok := unfrozenInstallArgs(&Project{PkgManager: "yarn"}); ok {
		t.Error("yarn has no frozen mode to fall back from")
	}
}

// TestFrozenLockfileRefusalIsDistinguishedFromRealFailures: retrying unfrozen is only
// right when the manager declined over lockfile drift. A missing dependency or a
// network error must still fail, not be papered over by a looser install.
func TestFrozenLockfileRefusalIsDistinguishedFromRealFailures(t *testing.T) {
	refusals := []string{
		`[ERR_PNPM_LOCKFILE_CONFIG_MISMATCH] Cannot proceed with the frozen installation. The current "overrides" configuration doesn't match`,
		"ERR_PNPM_OUTDATED_LOCKFILE  Cannot install with frozen-lockfile",
		"`npm ci` can only install packages when your package.json and package-lock.json are in sync",
	}
	for _, log := range refusals {
		if !isFrozenLockfileRefusal(log) {
			t.Errorf("should be recognised as lockfile drift: %q", log)
		}
	}
	for _, log := range []string{
		"ERR_PNPM_FETCH_404  GET https://registry.npmjs.org/nope: Not Found",
		"error An unexpected error occurred: getaddrinfo ENOTFOUND registry.yarnpkg.com",
		"Error: Cannot find module 'autoprefixer'",
	} {
		if isFrozenLockfileRefusal(log) {
			t.Errorf("must NOT retry unfrozen for: %q", log)
		}
	}
}

// TestUnfrozenRetryReachesNestedInstalls: the retry's flag only reaches the command fpm
// runs. An app's own postinstall may run a nested install of its own — drive's runs
// `cd frontend && pnpm install` — which sees none of the command line and, with CI set,
// freezes again. npm and pnpm both read settings from npm_config_* environment
// variables, so the setting has to travel that way to reach the whole tree.
func TestUnfrozenRetryReachesNestedInstalls(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "frontend", "package.json"), `{"scripts":{"build":"vite build"}}`)
	write(t, filepath.Join(root, "frontend", "pnpm-lock.yaml"), "lockfileVersion: '9.0'\n")
	write(t, filepath.Join(root, "app", "hooks.py"), "app_name = \"app\"\n")

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "env.log")
	// Refuse the frozen install the way pnpm does, then record the environment the
	// retry runs with and succeed.
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  install)\n" +
		"    if [ \"$npm_config_frozen_lockfile\" != \"false\" ]; then\n" +
		"      echo '[ERR_PNPM_LOCKFILE_CONFIG_MISMATCH] Cannot proceed with the frozen installation' >&2\n" +
		"      exit 1\n" +
		"    fi\n" +
		"    echo \"retry saw npm_config_frozen_lockfile=$npm_config_frozen_lockfile\" >> " + logPath + "\n" +
		"    exit 0 ;;\n" +
		"esac\n" +
		"mkdir -p \"$FAKE_ROOT/app/public/frontend\" && printf x > \"$FAKE_ROOT/app/public/frontend/index.html\"\n"
	write(t, filepath.Join(binDir, "pnpm"), script)
	if err := os.Chmod(filepath.Join(binDir, "pnpm"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_ROOT", root)

	var out strings.Builder
	res, err := Build(BuildOptions{SourcePath: root, AppName: "app", Stdout: &out})
	if err != nil {
		t.Fatalf("Build: %v\n%s", err, out.String())
	}
	res.Cleanup()

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("the retry never ran: %v", err)
	}
	if !strings.Contains(string(log), "npm_config_frozen_lockfile=false") {
		t.Errorf("a nested install would not see the setting: %q", log)
	}
}

// TestUnfrozenNpmrcCarriesTheUsersConfig: the setting has to travel in a config file,
// because `npm run` rebuilds npm_config_* for its children and loses anything injected
// through the environment — which is how drive's nested `cd frontend && pnpm install`
// kept freezing. Replacing the user's npmrc wholesale would take their registry
// credentials with it, so it is copied in first.
func TestUnfrozenNpmrcCarriesTheUsersConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	existing := "//registry.example.com/:_authToken=secret\nregistry=https://registry.example.com\n"
	if err := os.WriteFile(filepath.Join(home, ".npmrc"), []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	path, cleanup, err := unfrozenNpmrc()
	if err != nil {
		t.Fatalf("unfrozenNpmrc: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "frozen-lockfile=false") {
		t.Errorf("the setting is missing: %q", got)
	}
	if !strings.Contains(got, "_authToken=secret") || !strings.Contains(got, "registry=https://registry.example.com") {
		t.Errorf("the user's own config was dropped, which would break a private registry: %q", got)
	}

	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the throwaway config must be removed")
	}
}

// With no ~/.npmrc there is simply nothing to carry.
func TestUnfrozenNpmrcWithoutAUserConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, cleanup, err := unfrozenNpmrc()
	if err != nil {
		t.Fatalf("unfrozenNpmrc: %v", err)
	}
	defer cleanup()
	data, _ := os.ReadFile(path)
	if strings.TrimSpace(string(data)) != "frozen-lockfile=false" {
		t.Errorf("contents = %q", data)
	}
}
