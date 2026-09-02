package cmd

import (
	"errors"
	"fmt"
	"os"

	"fpm/internal/apputils"
	"fpm/internal/benchbuild"
	"fpm/internal/resolver"

	"github.com/common-nighthawk/go-figure"
	"github.com/spf13/cobra"
)

// version and commit are stamped at build time by the Makefile, so a released binary can
// identify itself. A build made with plain `go build` reports "dev".
var (
	version = "dev"
	commit  = "unknown"
)

// Exit codes. Orchestration tooling that drives fpm needs to tell "reject this
// input" from "retry this build" from "provide a dependency first" without parsing
// messages, so each class of failure fpm can classify gets its own code.
const (
	// ExitFailure is any error not classified below.
	ExitFailure = 1
	// ExitNotFrappeApp: the source tree is not a Frappe app (fpm package).
	ExitNotFrappeApp = 3
	// ExitAssetBuildFailed: the bench asset build failed (fpm package --bench-path).
	ExitAssetBuildFailed = 4
	// ExitUnresolvedRequiredApps: a required_apps entry could not be pinned (fpm package).
	ExitUnresolvedRequiredApps = 5
	// ExitMissingRequiredApps: a required app is not in the local store (fpm install, fpm deps --check).
	ExitMissingRequiredApps = 6
	// ExitPlatformMismatch: the package's vendored wheels do not match the bench (fpm install).
	ExitPlatformMismatch = 7
	// ExitRolledBack: install failed mid-flight and was safely rolled back (fpm install).
	ExitRolledBack = 8
	// ExitVersionConflict: a required app version conflicts with an app already present in the bench (fpm install).
	ExitVersionConflict = 9
	// ExitNotFound: the queried package does not exist (fpm exists).
	ExitNotFound = 10
	// ExitSiteHalfInstalled: the app reached the site but its DocTypes did not, and
	// fpm could not repair it (fpm install --site). The bench is intact and the app
	// is registered, so this is recoverable with 'bench --site <site> migrate' —
	// which is why it is not just a generic failure.
	ExitSiteHalfInstalled = 11
)

// ErrPlatformMismatch wraps install-time wheel/interpreter incompatibilities.
var ErrPlatformMismatch = errors.New("vendored wheels do not match this bench")

// ErrRolledBack wraps install failures that were cleanly rolled back.
var ErrRolledBack = errors.New("installation failed and changes were rolled back")

// ErrVersionConflict wraps version conflicts with apps already present in the bench.
var ErrVersionConflict = errors.New("version conflict with app in bench")

// ErrSiteHalfInstalled wraps a site install that registered the app but left its
// DocTypes unsynced.
var ErrSiteHalfInstalled = errors.New("site install left the app without its DocTypes")

// errNotFound wraps "does not exist" answers from fpm exists.
var errNotFound = errors.New("package not found")

// ExitCodeFor maps an error to the process exit code.
func ExitCodeFor(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, apputils.ErrNotFrappeApp):
		return ExitNotFrappeApp
	case errors.Is(err, benchbuild.ErrBuildFailed):
		return ExitAssetBuildFailed
	case errors.Is(err, resolver.ErrUnresolved):
		return ExitUnresolvedRequiredApps
	case errors.Is(err, resolver.ErrMissing):
		return ExitMissingRequiredApps
	case errors.Is(err, ErrPlatformMismatch):
		return ExitPlatformMismatch
	case errors.Is(err, ErrVersionConflict):
		return ExitVersionConflict
	case errors.Is(err, ErrRolledBack):
		return ExitRolledBack
	case errors.Is(err, ErrSiteHalfInstalled):
		return ExitSiteHalfInstalled
	case errors.Is(err, errNotFound):
		return ExitNotFound
	default:
		return ExitFailure
	}
}

// VersionString renders the build identity reported by `fpm --version`.
func VersionString() string {
	if commit == "unknown" || commit == "" {
		return version
	}
	return fmt.Sprintf("%s (commit %s)", version, commit)
}

var rootCmd = &cobra.Command{
	Use:     "fpm",
	Short:   "Vyogo FPM - Frappe Package Manager CLI",
	Version: VersionString(),
	// A failed package or install is reported once, as the error itself. Dumping the
	// flag reference after a build or resolution failure buries the message that
	// matters; usage is still shown for flag and argument errors.
	SilenceUsage:  true,
	SilenceErrors: true,
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(ExitCodeFor(err))
	}
}

func init() {
	// Flag and argument mistakes still deserve the usage text.
	rootCmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		cmd.SilenceUsage = false
		return err
	})
}

func init() {
	myFigure := figure.NewFigure("Vyogo FPM", "", true)
	rootCmd.SetVersionTemplate("fpm {{.Version}}\n")
	rootCmd.Long = fmt.Sprintf("\n%s\n\nFPM (Frappe Package Manager) is a command-line interface to manage Frappe applications,\nproviding package creation, installation, and repository management\nto streamline Frappe app deployment.", myFigure.String())
}
