package metadata

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadAppMetadata(t *testing.T) {
	// Setup: Create a temporary app directory
	tmpDir, err := os.MkdirTemp("", "test-app-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Case 1: app_metadata.json exists and is valid
	validMetadataContent := `{
		"package_name": "test_app",
		"package_version": "0.0.1",
		"dependencies": {"frappe": "13.0.0"}
	}`
	validMetadataPath := filepath.Join(tmpDir, "app_metadata.json")
	if err := os.WriteFile(validMetadataPath, []byte(validMetadataContent), 0644); err != nil {
		t.Fatalf("Failed to write valid metadata file: %v", err)
	}

	loadedMeta, err := LoadAppMetadata(tmpDir)
	if err != nil {
		t.Errorf("LoadAppMetadata failed for valid file: %v", err)
	}
	expectedMeta := &AppMetadata{
		PackageName:         "test_app",
		PackageVersion:      "0.0.1",
		Dependencies:        map[string]string{"frappe": "13.0.0"},
		FrappeCompatibility: make([]string, 0), // Should match initialized empty slice
		Hooks:               make(map[string]string), // Should match initialized empty map
		SourceControlURL:    "", // Expect zero value if not in JSON
		PackageType:         "", // Expect zero value if not in JSON
		ContentChecksum:     "", // Expect zero value if not in JSON
	}
	if !reflect.DeepEqual(loadedMeta, expectedMeta) {
		t.Errorf("Loaded metadata mismatch. Got %#v, want %#v", loadedMeta, expectedMeta) // Using %#v for more detail
	}

	// Case 2: app_metadata.json does not exist
	nonExistentAppDir, err := os.MkdirTemp("", "test-nonexistent-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(nonExistentAppDir)

	emptyMeta, err := LoadAppMetadata(nonExistentAppDir)
	if err != nil {
		t.Errorf("LoadAppMetadata failed for non-existent file: %v", err)
	}
	if emptyMeta.PackageName != "" || emptyMeta.PackageVersion != "" { // Should be empty
		t.Errorf("Expected empty metadata for non-existent file, got %+v", emptyMeta)
	}
    if emptyMeta.Dependencies == nil { // Should be initialized map, not nil
        t.Errorf("Expected initialized Dependencies map, got nil")
    }


	// Case 3: app_metadata.json is malformed
	malformedMetadataContent := `{"packageName": "test_malformed",` // Missing closing brace
	// Need to create a new dir for this specific test, or ensure LoadAppMetadata loads by specific filename
    // For simplicity, let's assume LoadAppMetadata loads "app_metadata.json" from the given dir.
    // So, we'll create a new temp dir for this test.
    malformedDir, err := os.MkdirTemp("", "test-malformed-")
    if err != nil {
        t.Fatalf("Failed to create temp dir for malformed test: %v", err)
    }
    defer os.RemoveAll(malformedDir)
    malformedFilePath := filepath.Join(malformedDir, "app_metadata.json")

	if err := os.WriteFile(malformedFilePath, []byte(malformedMetadataContent), 0644); err != nil {
		t.Fatalf("Failed to write malformed metadata file: %v", err)
	}
	_, err = LoadAppMetadata(malformedDir)
	if err == nil {
		t.Errorf("LoadAppMetadata should have failed for malformed file, but it didn't")
	}
}

func TestGenerateAppMetadata(t *testing.T) {
	tmpAppDir, err := os.MkdirTemp("", "test-gen-app-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpAppDir)

	appName := filepath.Base(tmpAppDir)
	version := "1.2.3"

	generatedMeta, err := GenerateAppMetadata(tmpAppDir, version)
	if err != nil {
		t.Fatalf("GenerateAppMetadata failed: %v", err)
	}

	if generatedMeta.PackageName != appName {
		t.Errorf("Generated package name mismatch. Got %s, want %s", generatedMeta.PackageName, appName)
	}
	if generatedMeta.PackageVersion != version {
		t.Errorf("Generated package version mismatch. Got %s, want %s", generatedMeta.PackageVersion, version)
	}
    if generatedMeta.Dependencies == nil {
        t.Errorf("Generated metadata Dependencies should not be nil")
    }
    if generatedMeta.FrappeCompatibility == nil {
        t.Errorf("Generated metadata FrappeCompatibility should not be nil")
    }
    if generatedMeta.Hooks == nil {
        t.Errorf("Generated metadata Hooks should not be nil")
    }
}

func TestSaveAndLoadAppMetadata(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "test-save-load-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	metaToSave := &AppMetadata{
		PackageName:    "saved_app",
		PackageVersion: "1.0.0",
		Description:    "A test app",
		Dependencies:   map[string]string{"frappe": "14.0.0"},
        FrappeCompatibility: []string{"14.x.x"},
        Hooks: map[string]string{"install": "install.py"},
        SourceControlURL: "https://github.com/test/app.git",
        PackageType: "prod",
        ContentChecksum: "dummychecksum123abc",
	}

	err = SaveAppMetadata(tmpDir, metaToSave)
	if err != nil {
		t.Fatalf("SaveAppMetadata failed: %v", err)
	}

	loadedMeta, err := LoadAppMetadata(tmpDir) // LoadAppMetadata expects app_metadata.json in tmpDir
	if err != nil {
		t.Fatalf("LoadAppMetadata failed after save: %v", err)
	}

	if !reflect.DeepEqual(loadedMeta, metaToSave) {
		t.Errorf("Loaded metadata after save does not match original. Got %+v, want %+v", loadedMeta, metaToSave)
	}
}
