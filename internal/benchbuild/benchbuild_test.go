package benchbuild

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestStageCopiesSourceIntoBench: a checkout outside the bench is copied to
// <bench>/apps/<app> without .git/node_modules, and removed again by cleanup.
func TestStageCopiesSourceIntoBench(t *testing.T) {
	bench := t.TempDir()
	src := filepath.Join(t.TempDir(), "myapp")
	write(t, filepath.Join(src, "myapp", "hooks.py"), "app_name = \"myapp\"\n")
	write(t, filepath.Join(src, "package.json"), "{}")
	write(t, filepath.Join(src, ".git", "HEAD"), "ref: refs/heads/main\n")
	write(t, filepath.Join(src, "node_modules", "x", "index.js"), "x")
	write(t, filepath.Join(src, "myapp", "public", "js", "myapp.bundle.js"), "console.log(1)")

	root, cleanup, err := stageAppIntoBench(bench, "myapp", src, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(bench, "apps", "myapp")
	if root != want {
		t.Fatalf("build root = %s, want %s", root, want)
	}
	if info, err := os.Lstat(root); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("staged app must be a real directory, not a symlink (err=%v)", err)
	}
	for _, p := range []string{"myapp/hooks.py", "package.json", "myapp/public/js/myapp.bundle.js"} {
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			t.Fatalf("%s missing from staged copy", p)
		}
	}
	for _, p := range []string{".git", "node_modules"} {
		if _, err := os.Stat(filepath.Join(root, p)); !os.IsNotExist(err) {
			t.Fatalf("%s must not be staged", p)
		}
	}
	cleanup()
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("cleanup must remove the staged copy")
	}
}

// TestStageBuildsInPlaceWhenAlreadyInBench: a source that is <bench>/apps/<app> is
// used as is and cleanup leaves it alone.
func TestStageBuildsInPlaceWhenAlreadyInBench(t *testing.T) {
	bench := t.TempDir()
	src := filepath.Join(bench, "apps", "myapp")
	write(t, filepath.Join(src, "myapp", "hooks.py"), "app_name = \"myapp\"\n")

	root, cleanup, err := stageAppIntoBench(bench, "myapp", src, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if root != src {
		t.Fatalf("build root = %s, want %s", root, src)
	}
	cleanup()
	if _, err := os.Stat(filepath.Join(src, "myapp", "hooks.py")); err != nil {
		t.Fatal("in-place source must survive cleanup")
	}
}

// TestStageRefusesForeignEntry: an unrelated apps/<app> is never overwritten.
func TestStageRefusesForeignEntry(t *testing.T) {
	bench := t.TempDir()
	write(t, filepath.Join(bench, "apps", "myapp", "myapp", "hooks.py"), "other\n")
	src := filepath.Join(t.TempDir(), "myapp")
	write(t, filepath.Join(src, "myapp", "hooks.py"), "app_name = \"myapp\"\n")

	_, _, err := stageAppIntoBench(bench, "myapp", src, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "already has") {
		t.Fatalf("expected refusal, got %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(bench, "apps", "myapp", "myapp", "hooks.py")); string(data) != "other\n" {
		t.Fatal("existing entry must be untouched")
	}
}

func TestEnsureInAppsTxt(t *testing.T) {
	bench := t.TempDir()
	write(t, filepath.Join(bench, "sites", "apps.txt"), "frappe") // no trailing newline, as images ship it
	cleanup, err := ensureInAppsTxt(bench, "myapp")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(bench, "sites", "apps.txt"))
	if string(data) != "frappe\nmyapp\n" {
		t.Fatalf("apps.txt = %q", data)
	}
	cleanup()
	data, _ = os.ReadFile(filepath.Join(bench, "sites", "apps.txt"))
	if string(data) != "frappe" {
		t.Fatalf("apps.txt not restored: %q", data)
	}
}

func TestBuildRejectsUnusableBench(t *testing.T) {
	_, err := Build(Options{BenchPath: t.TempDir(), AppName: "x", SourcePath: t.TempDir(), Stdout: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "not a usable bench") {
		t.Fatalf("expected unusable-bench error, got %v", err)
	}
}

// fakeTool puts an executable of the given name on PATH for the test, and returns the
// file it logs its arguments and environment to.
func fakeTool(t *testing.T, dir, name, body string) string {
	t.Helper()
	logPath := filepath.Join(dir, name+".log")
	script := "#!/bin/sh\necho \"$@\" >> " + logPath + "\n" + body
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return logPath
}

// TestBuildWithoutAVirtualenvUsesFrappesEsbuild: a bench-shaped directory holding
// frappe's source but no python environment — a build workspace, not a bench — still
// compiles an app's assets, because frappe's asset pipeline is node. This is what lets
// a catalogue build ship compiled bundles instead of sources (issue #9).
func TestBuildWithoutAVirtualenvUsesFrappesEsbuild(t *testing.T) {
	bench := t.TempDir()
	binDir := t.TempDir()
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// A frappe checkout with its asset pipeline and node dependencies already there.
	write(t, filepath.Join(bench, "apps", "frappe", "esbuild", "esbuild.js"), "// frappe's pipeline")
	write(t, filepath.Join(bench, "apps", "frappe", "node_modules", ".yarn-integrity"), "{}")
	write(t, filepath.Join(bench, "sites", "apps.txt"), "frappe\n")

	src := filepath.Join(t.TempDir(), "myapp")
	write(t, filepath.Join(src, "myapp", "hooks.py"), "app_name = \"myapp\"\n")
	write(t, filepath.Join(src, "myapp", "public", "js", "myapp.bundle.js"), "console.log(1)")

	// The stand-in for node writes where esbuild writes: through sites/assets/<app>,
	// which only resolves if the build linked it to the app's public directory.
	nodeLog := fakeTool(t, binDir, "node", `
out="$FRAPPE_BENCH_ROOT/sites/assets/myapp/dist/js"
mkdir -p "$out" || exit 1
echo "console.log(1)" > "$out/myapp.bundle.ABCDEFGH.js"
echo "FRAPPE_BENCH_ROOT=$FRAPPE_BENCH_ROOT" >> `+filepath.Join(binDir, "node.env")+`
exit 0
`)
	yarnLog := fakeTool(t, binDir, "yarn", "exit 0\n")

	res, err := Build(Options{BenchPath: bench, AppName: "myapp", SourcePath: src, Stdout: io.Discard})
	if err != nil {
		t.Fatalf("build should succeed without a virtualenv: %v", err)
	}
	defer res.Cleanup()

	logged, err := os.ReadFile(nodeLog)
	if err != nil {
		t.Fatal("node was never invoked")
	}
	for _, want := range []string{"esbuild", "--production", "--apps myapp"} {
		if !strings.Contains(string(logged), want) {
			t.Fatalf("node args missing %q: %s", want, logged)
		}
	}
	env, _ := os.ReadFile(filepath.Join(binDir, "node.env"))
	if !strings.Contains(string(env), "FRAPPE_BENCH_ROOT="+bench) {
		t.Fatalf("esbuild must be told where the bench is, got %q", env)
	}
	if _, err := os.Stat(yarnLog); err == nil {
		t.Fatal("frappe's node dependencies were already installed; yarn must not run again")
	}

	// The bundle has to have landed in the app's own public/dist, which is what the
	// package ships — proving sites/assets/myapp was linked there.
	built := filepath.Join(res.BuildRoot, "myapp", "public", "dist", "js", "myapp.bundle.ABCDEFGH.js")
	if _, err := os.Stat(built); err != nil {
		t.Fatalf("the built bundle is not in the app's public/dist: %v", err)
	}
	if got := res.Bundles["myapp.bundle.js"]; got != "/assets/myapp/dist/js/myapp.bundle.ABCDEFGH.js" {
		t.Fatalf("bundle manifest entry = %q", got)
	}
}

// TestBuildRejectsADirectoryThatIsNeitherBenchNorWorkspace keeps the error specific:
// without a virtualenv and without frappe's pipeline there is nothing to build with.
func TestBuildRejectsADirectoryThatIsNeitherBenchNorWorkspace(t *testing.T) {
	bench := t.TempDir()
	write(t, filepath.Join(bench, "apps", "frappe", "README.md"), "not a checkout with esbuild")
	write(t, filepath.Join(bench, "sites", "apps.txt"), "frappe\n")
	src := filepath.Join(t.TempDir(), "myapp")
	write(t, filepath.Join(src, "myapp", "hooks.py"), "app_name = \"myapp\"\n")

	_, err := Build(Options{BenchPath: bench, AppName: "myapp", SourcePath: src, Stdout: io.Discard})
	if err == nil {
		t.Fatal("expected a failure")
	}
	if !strings.Contains(err.Error(), "env/bin/python") || !strings.Contains(err.Error(), "esbuild") {
		t.Fatalf("the error should name both ways a bench can build: %v", err)
	}
}

// TestYarnInstallDisablesTheCorepackWalk: yarn 1's corepack probe walks from the
// install directory to the filesystem root, so a stray "packageManager" manifest in
// any ancestor — $HOME above ~/.fpm/build-cache, in the report that prompted this —
// failed every asset build under it. The install must carry the variable that turns
// the probe off, and must still force devDependencies on.
func TestYarnInstallDisablesTheCorepackWalk(t *testing.T) {
	dir := t.TempDir()
	cmd := yarnInstall(dir)

	if cmd.Dir != dir {
		t.Errorf("install runs in %q, want %q", cmd.Dir, dir)
	}
	args := strings.Join(cmd.Args, " ")
	if !strings.Contains(args, "--production=false") {
		t.Errorf("devDependencies are not forced on: %s", args)
	}
	var got string
	for _, kv := range cmd.Env {
		if v, ok := strings.CutPrefix(kv, "SKIP_YARN_COREPACK_CHECK="); ok {
			got = v
		}
	}
	if got != "1" {
		t.Errorf("SKIP_YARN_COREPACK_CHECK=%q, want \"1\"; without it an unrelated "+
			"package.json in any parent directory fails the build", got)
	}
}
