package mirror

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWorkspaceCheckoutReusesCloneAndCleans(t *testing.T) {
	repo := fixtureRepo(t, "v1.0.0")

	ws, err := NewWorkspace(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}

	dir, err := ws.Checkout("wiki", repo, "v1.0.0", false)
	if err != nil {
		t.Fatal(err)
	}

	// Leftovers from a previous build must not survive into the next one.
	junk := filepath.Join(dir, "node_modules", "left-over.js")
	if err := os.MkdirAll(filepath.Dir(junk), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(junk, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Add a new tag upstream; the second checkout must fetch it into the
	// existing clone rather than recloning.
	tag := exec.Command("git", "tag", "v1.1.0")
	tag.Dir = repo
	if out, err := tag.CombinedOutput(); err != nil {
		t.Fatalf("git tag: %v\n%s", err, out)
	}

	dir2, err := ws.Checkout("wiki", repo, "v1.1.0", false)
	if err != nil {
		t.Fatal(err)
	}
	if dir2 != dir {
		t.Errorf("checkout moved: %s vs %s", dir2, dir)
	}
	if _, err := os.Stat(junk); !os.IsNotExist(err) {
		t.Error("clean -fdx did not remove build leftovers")
	}
}

func TestWorkspaceCheckoutBranch(t *testing.T) {
	repo := fixtureRepo(t)

	ws, err := NewWorkspace(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Checkout("drive", repo, "main", true); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Checkout("drive", repo, "no-such-branch", true); err == nil {
		t.Error("missing branch must error")
	}
}

func TestWorkspaceBuildEnvPointsAtCaches(t *testing.T) {
	root := t.TempDir()
	ws, err := NewWorkspace(root, false)
	if err != nil {
		t.Fatal(err)
	}
	env := ws.BuildEnv()
	want := []string{
		"PIP_CACHE_DIR=" + filepath.Join(root, "pip"),
		"npm_config_cache=" + filepath.Join(root, "npm"),
		"YARN_CACHE_FOLDER=" + filepath.Join(root, "npm", "yarn"),
	}
	for _, entry := range want {
		found := false
		for _, got := range env {
			if got == entry {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("BuildEnv missing %s", entry)
		}
	}
}
