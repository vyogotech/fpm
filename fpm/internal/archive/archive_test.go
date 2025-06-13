package archive

import (
	"archive/zip"
	// "bytes" // Removed
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	// "reflect" // Removed
	"sort"
	"strings"
	"testing"
	"fpm/internal/metadata" // Import your metadata package
)

// Helper function to create a mock app structure
func createMockApp(t *testing.T, basePath string, appName string, files map[string]string, fpmIgnoreContent string) {
	appPath := filepath.Join(basePath, appName)
	if err := os.MkdirAll(filepath.Join(appPath, appName, "doctype", "test_doc"), 0755); err != nil { // Simulate app_source structure
		t.Fatalf("Failed to create mock app dir: %v", err)
	}
    // Create app_source structure by creating the appName dir inside appPath
    // as CreateFPMArchive expects appSourcePath to be the root of the app repo
    // and then copies its content into app_source in the archive.
    // The files map paths should be relative to appName (the app repo root).

	for p, content := range files {
		filePath := filepath.Join(appPath, p)
		dir := filepath.Dir(filePath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("Failed to create dir %s for mock file: %v", dir, err)
		}
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to write mock file %s: %v", p, err)
		}
	}

	if fpmIgnoreContent != "" {
		if err := os.WriteFile(filepath.Join(appPath, ".fpmignore"), []byte(fpmIgnoreContent), 0644); err != nil {
			t.Fatalf("Failed to write .fpmignore: %v", err)
		}
	}
}

// Helper to check if a path exists in a zip archive and optionally its content
func checkZipContent(t *testing.T, zipFilePath string, expectedFiles map[string]*string) {
	r, err := zip.OpenReader(zipFilePath)
	if err != nil {
		t.Fatalf("Failed to open zip file %s: %v", zipFilePath, err)
	}
	defer r.Close()

	foundCount := 0
	for _, f := range r.File {
		expectedContent, ok := expectedFiles[f.Name]
		if !ok { // File in zip not in expectedFiles (could be an unexpected file)
			// For now, we only check if expected files are present.
			// A more strict test would fail if extra files are found.
			continue
		}
		foundCount++

		if expectedContent != nil { // If content check is required
			rc, err := f.Open()
			if err != nil {
				t.Errorf("Failed to open file %s in zip: %v", f.Name, err)
				continue
			}
			defer rc.Close()

			contentBytes, err := io.ReadAll(rc)
			if err != nil {
				t.Errorf("Failed to read file %s in zip: %v", f.Name, err)
				continue
			}
			if string(contentBytes) != *expectedContent {
				t.Errorf("Content mismatch for %s in zip. Got '%s', want '%s'", f.Name, string(contentBytes), *expectedContent)
			}
		}
		delete(expectedFiles, f.Name) // Remove found file from map
	}

	if len(expectedFiles) > 0 {
		missingFiles := []string{}
		for k := range expectedFiles {
			missingFiles = append(missingFiles, k)
		}
		sort.Strings(missingFiles)
		t.Errorf("Zip file %s is missing expected files: %v", zipFilePath, strings.Join(missingFiles, ", "))
	}
}


func TestCreateFPMArchive(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "test-archive-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	mockAppBasePath := filepath.Join(tmpDir, "apps") // Where mock apps will be created
	outputPath := filepath.Join(tmpDir, "output")   // Where .fpm files will be saved
	if err := os.MkdirAll(mockAppBasePath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outputPath, 0755); err != nil {
		t.Fatal(err)
	}

	appName := "my_test_app"
	appVersion := "0.1.0"
	appSourcePath := filepath.Join(mockAppBasePath, appName)

	// Sample files for the mock app
	appFiles := map[string]string{
		"app_metadata.json":         `{"package_name": "my_test_app", "package_version": "0.0.1", "description": "A test app."}`, // Version will be overridden
		"requirements.txt":          "frappe>=13.0.0",
		"install_hooks.py":          "print('hello from install hook')",
		"my_test_app/file1.py":      "print('file1')",
		"my_test_app/data/file.json": "{\"key\": \"value\"}",
		"my_test_app/ignored_file.txt": "this should be ignored",
		"public/js/script.js":       "console.log('script');", // Should be under app_source/public/js/script.js
		"compiled_assets/css/style.css": "body { color: red; }", // Should be at root of archive
	}
    // Simplified ignore:
    fpmIgnoreContentSimple := "my_test_app/ignored_file.txt\n"


	createMockApp(t, mockAppBasePath, appName, appFiles, fpmIgnoreContentSimple)

	meta, err := metadata.LoadAppMetadata(appSourcePath) // Load the mock metadata
	if err != nil {
		t.Fatalf("Failed to load mock app metadata: %v", err)
	}
    // Ensure version is set correctly for CreateFPMArchive call
    meta.PackageVersion = appVersion


	err = CreateFPMArchive(appSourcePath, outputPath, meta, appVersion)
	if err != nil {
		t.Fatalf("CreateFPMArchive failed: %v", err)
	}

	expectedFPMFilename := filepath.Join(outputPath, appName+"-"+appVersion+".fpm")
	if _, err := os.Stat(expectedFPMFilename); os.IsNotExist(err) {
		t.Fatalf(".fpm file was not created: %s", expectedFPMFilename)
	}

	// Verify ZIP contents
	expectedMetadataJson, _ := json.MarshalIndent(meta, "", "  ")
	strExpectedMetadataJson := string(expectedMetadataJson)
	strRequirementsTxt := "frappe>=13.0.0"
	strInstallHooksPy := "print('hello from install hook')"
    strAppSourceFile1Py := "print('file1')"
    strAppSourceDataFileJson := "{\"key\": \"value\"}"
    strCompiledAssetsStyleCss := "body { color: red; }"


	expectedFilesInZip := map[string]*string{
		"app_metadata.json":         &strExpectedMetadataJson,
		"requirements.txt":          &strRequirementsTxt,
		"install_hooks.py":          &strInstallHooksPy,
		"my_test_app/file1.py": &strAppSourceFile1Py, // No app_source prefix
        "my_test_app/data/file.json": &strAppSourceDataFileJson, // No app_source prefix
		"public/js/script.js": nil, // No app_source prefix, this path is relative to app source root
		"compiled_assets/css/style.css": &strCompiledAssetsStyleCss,
	}

	checkZipContent(t, expectedFPMFilename, expectedFilesInZip)

    // Check that ignored_file.txt is NOT in the zip
    r, _ := zip.OpenReader(expectedFPMFilename)
    defer r.Close()
    for _, f := range r.File {
        if strings.HasSuffix(f.Name, "ignored_file.txt") {
            t.Errorf("Found ignored file 'my_test_app/ignored_file.txt' in archive at %s", f.Name)
        }
    }
}
