package cmd

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
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

// TestCheckWheelTarget exercises the pre-install compatibility check. Since pip has
// no network fallback once it runs offline, a mismatch is a hard error unless the
// caller explicitly opts out.
func TestCheckWheelTarget(t *testing.T) {
	// These must never fail regardless of host.
	for _, meta := range []*metadata.AppMetadata{
		nil,
		{},                      // no wheels vendored
		{WheelPlatform: "host"}, // built on the installing machine's own platform; pip decides
	} {
		if err := checkWheelTarget(meta, "3.11", false); err != nil {
			t.Fatalf("expected no error for %+v, got: %v", meta, err)
		}
	}

	// A plainly foreign platform must fail on any host we build for, and be
	// classified so the CLI exits with the platform-mismatch code.
	foreign := &metadata.AppMetadata{WheelPlatform: "win_amd64_nonsense_tag"}
	err := checkWheelTarget(foreign, "", false)
	if err == nil || !errors.Is(err, ErrPlatformMismatch) {
		t.Fatalf("expected ErrPlatformMismatch, got: %v", err)
	}
	if ExitCodeFor(err) != ExitPlatformMismatch {
		t.Fatalf("exit code = %d, want %d", ExitCodeFor(err), ExitPlatformMismatch)
	}

	// --ignore-platform-mismatch downgrades it to a warning.
	stderr := captureStderr(t, func() {
		if err := checkWheelTarget(foreign, "", true); err != nil {
			t.Errorf("ignore flag should suppress the error, got %v", err)
		}
	})
	if !strings.Contains(stderr, "Warning") {
		t.Fatalf("expected a warning when ignoring, got: %q", stderr)
	}

	// A matching platform with a different interpreter version is a mismatch too.
	hostTag := "manylinux2014_" + goArchToWheelArch(runtime.GOARCH)
	if runtime.GOOS == "darwin" {
		hostTag = "macosx_11_0_" + runtime.GOARCH
	}
	matching := &metadata.AppMetadata{WheelPlatform: hostTag, WheelPythonVersion: "3.11"}
	if err := checkWheelTarget(matching, "3.11", false); err != nil {
		t.Fatalf("matching platform and python must pass, got: %v", err)
	}
	if err := checkWheelTarget(matching, "", false); err != nil {
		t.Fatalf("unknown bench python skips the version check, got: %v", err)
	}
	err = checkWheelTarget(matching, "3.12", false)
	if err == nil || !strings.Contains(err.Error(), "Python 3.11") {
		t.Fatalf("expected python version mismatch, got: %v", err)
	}
}

func TestWheelPlatformMatchesHost(t *testing.T) {
	cases := []struct {
		tag, goos, goarch string
		want              bool
	}{
		{"manylinux2014_x86_64", "linux", "amd64", true},
		{"manylinux_2_28_aarch64", "linux", "arm64", true},
		{"manylinux2014_x86_64", "linux", "arm64", false},
		{"manylinux2014_x86_64", "darwin", "amd64", false},
		{"macosx_11_0_arm64", "darwin", "arm64", true},
		// Any tag in a multi-platform list may match.
		{"manylinux2014_x86_64,manylinux_2_28_aarch64", "linux", "arm64", true},
		{"win_amd64", "linux", "amd64", false},
	}
	for _, tc := range cases {
		if got := wheelPlatformMatchesHost(tc.tag, tc.goos, tc.goarch); got != tc.want {
			t.Fatalf("wheelPlatformMatchesHost(%q, %s/%s) = %v, want %v", tc.tag, tc.goos, tc.goarch, got, tc.want)
		}
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
