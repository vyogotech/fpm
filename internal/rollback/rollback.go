// Package rollback implements a transaction journal for fpm install.
//
// Every bench-level mutation performed during installation (symlinking apps,
// deploying assets, pip installation, updating apps.txt) registers a corresponding
// UndoAction. If a mid-flight failure occurs, the journal executes compensating
// actions in reverse LIFO order.
//
// Crucially, the journal consults the pre-install Snapshot before executing any
// undo: apps or configurations that existed before the install session are never
// removed or uninstalled.
package rollback

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"fpm/internal/assets"
	"fpm/internal/snapshot"
)

// UndoAction is a single compensating action that can revert an installation step.
type UndoAction interface {
	// Undo executes the rollback action.
	Undo(out io.Writer, verbose bool) error
	// Describe returns a concise human-readable summary of the action.
	Describe() string
	// AppName returns the name of the app this action affects.
	AppName() string
}

// Journal records and executes undo actions in LIFO order.
type Journal struct {
	Snapshot *snapshot.Snapshot
	Actions  []UndoAction
}

// NewJournal creates a rollback journal bound to a pre-install snapshot.
func NewJournal(snap *snapshot.Snapshot) *Journal {
	return &Journal{
		Snapshot: snap,
		Actions:  make([]UndoAction, 0),
	}
}

// Record appends an undo action to the journal.
func (j *Journal) Record(action UndoAction) {
	if action != nil {
		j.Actions = append(j.Actions, action)
	}
}

// Rollback executes all recorded actions in reverse (LIFO) order.
func (j *Journal) Rollback(out io.Writer, verbose bool) error {
	if len(j.Actions) == 0 {
		return nil
	}

	if out == nil {
		out = io.Discard
	}

	fmt.Fprintf(out, "\n⚠️  Installation stopped with an error. Rolling back %d action(s)...\n", len(j.Actions))

	var errs []error
	// Reverse LIFO loop
	for i := len(j.Actions) - 1; i >= 0; i-- {
		action := j.Actions[i]
		appName := action.AppName()

		// Safety check: if app was pre-existing in the bench, we must not delete it
		if appName != "" && j.Snapshot.WasPresentInBench(appName) {
			// For apps.txt / assets, we restore to snapshot rather than delete
			if _, ok := action.(*AppsTxtAction); ok {
				fmt.Fprintf(out, "  [rollback] Restoring sites/apps.txt to pre-install state\n")
				if err := j.Snapshot.RestoreAppsTxt(); err != nil {
					fmt.Fprintf(out, "  [rollback error] Failed to restore apps.txt: %v\n", err)
					errs = append(errs, err)
				}
				continue
			}
			if _, ok := action.(*AssetDeployAction); ok {
				fmt.Fprintf(out, "  [rollback] Restoring assets.json / assets-rtl.json to pre-install state\n")
				if err := j.Snapshot.RestoreAssetManifests(); err != nil {
					fmt.Fprintf(out, "  [rollback error] Failed to restore asset manifests: %v\n", err)
					errs = append(errs, err)
				}
				continue
			}
			fmt.Fprintf(out, "  [rollback] Preserved pre-existing app: %s (%s)\n", appName, action.Describe())
			continue
		}

		fmt.Fprintf(out, "  [rollback] %s\n", action.Describe())
		if err := action.Undo(out, verbose); err != nil {
			fmt.Fprintf(out, "  [rollback error] %s: %v\n", action.Describe(), err)
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("rollback completed with %d error(s): %w", len(errs), errors.Join(errs...))
	}
	fmt.Fprintf(out, "✓ Rollback completed successfully. Bench restored to clean state.\n\n")
	return nil
}

// --- Specific Undo Actions ---

// SymlinkAction undoes a symlink in <bench>/apps/<appName>.
type SymlinkAction struct {
	BenchPath string
	App       string
	Snapshot  *snapshot.Snapshot
}

func (a *SymlinkAction) AppName() string { return a.App }
func (a *SymlinkAction) Describe() string {
	return fmt.Sprintf("Remove symlink apps/%s", a.App)
}
func (a *SymlinkAction) Undo(out io.Writer, verbose bool) error {
	linkPath := filepath.Join(a.BenchPath, "apps", a.App)
	if a.Snapshot != nil && a.Snapshot.WasPresentInBench(a.App) {
		prevTarget := a.Snapshot.BenchApps[a.App]
		if prevTarget != "" {
			_ = os.Remove(linkPath)
			return os.Symlink(prevTarget, linkPath)
		}
		return nil
	}
	if err := os.Remove(linkPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove symlink %s: %w", linkPath, err)
	}
	return nil
}

// AssetDeployAction undoes deployed assets for an app.
type AssetDeployAction struct {
	BenchPath string
	App       string
	Snapshot  *snapshot.Snapshot
}

func (a *AssetDeployAction) AppName() string { return a.App }
func (a *AssetDeployAction) Describe() string {
	return fmt.Sprintf("Undeploy assets for %s (sites/assets/%s and manifests)", a.App, a.App)
}
func (a *AssetDeployAction) Undo(out io.Writer, verbose bool) error {
	if a.Snapshot != nil && a.Snapshot.WasPresentInBench(a.App) {
		return a.Snapshot.RestoreAssetManifests()
	}
	return assets.Undeploy(a.BenchPath, a.App)
}

// PipInstallAction undoes `pip install -e ./apps/<appName>`.
type PipInstallAction struct {
	BenchPath string
	App       string
	Snapshot  *snapshot.Snapshot
}

func (a *PipInstallAction) AppName() string { return a.App }
func (a *PipInstallAction) Describe() string {
	return fmt.Sprintf("Uninstall pip package '%s' from virtualenv", a.App)
}
func (a *PipInstallAction) Undo(out io.Writer, verbose bool) error {
	if a.Snapshot != nil && a.Snapshot.WasPresentInBench(a.App) {
		// Pre-existing pip package; do not uninstall
		return nil
	}
	pipPath := filepath.Join(a.BenchPath, "env", "bin", "pip")
	if _, err := os.Stat(pipPath); err != nil {
		return nil
	}
	cmd := exec.Command(pipPath, "uninstall", "-y", "-q", a.App)
	cmd.Dir = a.BenchPath
	output, err := cmd.CombinedOutput()
	if err != nil && verbose {
		fmt.Fprintf(out, "    [pip uninstall notice] %s\n", string(output))
	}
	return nil
}

// AppsTxtAction restores sites/apps.txt to the snapshot state.
type AppsTxtAction struct {
	BenchPath string
	App       string
	Snapshot  *snapshot.Snapshot
}

func (a *AppsTxtAction) AppName() string { return a.App }
func (a *AppsTxtAction) Describe() string {
	return fmt.Sprintf("Revert sites/apps.txt (remove '%s')", a.App)
}
func (a *AppsTxtAction) Undo(out io.Writer, verbose bool) error {
	if a.Snapshot != nil {
		return a.Snapshot.RestoreAppsTxt()
	}
	return nil
}
