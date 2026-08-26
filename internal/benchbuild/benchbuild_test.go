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
