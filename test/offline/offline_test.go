//go:build integration

// Package offline runs the end-to-end offline installation scenario against a real,
// network-isolated bench on a remote host. It is a thin Go wrapper around run.sh so
// the scenario shows up in `make test-integration` output; the real work — building
// a bench image from the local frappe/bench checkouts, packaging the fixture apps
// online, installing them in a `podman pod --network none`, and asserting on pip,
// required_apps, assets.json equivalence and HTTP serving — lives in run.sh and
// remote.sh, where it can also be run by hand, phase by phase.
//
// It is skipped unless FPM_OFFLINE_SSH_HOST is set, since it needs SSH access to a
// podman host with network access for the online phases.
package offline

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestOfflineInstallScenario(t *testing.T) {
	host := os.Getenv("FPM_OFFLINE_SSH_HOST")
	if host == "" {
		t.Skip("set FPM_OFFLINE_SSH_HOST=user@host to run the offline integration scenario")
	}
	phases := []string{"all"}
	if p := os.Getenv("FPM_OFFLINE_PHASES"); p != "" {
		phases = filepath.SplitList(p)
	}
	for _, phase := range phases {
		cmd := exec.Command("bash", "run.sh", phase)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		cmd.Env = os.Environ()
		if err := cmd.Run(); err != nil {
			t.Fatalf("phase %s failed: %v", phase, err)
		}
	}
	result := filepath.Join(".work", "results", "RESULT.md")
	data, err := os.ReadFile(result)
	if err != nil {
		t.Fatalf("no result report at %s: %v", result, err)
	}
	t.Logf("%s", data)
}
