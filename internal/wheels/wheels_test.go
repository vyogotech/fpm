package wheels

import (
	"os"
	"path/filepath"
	"strconv"
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

func TestTargetValidate(t *testing.T) {
	if err := (Target{}).Validate(); err != nil {
		t.Fatalf("host target must validate: %v", err)
	}
	if err := (Target{PythonVersion: "3.11"}).Validate(); err == nil {
		t.Fatal("a python version without a platform is a mistake and must be rejected")
	}
	// The whole point: a cross-build must not guess the destination interpreter.
	err := (Target{Platforms: []string{DefaultProdPlatform}}).Validate()
	if err == nil || !strings.Contains(err.Error(), "--python-version") {
		t.Fatalf("cross-build without python version must fail mentioning --python-version, got: %v", err)
	}
	if err := (Target{Platforms: []string{DefaultProdPlatform}, PythonVersion: "311"}).Validate(); err == nil {
		t.Fatal("malformed python version must be rejected")
	}
	if err := (Target{Platforms: []string{DefaultProdPlatform}, PythonVersion: "3.11"}).Validate(); err != nil {
		t.Fatalf("complete cross target must validate: %v", err)
	}
}

func TestTargetTag(t *testing.T) {
	if got := (Target{}).Tag(); got != HostPlatformTag {
		t.Fatalf("host tag = %q", got)
	}
	multi := Target{Platforms: []string{"manylinux2014_x86_64", "manylinux_2_28_x86_64"}}
	if got := multi.Tag(); got != "manylinux2014_x86_64,manylinux_2_28_x86_64" {
		t.Fatalf("multi tag = %q", got)
	}
	if got := ParseTag(multi.Tag()); len(got) != 2 || got[1] != "manylinux_2_28_x86_64" {
		t.Fatalf("ParseTag round trip = %v", got)
	}
	if got := ParseTag(HostPlatformTag); got != nil {
		t.Fatalf("host tag parses to no platforms, got %v", got)
	}
}

// TestBuildCommandHost checks the host path uses `pip wheel`, which can build wheels
// from source when a dependency publishes none.
func TestBuildCommandHost(t *testing.T) {
	cmd := BuildCommand("python3", "/src/requirements.txt", "/stage/wheels", Target{})
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
// --only-binary and every interpreter constraint pinned explicitly, since building
// from source for a foreign platform is not possible and pip must not fill in the
// packaging host's interpreter.
func TestBuildCommandCrossPlatform(t *testing.T) {
	target := Target{
		Platforms:     []string{DefaultProdPlatform, "manylinux_2_28_x86_64"},
		PythonVersion: "3.11",
		ABIs:          []string{"cp311", "abi3"},
	}
	cmd := BuildCommand("python3", "/src/requirements.txt", "/stage/wheels", target)
	got := cmd.String()

	for _, want := range []string{
		"-m pip download",
		"-r /src/requirements.txt",
		"-d /stage/wheels",
		"--platform " + DefaultProdPlatform,
		"--platform manylinux_2_28_x86_64",
		"--python-version 3.11",
		"--implementation cp",
		"--abi cp311",
		"--abi abi3",
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

	res, err := Bundle(dir, destDir, Target{})
	if err != nil {
		t.Fatalf("a missing requirements.txt should not be an error, got: %v", err)
	}
	if res.Bundled {
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

	res, err := Bundle(dir, destDir, Target{})
	if err != nil {
		t.Fatalf("an empty requirements.txt should not be an error, got: %v", err)
	}
	if res.Bundled {
		t.Fatal("nothing should have been vendored from an empty manifest")
	}
	if _, err := os.Stat(destDir); !os.IsNotExist(err) {
		t.Fatal("no wheels directory should be created when there is nothing to vendor")
	}
}

// TestBundleRejectsIncompleteCrossTargetBeforeRunningPip: an invalid target fails
// even when there are dependencies, before pip is looked for.
func TestBundleRejectsIncompleteCrossTarget(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("requests\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Bundle(dir, filepath.Join(dir, DirName), Target{Platforms: []string{DefaultProdPlatform}})
	if err == nil || !strings.Contains(err.Error(), "--python-version") {
		t.Fatalf("expected python-version error, got %v", err)
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

func TestParseDistributionFilename(t *testing.T) {
	cases := map[string]Pin{
		"frappe-15.0.0-py3-none-any.whl":                                {Name: "frappe", Version: "15.0.0"},
		"charset_normalizer-3.3.0-cp311-cp311-manylinux2014_x86_64.whl": {Name: "charset-normalizer", Version: "3.3.0"},
		"Some.Pkg-1.2.3-1-py3-none-any.whl":                             {Name: "some-pkg", Version: "1.2.3"}, // build tag
		"python-dateutil-2.8.2.tar.gz":                                  {Name: "python-dateutil", Version: "2.8.2"},
		"legacy-1.0.zip":                                                {Name: "legacy", Version: "1.0"},
	}
	for file, want := range cases {
		got, ok := ParseDistributionFilename(file)
		if !ok {
			t.Fatalf("%s: expected to parse", file)
		}
		if got.Name != want.Name || got.Version != want.Version {
			t.Fatalf("%s: got %s==%s, want %s==%s", file, got.Name, got.Version, want.Name, want.Version)
		}
	}
	for _, bad := range []string{"notes.txt", "broken.whl", "noversion.tar.gz"} {
		if _, ok := ParseDistributionFilename(bad); ok {
			t.Fatalf("%s: should not parse", bad)
		}
	}
}

func TestLockRoundTrip(t *testing.T) {
	dir := t.TempDir()
	target := Target{Platforms: []string{DefaultProdPlatform}, PythonVersion: "3.11"}
	pins := PinsFromFiles([]string{
		"requests-2.32.0-py3-none-any.whl",
		"charset_normalizer-3.3.0-cp311-cp311-manylinux2014_x86_64.whl",
	})
	if pins[0].Name != "charset-normalizer" {
		t.Fatalf("pins must be sorted by name, got %v", pins)
	}
	path := filepath.Join(dir, LockFileName)
	if err := writeLock(path, target, Requirements{Sources: []string{"requirements.txt"}}, pins, nil); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	content := string(data)
	for _, want := range []string{"# target-platform: " + DefaultProdPlatform, "# target-python: 3.11", "charset-normalizer==3.3.0\n", "requests==2.32.0\n"} {
		if !strings.Contains(content, want) {
			t.Fatalf("lock missing %q:\n%s", want, content)
		}
	}
	back, err := ReadLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 2 || back[1].String() != "requests==2.32.0" {
		t.Fatalf("ReadLock = %v", back)
	}
	if none, err := ReadLock(filepath.Join(dir, "absent")); err != nil || none != nil {
		t.Fatalf("missing lock must be (nil, nil), got %v, %v", none, err)
	}
}

func TestUnsatisfiedRequirement(t *testing.T) {
	out := "Collecting Unidecode~=1.4.0\nERROR: Could not find a version that satisfies the requirement googlemaps~=4.10.0 (from versions: 4.2.0)\nERROR: No matching distribution found for googlemaps~=4.10.0\n"
	if got := unsatisfiedRequirement(out); got != "googlemaps~=4.10.0" {
		t.Fatalf("got %q", got)
	}
	if got := unsatisfiedRequirement("ERROR: HTTP error 503"); got != "" {
		t.Fatalf("unrelated failure must not be parsed as a requirement, got %q", got)
	}
}

func TestIsUniversalWheel(t *testing.T) {
	cases := map[string]bool{
		"googlemaps-4.10.0-py3-none-any.whl":                      true,
		"six-1.16.0-py2.py3-none-any.whl":                         true,
		"msgpack-1.2.1-cp314-cp314-manylinux2014_x86_64.whl":      false,
		"rapidfuzz-3.14.3-cp314-cp314-macosx_11_0_arm64.whl":      false,
		"cryptography-43.0.0-cp39-abi3-manylinux_2_28_x86_64.whl": false,
		"broken.whl": false,
	}
	for file, want := range cases {
		if got := IsUniversalWheel(file); got != want {
			t.Fatalf("IsUniversalWheel(%q) = %v, want %v", file, got, want)
		}
	}
}

// TestVerifyCommandCrossPlatform: the check re-resolves with the same target pins but
// nothing except the vendored directory to resolve from. That is what the bench does.
func TestVerifyCommandCrossPlatform(t *testing.T) {
	target := Target{Platforms: []string{"manylinux2014_aarch64"}, PythonVersion: "3.14"}
	cmd := VerifyCommand("python3", "/stage/wheels/fpm-requirements.txt", "/stage/wheels", "/tmp/verify", target)

	joined := cmd.String()
	for _, want := range []string{
		"-m pip download",
		"-r /stage/wheels/fpm-requirements.txt",
		"-d /tmp/verify",
		"--no-index",
		"--find-links /stage/wheels",
		"--only-binary=:all:",
		"--platform manylinux2014_aarch64",
		"--platform manylinux_2_28_aarch64",
		"--python-version 3.14",
		"--implementation cp",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("VerifyCommand missing %q:\n%s", want, joined)
		}
	}
}

// TestVerifyCommandHost keeps a host package's sdists usable: the bench's own pip can
// build them at install time, so the check must not force binaries only.
func TestVerifyCommandHost(t *testing.T) {
	cmd := VerifyCommand("python3", "/stage/wheels/fpm-requirements.txt", "/stage/wheels", "/tmp/verify", Target{})
	joined := cmd.String()
	if !strings.Contains(joined, "--no-index") || !strings.Contains(joined, "--find-links /stage/wheels") {
		t.Fatalf("host verification must still resolve offline only:\n%s", joined)
	}
	if strings.Contains(joined, "--only-binary") || strings.Contains(joined, "--platform") {
		t.Fatalf("host verification must not pin a cross-target:\n%s", joined)
	}
}

// TestVerifyOfflineClosureReportsTheMissingDistribution is issue #9's second half: a
// wheels directory missing a transitive dependency (regex, pulled in by nltk) must fail
// packaging with the name of what is missing, not install cleanly and break on a bench.
func TestVerifyOfflineClosureReportsTheMissingDistribution(t *testing.T) {
	python := fakePip(t, `ERROR: Could not find a version that satisfies the requirement regex>=2021.8.3 (from nltk)
ERROR: No matching distribution found for regex>=2021.8.3`, 1)

	err := verifyOfflineClosure(python, "/stage/wheels/fpm-requirements.txt", "/stage/wheels",
		Target{Platforms: []string{"manylinux2014_x86_64"}, PythonVersion: "3.11"})
	if err == nil {
		t.Fatal("an incomplete closure must fail packaging")
	}
	for _, want := range []string{"regex>=2021.8.3", "complete dependency closure", "--bundle-deps=false"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error should mention %q, got:\n%v", want, err)
		}
	}
}

// TestVerifyOfflineClosurePasses: a complete set is silent and does not fail the build.
func TestVerifyOfflineClosurePasses(t *testing.T) {
	python := fakePip(t, "Saved ./wheels/frappe-1.0.0-py3-none-any.whl", 0)
	if err := verifyOfflineClosure(python, "/req.txt", "/wheels", Target{}); err != nil {
		t.Fatalf("a complete closure must verify: %v", err)
	}
}

// fakePip writes an executable standing in for the python interpreter pip is driven
// through, so the verification logic is testable without pip or a network.
func fakePip(t *testing.T, output string, exitCode int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "python")
	script := "#!/bin/sh\ncat <<'OUT'\n" + output + "\nOUT\nexit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
