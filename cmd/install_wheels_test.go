package cmd

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fpm/internal/metadata"
	"fpm/internal/wheels"
)

// TestBuildPipInstallArgsOnline is the pre-existing behaviour: a package with no
// vendored wheels resolves its dependencies from the network.
func TestBuildPipInstallArgsOnline(t *testing.T) {
	args := buildPipInstallArgs("./apps/my_app", "")
	got := strings.Join(args, " ")

	if got != "install -q -e ./apps/my_app" {
		t.Fatalf("unexpected online pip args: %q", got)
	}
	if strings.Contains(got, "--no-index") {
		t.Fatalf("a package without wheels must not be installed offline: %q", got)
	}
}

// TestBuildPipInstallArgsOffline is the point of vendoring: pip must be pinned to the
// bundled wheels so the install never reaches PyPI.
func TestBuildPipInstallArgsOffline(t *testing.T) {
	args := buildPipInstallArgs("./apps/my_app", "/store/acme/my_app/1.0.0/wheels")
	got := strings.Join(args, " ")

	for _, want := range []string{"--no-index", "--find-links /store/acme/my_app/1.0.0/wheels", "-e ./apps/my_app"} {
		if !strings.Contains(got, want) {
			t.Fatalf("offline pip args missing %q: %q", want, got)
		}
	}
}

func TestVendoredWheelsDir(t *testing.T) {
	base := t.TempDir()

	if got := vendoredWheelsDir(base); got != "" {
		t.Fatalf("expected no wheels dir, got %q", got)
	}

	wheelsPath := filepath.Join(base, wheels.DirName)
	if err := os.MkdirAll(wheelsPath, 0o755); err != nil {
		t.Fatalf("failed to create wheels dir: %v", err)
	}
	if got := vendoredWheelsDir(base); got != wheelsPath {
		t.Fatalf("expected %q, got %q", wheelsPath, got)
	}

	// A file named "wheels" is not a vendored wheel directory.
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, wheels.DirName), []byte("x"), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	if got := vendoredWheelsDir(other); got != "" {
		t.Fatalf("a regular file named %q must not be treated as a wheels dir, got %q", wheels.DirName, got)
	}
}

// TestWarnOnWheelPlatformMismatch exercises the diagnostic paths. It asserts the
// function's classification rather than its output, so it stays valid on any host.
func TestWarnOnWheelPlatformMismatch(t *testing.T) {
	// These must never warn regardless of host.
	for _, meta := range []*metadata.AppMetadata{
		nil,
		{},                      // no wheels vendored
		{WheelPlatform: "host"}, // built on the installing machine's own platform
	} {
		stderr := captureStderr(t, func() { warnOnWheelPlatformMismatch(meta) })
		if stderr != "" {
			t.Fatalf("expected no warning for %+v, got: %s", meta, stderr)
		}
	}

	// A plainly foreign platform must warn on any host we build for.
	stderr := captureStderr(t, func() {
		warnOnWheelPlatformMismatch(&metadata.AppMetadata{WheelPlatform: "win_amd64_nonsense_tag"})
	})
	if !strings.Contains(stderr, "Warning") {
		t.Fatalf("expected a mismatch warning, got: %q", stderr)
	}
}

func TestGoArchToWheelArch(t *testing.T) {
	cases := map[string]string{
		"amd64":   "x86_64",
		"arm64":   "aarch64",
		"riscv64": "riscv64", // unmapped architectures pass through
	}
	for in, want := range cases {
		if got := goArchToWheelArch(in); got != want {
			t.Fatalf("goArchToWheelArch(%q) = %q, want %q", in, got, want)
		}
	}
}

// captureStderr collects what fn writes to os.Stderr. The writer is closed before
// reading so a call that writes nothing returns "" rather than blocking.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()

	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	os.Stderr = w

	fn()

	w.Close()
	os.Stderr = orig

	out, readErr := io.ReadAll(r)
	r.Close()
	if readErr != nil {
		t.Fatalf("failed to read captured stderr: %v", readErr)
	}
	return string(out)
}
