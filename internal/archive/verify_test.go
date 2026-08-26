package archive

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fpm/internal/metadata"
	"fpm/internal/utils"
	"fpm/internal/wheels"
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

// TestDefaultPackagingVendorsNoWheels guards the default path: packaging without
// --bundle-deps must behave exactly as it did before vendoring existed.
func TestDefaultPackagingVendorsNoWheels(t *testing.T) {
	srcDir := buildTestApp(t, "novendor_app")
	outDir := t.TempDir()
	meta := &metadata.AppMetadata{
		PackageName:    "novendor_app",
		AppName:        "novendor_app",
		Org:            "testorg",
		PackageVersion: "1.0.0",
		PackageType:    "prod",
	}
	if err := CreateFPMArchive(srcDir, outDir, meta, "1.0.0"); err != nil {
		t.Fatalf("CreateFPMArchive failed: %v", err)
	}

	if meta.WheelPlatform != "" {
		t.Fatalf("default packaging should record no wheel platform, got %q", meta.WheelPlatform)
	}

	fpmPath := filepath.Join(outDir, "novendor_app-1.0.0.fpm")
	names, err := archiveEntryNames(fpmPath)
	if err != nil {
		t.Fatalf("failed to list archive: %v", err)
	}
	for _, n := range names {
		if strings.HasPrefix(n, wheels.DirName+"/") {
			t.Fatalf("default packaging should not bundle wheels, found %q", n)
		}
	}
}

// TestBundleDepsSkipsAppWithoutRequirements covers an app with no Python
// dependencies: --bundle-deps is a no-op rather than an error, and crucially does
// not require pip to be installed.
func TestBundleDepsSkipsAppWithoutRequirements(t *testing.T) {
	srcDir := t.TempDir()
	moduleDir := filepath.Join(srcDir, "nodeps_app")
	if err := os.MkdirAll(moduleDir, 0o755); err != nil {
		t.Fatalf("failed to create module dir: %v", err)
	}
	for name, content := range map[string]string{
		"hooks.py":    "app_name = \"nodeps_app\"\n",
		"__init__.py": "__version__ = \"1.0.0\"\n",
		"modules.txt": "Test Module\n",
	} {
		if err := os.WriteFile(filepath.Join(moduleDir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("failed to write %s: %v", name, err)
		}
	}

	outDir := t.TempDir()
	meta := &metadata.AppMetadata{
		PackageName:    "nodeps_app",
		AppName:        "nodeps_app",
		Org:            "testorg",
		PackageVersion: "1.0.0",
		PackageType:    "prod",
	}
	err := CreateFPMArchive(srcDir, outDir, meta, "1.0.0", Options{
		BundleDeps:  true,
		WheelTarget: wheels.Target{Platforms: []string{wheels.DefaultProdPlatform}, PythonVersion: "3.11"},
	})
	if err != nil {
		t.Fatalf("vendoring an app with no requirements.txt should succeed, got: %v", err)
	}
	if meta.WheelPlatform != "" {
		t.Fatalf("nothing was vendored, so no wheel platform should be recorded, got %q", meta.WheelPlatform)
	}
}

// TestVendoredWheelsAreStagedBeforeChecksum is the ordering guard for vendoring: the
// wheels must be staged before the content checksum is calculated, or the package would
// ship executable dependency code outside its own integrity hash — the same class of
// hole that once excluded install_hooks.py.
//
// It substitutes the vendoring step so the test needs neither pip nor network access.
func TestVendoredWheelsAreStagedBeforeChecksum(t *testing.T) {
	srcDir := buildTestApp(t, "staged_app")
	outDir := t.TempDir()
	meta := &metadata.AppMetadata{
		PackageName:    "staged_app",
		AppName:        "staged_app",
		Org:            "testorg",
		PackageVersion: "1.0.0",
		PackageType:    "prod",
	}

	fakeBundle := func(requirementsPath, destDir string, _ wheels.Target) (wheels.Result, error) {
		if _, err := os.Stat(requirementsPath); err != nil {
			return wheels.Result{}, err // vendoring must run after requirements.txt is staged
		}
		if err := os.MkdirAll(destDir, 0o755); err != nil {
			return wheels.Result{}, err
		}
		wheelPath := filepath.Join(destDir, "frappe-15.0.0-py3-none-any.whl")
		if err := os.WriteFile(wheelPath, []byte("fake wheel payload"), 0o644); err != nil {
			return wheels.Result{}, err
		}
		return wheels.Result{Bundled: true}, nil
	}

	err := CreateFPMArchive(srcDir, outDir, meta, "1.0.0", Options{
		BundleDeps:  true,
		WheelTarget: wheels.Target{Platforms: []string{wheels.DefaultProdPlatform}, PythonVersion: "3.11"},
		bundle:      fakeBundle,
	})
	if err != nil {
		t.Fatalf("CreateFPMArchive failed: %v", err)
	}

	if meta.WheelPlatform != wheels.DefaultProdPlatform {
		t.Fatalf("expected wheel platform %q recorded, got %q", wheels.DefaultProdPlatform, meta.WheelPlatform)
	}

	fpmPath := filepath.Join(outDir, "staged_app-1.0.0.fpm")

	// The wheel must actually be in the archive.
	names, err := archiveEntryNames(fpmPath)
	if err != nil {
		t.Fatalf("failed to list archive: %v", err)
	}
	found := false
	for _, n := range names {
		if n == wheels.DirName+"/frappe-15.0.0-py3-none-any.whl" {
			found = true
		}
	}
	if !found {
		t.Fatalf("vendored wheel missing from archive; entries: %v", names)
	}

	// And the recorded checksum must describe an archive that includes it. If wheels
	// were staged after the checksum, this verification fails.
	recorded, err := metadata.ReadMetadataFromFPMArchive(fpmPath)
	if err != nil {
		t.Fatalf("failed to read metadata from archive: %v", err)
	}
	if err := VerifyArchiveContentChecksum(fpmPath, recorded.ContentChecksum); err != nil {
		t.Fatalf("vendored wheels fall outside the content checksum: %v", err)
	}
}

// TestContentChecksumCoversVendoredWheels proves a bundled wheel cannot be swapped
// without breaking the package's integrity checksum. Vendored wheels are executable
// dependency code, so leaving them outside the hash would reintroduce the class of
// hole that excluded install_hooks.py.
func TestContentChecksumCoversVendoredWheels(t *testing.T) {
	base := packageTestApp(t, "wheelsum_app")
	baseSum, err := CalculateArchiveContentChecksum(base)
	if err != nil {
		t.Fatalf("failed to checksum baseline archive: %v", err)
	}

	// Rebuild the same archive with a wheel added, mimicking a vendored payload.
	withWheel := rebuildWithExtraEntry(t, base, wheels.DirName+"/frappe-15.0.0-py3-none-any.whl", "wheel-payload")
	withWheelSum, err := CalculateArchiveContentChecksum(withWheel)
	if err != nil {
		t.Fatalf("failed to checksum archive with wheel: %v", err)
	}
	if withWheelSum == baseSum {
		t.Fatal("adding a vendored wheel did not change the content checksum")
	}

	// Now tamper with the wheel's contents only.
	tampered := rebuildWithExtraEntry(t, base, wheels.DirName+"/frappe-15.0.0-py3-none-any.whl", "tampered-payload")
	tamperedSum, err := CalculateArchiveContentChecksum(tampered)
	if err != nil {
		t.Fatalf("failed to checksum tampered archive: %v", err)
	}
	if tamperedSum == withWheelSum {
		t.Fatal("swapping a vendored wheel's contents did not change the content checksum")
	}
}

// archiveEntryNames lists the entry names inside an .fpm.
func archiveEntryNames(fpmPath string) ([]string, error) {
	r, err := zip.OpenReader(fpmPath)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	names := make([]string, 0, len(r.File))
	for _, f := range r.File {
		names = append(names, f.Name)
	}
	return names, nil
}

// rebuildWithExtraEntry copies an .fpm, adding one extra entry, and returns the new path.
func rebuildWithExtraEntry(t *testing.T, srcFPM, entryName, entryContent string) string {
	t.Helper()

	src, err := zip.OpenReader(srcFPM)
	if err != nil {
		t.Fatalf("failed to open source archive: %v", err)
	}
	defer src.Close()

	outPath := filepath.Join(t.TempDir(), "rebuilt.fpm")
	outFile, err := os.Create(outPath)
	if err != nil {
		t.Fatalf("failed to create rebuilt archive: %v", err)
	}
	defer outFile.Close()

	zw := zip.NewWriter(outFile)
	for _, f := range src.File {
		w, err := zw.Create(f.Name)
		if err != nil {
			t.Fatalf("failed to create entry %s: %v", f.Name, err)
		}
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("failed to open entry %s: %v", f.Name, err)
		}
		if _, err := io.Copy(w, rc); err != nil {
			rc.Close()
			t.Fatalf("failed to copy entry %s: %v", f.Name, err)
		}
		rc.Close()
	}

	w, err := zw.Create(entryName)
	if err != nil {
		t.Fatalf("failed to create extra entry: %v", err)
	}
	if _, err := io.WriteString(w, entryContent); err != nil {
		t.Fatalf("failed to write extra entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("failed to close rebuilt archive: %v", err)
	}
	return outPath
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
