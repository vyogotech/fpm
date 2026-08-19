package archive

import (
	"os"
	"path/filepath"
	"testing"

	"fpm/internal/metadata"
)

// Apps check symlinks to install-time artifacts into their repos (frappe/wiki
// ships wiki/public/node_modules as a dangling link). Packaging must skip
// them, not die on the stat.
func TestCreateFPMArchiveSkipsDanglingSymlink(t *testing.T) {
	tmpDir := t.TempDir()
	appName := "my_test_app"
	appSourcePath := filepath.Join(tmpDir, "apps", appName)
	outputPath := filepath.Join(tmpDir, "output")

	createMockApp(t, filepath.Join(tmpDir, "apps"), appName, map[string]string{
		"app_metadata.json":    `{"package_name": "my_test_app", "package_version": "0.0.1"}`,
		"my_test_app/file1.py": "print('file1')",
	}, "")
	if err := os.MkdirAll(outputPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join(appSourcePath, "no-such-target"),
		filepath.Join(appSourcePath, appName, "node_modules"),
	); err != nil {
		t.Fatal(err)
	}

	meta, err := metadata.LoadAppMetadata(appSourcePath)
	if err != nil {
		t.Fatal(err)
	}
	meta.PackageVersion = "0.1.0"

	if err := CreateFPMArchive(appSourcePath, outputPath, meta, "0.1.0"); err != nil {
		t.Fatalf("CreateFPMArchive failed on a dangling symlink: %v", err)
	}
}
