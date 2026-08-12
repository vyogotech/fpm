package wheels

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDefaultBundleForPackageType documents the defaulting rule: a production package is
// a deployment artifact and bundles its dependencies, while a development package is for
// local iteration where bundling only slows the loop.
func TestDefaultBundleForPackageType(t *testing.T) {
	cases := map[string]bool{
		"prod": true,
		"dev":  false,
		"":     false,
	}
	for packageType, want := range cases {
		if got := DefaultBundleForPackageType(packageType); got != want {
			t.Fatalf("DefaultBundleForPackageType(%q) = %v, want %v", packageType, got, want)
		}
	}
}

func TestPlatformForPackageType(t *testing.T) {
	if got := PlatformForPackageType("prod"); got != DefaultProdPlatform {
		t.Fatalf("prod packages should cross-build for %s, got %q", DefaultProdPlatform, got)
	}
	// An empty tag means "build for the packaging host".
	if got := PlatformForPackageType("dev"); got != "" {
		t.Fatalf("dev packages should build for the host (empty tag), got %q", got)
	}
	if got := PlatformForPackageType(""); got != "" {
		t.Fatalf("unset package type should build for the host (empty tag), got %q", got)
	}
}

// TestBuildCommandHost checks the host path uses `pip wheel`, which can build wheels
// from source when a dependency publishes none.
func TestBuildCommandHost(t *testing.T) {
	cmd := BuildCommand("python3", "/src/requirements.txt", "/stage/wheels", "")
	got := cmd.String()

	for _, want := range []string{"-m pip wheel", "-r /src/requirements.txt", "-w /stage/wheels"} {
		if !strings.Contains(got, want) {
			t.Fatalf("host command missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "--platform") {
		t.Fatalf("host command should not pin a platform: %s", got)
	}
}

// TestBuildCommandCrossPlatform checks the cross-build path uses `pip download` with
// --only-binary, since building from source for a foreign platform is not possible.
func TestBuildCommandCrossPlatform(t *testing.T) {
	cmd := BuildCommand("python3", "/src/requirements.txt", "/stage/wheels", DefaultProdPlatform)
	got := cmd.String()

	for _, want := range []string{
		"-m pip download",
		"-r /src/requirements.txt",
		"-d /stage/wheels",
		"--platform " + DefaultProdPlatform,
		"--only-binary=:all:",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("cross-build command missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "pip wheel") {
		t.Fatalf("cross-build must not use `pip wheel`, which builds for the host: %s", got)
	}
}

// TestBundleSkipsWhenNoManifests covers an app with no Python dependencies: that is
// not an error, and no wheels directory should be left behind.
func TestBundleSkipsWhenNoManifests(t *testing.T) {
	dir := t.TempDir()
	destDir := filepath.Join(dir, DirName)

	vendored, err := Bundle(dir, destDir, "")
	if err != nil {
		t.Fatalf("a missing requirements.txt should not be an error, got: %v", err)
	}
	if vendored {
		t.Fatal("nothing should have been vendored")
	}
	if _, err := os.Stat(destDir); !os.IsNotExist(err) {
		t.Fatal("no wheels directory should be created when there is nothing to vendor")
	}
}

// TestBundleSkipsWhenManifestsEmpty covers present-but-empty manifests.
func TestBundleSkipsWhenManifestsEmpty(t *testing.T) {
	dir := t.TempDir()
	reqPath := filepath.Join(dir, "requirements.txt")
	if err := os.WriteFile(reqPath, nil, 0o644); err != nil {
		t.Fatalf("failed to write requirements: %v", err)
	}
	destDir := filepath.Join(dir, DirName)

	vendored, err := Bundle(dir, destDir, "")
	if err != nil {
		t.Fatalf("an empty requirements.txt should not be an error, got: %v", err)
	}
	if vendored {
		t.Fatal("nothing should have been vendored from an empty manifest")
	}
	if _, err := os.Stat(destDir); !os.IsNotExist(err) {
		t.Fatal("no wheels directory should be created when there is nothing to vendor")
	}
}

func TestCountWheels(t *testing.T) {
	dir := t.TempDir()
	files := []string{
		"frappe-15.0.0-py3-none-any.whl",
		"charset_normalizer-3.3.0-cp311-manylinux2014_x86_64.whl",
		"legacy-dep-1.0.tar.gz", // sdists count as vendored distributions
		"notes.txt",             // ignored
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatalf("failed to write %s: %v", f, err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}

	got, err := countWheels(dir)
	if err != nil {
		t.Fatalf("countWheels failed: %v", err)
	}
	if got != 3 {
		t.Fatalf("expected 3 distributions (2 wheels + 1 sdist), got %d", got)
	}
}
