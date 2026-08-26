package apputils

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ErrNotFrappeApp is the sentinel every "this source tree is not a Frappe app"
// failure wraps. Callers distinguish it from build, network and repository
// failures with errors.Is, and the CLI maps it to its own exit code, so an
// orchestrator that feeds arbitrary git checkouts into `fpm package` can tell
// "reject this input" apart from "retry this build".
var ErrNotFrappeApp = errors.New("not a Frappe app")

// NotFrappeAppError reports why a source tree failed Frappe app validation.
// It unwraps to ErrNotFrappeApp.
type NotFrappeAppError struct {
	// SourceDir is the directory that was validated.
	SourceDir string
	// AppName is the app module that was expected, when one was known.
	AppName string
	// Reason is the specific check that failed.
	Reason string
}

func (e *NotFrappeAppError) Error() string {
	return "Frappe app validation failed: " + e.Reason
}

// Is lets errors.Is(err, ErrNotFrappeApp) match every NotFrappeAppError.
func (e *NotFrappeAppError) Is(target error) bool {
	return target == ErrNotFrappeApp
}

// requiredAppFiles are the files every Frappe app module carries. Their presence is
// what makes a Python package a Frappe app rather than any other package.
var requiredAppFiles = []string{"__init__.py", "hooks.py", "modules.txt"}

// ValidateFrappeApp checks that sourceDir/appName is a Frappe app module: a
// directory holding __init__.py, hooks.py and modules.txt. It returns a
// *NotFrappeAppError (wrapping ErrNotFrappeApp) on any failure, and never touches
// anything outside that directory, so it is cheap enough to run before any other
// packaging work.
func ValidateFrappeApp(sourceDir string, appName string) error {
	fail := func(reason string) error {
		return &NotFrappeAppError{SourceDir: sourceDir, AppName: appName, Reason: reason}
	}
	if appName == "" {
		return fail(fmt.Sprintf("no Frappe app module found under '%s'", sourceDir))
	}

	innerAppPath := filepath.Join(sourceDir, appName)
	info, err := os.Stat(innerAppPath)
	if os.IsNotExist(err) {
		return fail(fmt.Sprintf("app directory '%s' not found", innerAppPath))
	}
	if err != nil {
		return fail(fmt.Sprintf("error checking app directory '%s': %v", innerAppPath, err))
	}
	if !info.IsDir() {
		return fail(fmt.Sprintf("'%s' is not a directory", innerAppPath))
	}

	for _, name := range requiredAppFiles {
		p := filepath.Join(innerAppPath, name)
		info, err := os.Stat(p)
		if os.IsNotExist(err) {
			return fail(fmt.Sprintf("file '%s' not found", p))
		}
		if err != nil {
			return fail(fmt.Sprintf("error checking file '%s': %v", p, err))
		}
		if info.IsDir() {
			return fail(fmt.Sprintf("'%s' is a directory, not a file", p))
		}
	}
	return nil
}

// DetectAppModule finds the Frappe app module inside a source checkout without
// consulting any metadata, so validation can run before anything else.
//
// A Frappe app repository is laid out as <repo>/<app_name>/hooks.py. The module is
// found by scanning the immediate subdirectories of sourceDir for one that passes
// ValidateFrappeApp. When hint is non-empty (from --app-name, an existing
// app_metadata.json, or the checkout's directory name) and names a valid module,
// it wins; otherwise the scan must find exactly one candidate. Zero candidates
// means the tree is not a Frappe app; more than one is ambiguous and the caller
// must say which with --app-name. Both are reported as *NotFrappeAppError.
func DetectAppModule(sourceDir string, hint string) (string, error) {
	if hint != "" {
		if err := ValidateFrappeApp(sourceDir, hint); err == nil {
			return hint, nil
		}
	}

	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return "", &NotFrappeAppError{SourceDir: sourceDir, AppName: hint,
			Reason: fmt.Sprintf("cannot read source directory '%s': %v", sourceDir, err)}
	}

	var candidates []string
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if ValidateFrappeApp(sourceDir, entry.Name()) == nil {
			candidates = append(candidates, entry.Name())
		}
	}
	sort.Strings(candidates)

	switch len(candidates) {
	case 1:
		return candidates[0], nil
	case 0:
		if hint != "" {
			// Report the specific failure for the hinted module: it is more useful
			// than "nothing found" when the caller named the module explicitly.
			return "", ValidateFrappeApp(sourceDir, hint)
		}
		return "", &NotFrappeAppError{SourceDir: sourceDir,
			Reason: fmt.Sprintf("no directory under '%s' contains %s (a Frappe app module)",
				sourceDir, strings.Join(requiredAppFiles, ", "))}
	default:
		return "", &NotFrappeAppError{SourceDir: sourceDir, AppName: hint,
			Reason: fmt.Sprintf("multiple Frappe app modules found under '%s' (%s); pass --app-name to choose one",
				sourceDir, strings.Join(candidates, ", "))}
	}
}
