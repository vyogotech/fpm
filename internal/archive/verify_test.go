package archive

import (
	"os"
	"path/filepath"
	"testing"

	"fpm/internal/metadata"
	"fpm/internal/utils"
)

// buildTestApp writes a minimal Frappe app source tree and returns its path.
// It includes the standard root files that are staged after the app source, since
// those are precisely the files that must not escape the content checksum.
func buildTestApp(t *testing.T, appName string) string {
	t.Helper()

	srcDir := t.TempDir()
	moduleDir := filepath.Join(srcDir, appName)
	if err := os.MkdirAll(moduleDir, 0o755); err != nil {
		t.Fatalf("failed to create module dir: %v", err)
	}

	files := map[string]string{
		filepath.Join(moduleDir, "hooks.py"):      "app_name = \"" + appName + "\"\n",
		filepath.Join(moduleDir, "__init__.py"):   "__version__ = \"1.0.0\"\n",
		filepath.Join(moduleDir, "modules.txt"):   "Test Module\n",
		filepath.Join(srcDir, "requirements.txt"): "frappe\n",
		filepath.Join(srcDir, "install_hooks.py"): "print('install')\n",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("failed to write %s: %v", path, err)
		}
	}
	return srcDir
}

func packageTestApp(t *testing.T, appName string) string {
	t.Helper()

	srcDir := buildTestApp(t, appName)
	outDir := t.TempDir()
	meta := &metadata.AppMetadata{
		PackageName:    appName,
		AppName:        appName,
		Org:            "testorg",
		PackageVersion: "1.0.0",
		PackageType:    "prod",
	}
	if err := CreateFPMArchive(srcDir, outDir, meta, "1.0.0"); err != nil {
		t.Fatalf("CreateFPMArchive failed: %v", err)
	}
	return filepath.Join(outDir, appName+"-1.0.0.fpm")
}

// TestContentChecksumMatchesArchiveContents is the regression test for the checksum
// being recorded before requirements.txt / install_hooks.py were staged, which left
// those files outside the integrity hash.
func TestContentChecksumMatchesArchiveContents(t *testing.T) {
	fpmPath := packageTestApp(t, "checksum_app")

	recorded, err := metadata.ReadMetadataFromFPMArchive(fpmPath)
	if err != nil {
		t.Fatalf("failed to read metadata from archive: %v", err)
	}
	if recorded.ContentChecksum == "" {
		t.Fatal("archive recorded no content checksum")
	}

	if err := VerifyArchiveContentChecksum(fpmPath, recorded.ContentChecksum); err != nil {
		t.Fatalf("freshly packaged archive failed its own integrity check: %v", err)
	}
}

// TestContentChecksumCoversRootFiles proves the standard root files staged after the
// app source now affect the checksum. Before the fix these were hashed as absent.
func TestContentChecksumCoversRootFiles(t *testing.T) {
	appName := "roots_app"

	baseline := packageTestApp(t, appName)
	baseMeta, err := metadata.ReadMetadataFromFPMArchive(baseline)
	if err != nil {
		t.Fatalf("failed to read baseline metadata: %v", err)
	}

	for _, target := range []string{"requirements.txt", "install_hooks.py"} {
		t.Run(target, func(t *testing.T) {
			srcDir := buildTestApp(t, appName)
			// Change only the root file under test.
			if err := os.WriteFile(filepath.Join(srcDir, target), []byte("tampered\n"), 0o644); err != nil {
				t.Fatalf("failed to modify %s: %v", target, err)
			}

			outDir := t.TempDir()
			meta := &metadata.AppMetadata{
				PackageName:    appName,
				AppName:        appName,
				Org:            "testorg",
				PackageVersion: "1.0.0",
				PackageType:    "prod",
			}
			if err := CreateFPMArchive(srcDir, outDir, meta, "1.0.0"); err != nil {
				t.Fatalf("CreateFPMArchive failed: %v", err)
			}

			if meta.ContentChecksum == baseMeta.ContentChecksum {
				t.Fatalf("changing %s did not change the content checksum (%s); "+
					"it is excluded from the integrity hash", target, meta.ContentChecksum)
			}
		})
	}
}

// TestVerifyArchiveContentChecksumRejectsMismatch covers the tamper and missing cases.
func TestVerifyArchiveContentChecksumRejectsMismatch(t *testing.T) {
	fpmPath := packageTestApp(t, "reject_app")

	if err := VerifyArchiveContentChecksum(fpmPath, "not-the-real-checksum"); err == nil {
		t.Fatal("expected a mismatched checksum to be rejected, got nil error")
	}

	if err := VerifyArchiveContentChecksum(fpmPath, ""); err == nil {
		t.Fatal("expected an absent checksum to be reported as unverifiable, got nil error")
	}
}

// TestContentChecksumIsNotTheFileChecksum guards against the two checksums being
// conflated again: one hashes the extracted payload, the other the .fpm bytes.
func TestContentChecksumIsNotTheFileChecksum(t *testing.T) {
	fpmPath := packageTestApp(t, "distinct_app")

	contentSum, err := CalculateArchiveContentChecksum(fpmPath)
	if err != nil {
		t.Fatalf("CalculateArchiveContentChecksum failed: %v", err)
	}
	fileSum, err := utils.CalculateFileChecksum(fpmPath)
	if err != nil {
		t.Fatalf("CalculateFileChecksum failed: %v", err)
	}

	if contentSum == fileSum {
		t.Fatal("content checksum and file checksum should describe different inputs")
	}
}
