package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestVersionStringDefaults covers a build made with plain `go build`, which carries no
// stamping. Reporting "dev" is the honest answer; a hardcoded release number would not be.
func TestVersionStringDefaults(t *testing.T) {
	origVersion, origCommit := version, commit
	defer func() { version, commit = origVersion, origCommit }()

	version, commit = "dev", "unknown"
	if got := VersionString(); got != "dev" {
		t.Fatalf("an unstamped build should report %q, got %q", "dev", got)
	}

	version, commit = "dev", ""
	if got := VersionString(); got != "dev" {
		t.Fatalf("an empty commit should be omitted, got %q", got)
	}
}

func TestVersionStringStamped(t *testing.T) {
	origVersion, origCommit := version, commit
	defer func() { version, commit = origVersion, origCommit }()

	version, commit = "v1.7.0", "abc1234"
	got := VersionString()
	for _, want := range []string{"v1.7.0", "abc1234"} {
		if !strings.Contains(got, want) {
			t.Fatalf("version string %q should contain %q", got, want)
		}
	}
}

// TestVersionFlagIsStampedByBuild is the end-to-end check that the ldflags path in the
// Makefile actually reaches the binary: a version users cannot read is no version at all.
func TestVersionFlagIsStampedByBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary; skipped in short mode")
	}

	binPath := filepath.Join(t.TempDir(), "fpm-version-test")
	build := exec.Command("go", "build",
		"-ldflags", "-X 'fpm/cmd.version=v9.9.9' -X 'fpm/cmd.commit=deadbee'",
		"-o", binPath, "./fpm")
	build.Dir = mustModuleDir(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("failed to build test binary: %v\n%s", err, out)
	}

	out, err := exec.Command(binPath, "--version").CombinedOutput()
	require.NoError(t, err, "running --version failed: %s", out)

	got := string(out)
	require.Contains(t, got, "v9.9.9", "stamped version should be reported")
	require.Contains(t, got, "deadbee", "stamped commit should be reported")
}

// mustModuleDir returns the cmd directory, which holds the fpm main package.
func mustModuleDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	return wd
}
