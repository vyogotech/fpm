package mirror

import (
	"encoding/json"
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

// TestWorkspaceIsBenchShaped: app frontends reach the bench by relative path, and
// frappe-ui's vite plugin derives an app's name from the apps/<app> path segment. A
// flat src/<slug> layout satisfies neither, and each app then fails differently —
// builder and gameplan on "indexHtmlPath is required", helpdesk on `cd ../../frappe/ui`.
func TestWorkspaceIsBenchShaped(t *testing.T) {
	root := t.TempDir()
	ws, err := NewWorkspace(root, false)
	if err != nil {
		t.Fatal(err)
	}

	apps := ws.srcRoot()
	if filepath.Base(apps) != "apps" || filepath.Base(filepath.Dir(apps)) != "bench" {
		t.Errorf("checkouts live in %s, want <cache>/bench/apps", apps)
	}

	// The relative path an app frontend actually walks: <apps>/<slug>/frontend/src
	// four levels up, then sites/common_site_config.json.
	config := filepath.Join(apps, "crm", "frontend", "src", "..", "..", "..", "..", "sites", "common_site_config.json")
	data, err := os.ReadFile(filepath.Clean(config))
	if err != nil {
		t.Fatalf("an app frontend could not reach the bench config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("the bench config is not valid JSON: %v", err)
	}
	if cfg["socketio_port"] == nil {
		t.Error("the bench config must carry socketio_port; app frontends import it by name")
	}
}

// TestWorkspaceKeepsAnExistingBenchConfig: pointing --cache-dir at a real bench must
// not overwrite its settings with defaults.
func TestWorkspaceKeepsAnExistingBenchConfig(t *testing.T) {
	root := t.TempDir()
	sites := filepath.Join(root, "bench", "sites")
	if err := os.MkdirAll(sites, 0o755); err != nil {
		t.Fatal(err)
	}
	real := `{"socketio_port": 9123}`
	if err := os.WriteFile(filepath.Join(sites, "common_site_config.json"), []byte(real), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := NewWorkspace(root, false); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(sites, "common_site_config.json"))
	if string(got) != real {
		t.Errorf("an existing bench config was overwritten: %s", got)
	}
}
