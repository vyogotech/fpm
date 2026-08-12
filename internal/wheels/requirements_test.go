package wheels

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write %s: %v", name, err)
	}
}

func TestCollectFromRequirementsTxt(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, RequirementsFileName, `
# a comment line
frappe>=15.0.0

requests==2.31.0  # trailing comment
`)

	req, err := Collect(dir)
	if err != nil {
		t.Fatalf("Collect failed: %v", err)
	}

	want := []string{"frappe>=15.0.0", "requests==2.31.0"}
	if len(req.Specs) != len(want) {
		t.Fatalf("expected %v, got %v", want, req.Specs)
	}
	for i, w := range want {
		if req.Specs[i] != w {
			t.Fatalf("spec %d: expected %q, got %q", i, w, req.Specs[i])
		}
	}
	if req.Describe() != RequirementsFileName {
		t.Fatalf("expected source %q, got %q", RequirementsFileName, req.Describe())
	}
}

// TestCollectFromPyProject covers modern Frappe apps, which declare dependencies under
// PEP 621 [project] metadata rather than in requirements.txt.
func TestCollectFromPyProject(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, PyProjectFileName, `
[build-system]
requires = ["flit_core >=3.4,<4"]
build-backend = "flit_core.buildapi"

[project]
name = "my_app"
version = "1.0.0"
dependencies = [
    "frappe>=15.0.0",
    "pandas~=2.1",
]

[project.optional-dependencies]
dev = ["pytest"]
`)

	req, err := Collect(dir)
	if err != nil {
		t.Fatalf("Collect failed: %v", err)
	}

	// Runtime deps, plus the build backend, which pip needs to perform the PEP 517
	// editable build of the app on the target machine.
	for _, want := range []string{"frappe>=15.0.0", "pandas~=2.1", "flit_core >=3.4,<4"} {
		found := false
		for _, s := range req.Specs {
			if s == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing %q in %v", want, req.Specs)
		}
	}

	// Optional extras are not installed by default, so bundling them would bloat the
	// package with dependencies the target never uses.
	for _, s := range req.Specs {
		if s == "pytest" {
			t.Fatalf("optional extras should not be bundled, found %q in %v", s, req.Specs)
		}
	}
}

// TestCollectMergesBothManifests covers an app mid-migration that carries both files.
func TestCollectMergesBothManifests(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, RequirementsFileName, "requests==2.31.0\nfrappe>=15.0.0\n")
	writeFile(t, dir, PyProjectFileName, `
[project]
name = "my_app"
dependencies = ["frappe>=15.0.0", "pandas~=2.1"]
`)

	req, err := Collect(dir)
	if err != nil {
		t.Fatalf("Collect failed: %v", err)
	}

	// frappe is declared in both and must appear once.
	count := 0
	for _, s := range req.Specs {
		if s == "frappe>=15.0.0" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("duplicate specifier should be collapsed, appeared %d times in %v", count, req.Specs)
	}
	if len(req.Specs) != 3 {
		t.Fatalf("expected 3 unique specs, got %v", req.Specs)
	}
	if !strings.Contains(req.Describe(), RequirementsFileName) ||
		!strings.Contains(req.Describe(), PyProjectFileName) {
		t.Fatalf("both manifests should be named as sources, got %q", req.Describe())
	}
}

func TestCollectNoManifests(t *testing.T) {
	req, err := Collect(t.TempDir())
	if err != nil {
		t.Fatalf("an app with no manifests should not error, got: %v", err)
	}
	if !req.Empty() {
		t.Fatalf("expected no specs, got %v", req.Specs)
	}
}

// TestCollectPreservesPipDirectives keeps includes and index options working, so an app
// that splits requirements across files still resolves.
func TestCollectPreservesPipDirectives(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, RequirementsFileName, "-r base.txt\n--index-url https://pypi.example.com/simple\nfrappe\n")

	req, err := Collect(dir)
	if err != nil {
		t.Fatalf("Collect failed: %v", err)
	}
	for _, want := range []string{"-r base.txt", "--index-url https://pypi.example.com/simple", "frappe"} {
		found := false
		for _, s := range req.Specs {
			if s == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing %q in %v", want, req.Specs)
		}
	}
}

func TestCollectMalformedPyProject(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, PyProjectFileName, "[project\nname = broken")

	if _, err := Collect(dir); err == nil {
		t.Fatal("a malformed pyproject.toml should be reported, not silently ignored")
	}
}

func TestWriteMergedRequirements(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "merged.txt")
	req := Requirements{
		Specs:   []string{"requests==2.31.0", "frappe>=15.0.0"},
		Sources: []string{RequirementsFileName, PyProjectFileName},
	}

	if err := writeMergedRequirements(req, path); err != nil {
		t.Fatalf("writeMergedRequirements failed: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read merged file: %v", err)
	}
	content := string(data)

	for _, want := range req.Specs {
		if !strings.Contains(content, want) {
			t.Fatalf("merged file missing %q:\n%s", want, content)
		}
	}
	// Sorted output keeps the generated file stable across runs.
	if strings.Index(content, "frappe>=15.0.0") > strings.Index(content, "requests==2.31.0") {
		t.Fatalf("specs should be written sorted:\n%s", content)
	}
}
