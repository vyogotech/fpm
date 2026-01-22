package cmd

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"fpm/internal/metadata"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
)

// SharedExecuteCommand is a helper to execute Cobra commands and capture their output.
// It redirects os.Stdout and os.Stderr to capture output from fmt.Printf etc.
func SharedExecuteCommand(root *cobra.Command, args ...string) (string, error) {
	buf := new(bytes.Buffer)

	// Backup original stdout/stderr
	oldStdout := os.Stdout
	oldStderr := os.Stderr

	r, w, _ := os.Pipe()
	os.Stdout = w
	os.Stderr = w

	root.SetOut(w)
	root.SetErr(w)
	root.SetArgs(args)

	err := root.Execute()

	// Restore original stdout/stderr
	w.Close()
	os.Stdout = oldStdout
	os.Stderr = oldStderr

	io.Copy(buf, r)
	output := buf.String()
	return output, err
}

// SharedRunFPMCommand runs the main FPM binary (via go run) with given arguments.
func SharedRunFPMCommand(t *testing.T, verbose bool, args ...string) ([]byte, error) {
	_, filename, _, _ := runtime.Caller(0)
	mainGoPath := filepath.Join(filepath.Dir(filename), "fpm", "main.go")

	cmdArgs := append([]string{"run", mainGoPath}, args...)
	cmd := exec.Command("go", cmdArgs...)

	var output []byte
	var cmdErr error

	if verbose {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmdErr = cmd.Run()
	} else {
		output, cmdErr = cmd.CombinedOutput()
	}

	if cmdErr != nil {
		errorMsg := fmt.Sprintf("error running 'go run %s %s': %v", mainGoPath, strings.Join(args, " "), cmdErr)
		if !verbose && len(output) > 0 {
			errorMsg += fmt.Sprintf("\nCommand output:\n%s", string(output))
		}
		return output, fmt.Errorf(errorMsg)
	}
	return output, nil
}

// SharedResetRepoCmdFlags resets flags for repoAddCmd.
func SharedResetRepoCmdFlags() {
	repoAddCmd.Flags().VisitAll(func(f *pflag.Flag) {
		f.Value.Set(f.DefValue)
		f.Changed = false
	})
	repoAddPriority = 0
}

// SharedCreateMinimalAppForInstall creates a basic Frappe app structure for testing installation.
func SharedCreateMinimalAppForInstall(t *testing.T, basePath, appName, appVersion, appOrg string) {
	appModulePath := filepath.Join(basePath, appName)
	require.NoError(t, os.MkdirAll(appModulePath, 0o755))

	hooksContent := fmt.Sprintf("app_name = \"%s\"\napp_version = \"%s\"\napp_publisher = \"%s\"\n", appName, appVersion, appOrg)
	require.NoError(t, os.WriteFile(filepath.Join(appModulePath, "hooks.py"), []byte(hooksContent), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(appModulePath, "__init__.py"), []byte("# init"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(appModulePath, "modules.txt"), []byte("mymodule"), 0o644))
}

// SharedCreateMinimalAppForPackage creates a very basic app structure for testing packaging.
func SharedCreateMinimalAppForPackage(t *testing.T, baseDir string, appName string, files map[string]string) string {
	t.Helper()
	sourceDir := filepath.Join(baseDir, appName+"_source")
	appModuleDir := filepath.Join(sourceDir, appName)
	require.NoError(t, os.MkdirAll(appModuleDir, 0755))

	standardAppFiles := map[string]string{
		"__init__.py": "",
		"hooks.py":    fmt.Sprintf("app_name = \"%s\"", appName),
		"modules.txt": "",
	}
	for fname, content := range standardAppFiles {
		require.NoError(t, os.WriteFile(filepath.Join(appModuleDir, fname), []byte(content), 0644))
	}

	if files != nil {
		for relPath, content := range files {
			absPath := filepath.Join(sourceDir, relPath)
			absDir := filepath.Dir(absPath)
			require.NoError(t, os.MkdirAll(absDir, 0755))
			require.NoError(t, os.WriteFile(absPath, []byte(content), 0644))
		}
	}
	return sourceDir
}

// SharedReadMetadataFromFpm opens the FPM file and returns its metadata.
func SharedReadMetadataFromFpm(t *testing.T, fpmFilePath string) (*metadata.AppMetadata, error) {
	t.Helper()
	unzipDir := t.TempDir()

	r, err := zip.OpenReader(fpmFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open fpm package %s: %w", fpmFilePath, err)
	}
	defer r.Close()

	for _, f := range r.File {
		fpath := filepath.Join(unzipDir, f.Name)
		if !strings.HasPrefix(fpath, filepath.Clean(unzipDir)+string(os.PathSeparator)) {
			return nil, fmt.Errorf("illegal file path in zip: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			os.MkdirAll(fpath, os.ModePerm)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(fpath), os.ModePerm); err != nil {
			return nil, fmt.Errorf("failed to create directory for %s: %w", fpath, err)
		}
		outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return nil, fmt.Errorf("failed to open file for writing %s: %w", fpath, err)
		}
		rc, err := f.Open()
		if err != nil {
			outFile.Close()
			return nil, fmt.Errorf("failed to open file in zip %s: %w", f.Name, err)
		}
		_, err = io.Copy(outFile, rc)
		outFile.Close()
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to copy content of %s: %w", f.Name, err)
		}
	}
	return metadata.LoadAppMetadata(unzipDir)
}

// SharedCreateDummyFPMBytes creates a dummy .fpm package in memory and returns its bytes.
func SharedCreateDummyFPMBytes(t *testing.T, org, appName, version string) []byte {
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)

	// metadata
	meta := metadata.AppMetadata{
		Org:            org,
		AppName:        appName,
		PackageVersion: version,
		Description:    "Dummy app for testing",
	}
	metaBytes, _ := json.MarshalIndent(meta, "", "  ")
	f, _ := zw.Create("app_metadata.json")
	f.Write(metaBytes)

	// app module dir
	appModuleDir := fmt.Sprintf("%s/", appName)
	header := &zip.FileHeader{Name: appModuleDir}
	header.SetMode(0o755 | os.ModeDir)
	zw.CreateHeader(header)

	// dummy file
	f2, _ := zw.Create(filepath.Join(appName, "dummy.py"))
	f2.Write([]byte("# dummy"))

	zw.Close()
	return buf.Bytes()
}
