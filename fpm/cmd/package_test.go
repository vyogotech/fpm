package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateFrappeAppStructure(t *testing.T) {
	// This explicitAppName simulates the value passed via the --app-name flag.
	const explicitAppName = "myfrappeapp"

	// Helper to create dummy files
	createFile := func(path string) error {
		f, err := os.Create(path)
		if err != nil {
			return err
		}
		return f.Close()
	}

	t.Run("valid app structure", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "test-valid-app-")
		if err != nil {
			t.Fatalf("Failed to create temp dir: %v", err)
		}
		defer os.RemoveAll(tmpDir)

		// sourceDir is tmpDir. The directory to be validated is sourceDir/explicitAppName/
		appDirToValidate := filepath.Join(tmpDir, explicitAppName)
		if err := os.Mkdir(appDirToValidate, 0755); err != nil {
			t.Fatalf("Failed to create app dir to validate '%s': %v", appDirToValidate, err)
		}

		if err := createFile(filepath.Join(appDirToValidate, "__init__.py")); err != nil {
			t.Fatalf("Failed to create __init__.py: %v", err)
		}
		if err := createFile(filepath.Join(appDirToValidate, "hooks.py")); err != nil {
			t.Fatalf("Failed to create hooks.py: %v", err)
		}
		if err := createFile(filepath.Join(appDirToValidate, "modules.txt")); err != nil {
			t.Fatalf("Failed to create modules.txt: %v", err)
		}

		// Call with sourceDir = tmpDir, appName = explicitAppName
		err = validateFrappeAppStructure(tmpDir, explicitAppName)
		if err != nil {
			t.Errorf("Expected no error for valid structure, got %v", err)
		}
	})

	t.Run("missing inner app directory", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "test-missing-inner-")
		if err != nil {
			t.Fatalf("Failed to create temp dir: %v", err)
		}
		defer os.RemoveAll(tmpDir)

		// Do not create the directory tmpDir/explicitAppName/

		err = validateFrappeAppStructure(tmpDir, explicitAppName)
		if err == nil {
			t.Errorf("Expected error for missing inner app directory, got nil")
		} else {
			expectedPath := filepath.Join(tmpDir, explicitAppName)
			expectedErrorPart := fmt.Sprintf("app directory '%s' not found", expectedPath)
			if !strings.Contains(err.Error(), expectedErrorPart) {
				t.Errorf("Expected error message to contain '%s', but got '%s'", expectedErrorPart, err.Error())
			}
		}
	})

	t.Run("inner app path is a file not a directory", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "test-inner-is-file-")
		if err != nil {
			t.Fatalf("Failed to create temp dir: %v", err)
		}
		defer os.RemoveAll(tmpDir)

		// Create a file at tmpDir/explicitAppName instead of a directory
		filePathAsDir := filepath.Join(tmpDir, explicitAppName)
		if err := createFile(filePathAsDir); err != nil {
			t.Fatalf("Failed to create dummy file for app path: %v", err)
		}

		err = validateFrappeAppStructure(tmpDir, explicitAppName)
		if err == nil {
			t.Errorf("Expected error when app path is a file, got nil")
		} else {
			expectedErrorPart := fmt.Sprintf("'%s' is not a directory", filePathAsDir)
			if !strings.Contains(err.Error(), expectedErrorPart) {
				t.Errorf("Expected error message to contain '%s', but got '%s'", expectedErrorPart, err.Error())
			}
		}
	})

	t.Run("missing __init__.py", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "test-missing-init-")
		if err != nil {
			t.Fatalf("Failed to create temp dir: %v", err)
		}
		defer os.RemoveAll(tmpDir)

		appDirToValidate := filepath.Join(tmpDir, explicitAppName)
		if err := os.Mkdir(appDirToValidate, 0755); err != nil {
			t.Fatalf("Failed to create app dir to validate '%s': %v", appDirToValidate, err)
		}

		// Missing __init__.py
		if err := createFile(filepath.Join(appDirToValidate, "hooks.py")); err != nil {
			t.Fatalf("Failed to create hooks.py: %v", err)
		}
		if err := createFile(filepath.Join(appDirToValidate, "modules.txt")); err != nil {
			t.Fatalf("Failed to create modules.txt: %v", err)
		}

		err = validateFrappeAppStructure(tmpDir, explicitAppName)
		if err == nil {
			t.Errorf("Expected error for missing __init__.py, got nil")
		} else {
			expectedPath := filepath.Join(appDirToValidate, "__init__.py")
			expectedErrorPart := fmt.Sprintf("file '%s' not found", expectedPath)
			if !strings.Contains(err.Error(), expectedErrorPart) {
				t.Errorf("Expected error message to contain '%s', but got '%s'", expectedErrorPart, err.Error())
			}
		}
	})

	t.Run("missing hooks.py", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "test-missing-hooks-")
		if err != nil {
			t.Fatalf("Failed to create temp dir: %v", err)
		}
		defer os.RemoveAll(tmpDir)

		appDirToValidate := filepath.Join(tmpDir, explicitAppName)
		if err := os.Mkdir(appDirToValidate, 0755); err != nil {
			t.Fatalf("Failed to create app dir to validate '%s': %v", appDirToValidate, err)
		}

		if err := createFile(filepath.Join(appDirToValidate, "__init__.py")); err != nil {
			t.Fatalf("Failed to create __init__.py: %v", err)
		}
		// Missing hooks.py
		if err := createFile(filepath.Join(appDirToValidate, "modules.txt")); err != nil {
			t.Fatalf("Failed to create modules.txt: %v", err)
		}

		err = validateFrappeAppStructure(tmpDir, explicitAppName)
		if err == nil {
			t.Errorf("Expected error for missing hooks.py, got nil")
		} else {
			expectedPath := filepath.Join(appDirToValidate, "hooks.py")
			expectedErrorPart := fmt.Sprintf("file '%s' not found", expectedPath)
			if !strings.Contains(err.Error(), expectedErrorPart) {
				t.Errorf("Expected error message to contain '%s', but got '%s'", expectedErrorPart, err.Error())
			}
		}
	})

	t.Run("missing modules.txt", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "test-missing-modules-")
		if err != nil {
			t.Fatalf("Failed to create temp dir: %v", err)
		}
		defer os.RemoveAll(tmpDir)

		appDirToValidate := filepath.Join(tmpDir, explicitAppName)
		if err := os.Mkdir(appDirToValidate, 0755); err != nil {
			t.Fatalf("Failed to create app dir to validate '%s': %v", appDirToValidate, err)
		}

		if err := createFile(filepath.Join(appDirToValidate, "__init__.py")); err != nil {
			t.Fatalf("Failed to create __init__.py: %v", err)
		}
		if err := createFile(filepath.Join(appDirToValidate, "hooks.py")); err != nil {
			t.Fatalf("Failed to create hooks.py: %v", err)
		}
		// Missing modules.txt

		err = validateFrappeAppStructure(tmpDir, explicitAppName)
		if err == nil {
			t.Errorf("Expected error for missing modules.txt, got nil")
		} else {
			expectedPath := filepath.Join(appDirToValidate, "modules.txt")
			expectedErrorPart := fmt.Sprintf("file '%s' not found", expectedPath)
			if !strings.Contains(err.Error(), expectedErrorPart) {
				t.Errorf("Expected error message to contain '%s', but got '%s'", expectedErrorPart, err.Error())
			}
		}
	})

	// Test cases for when a required component is a directory instead of a file
	testCasesIsDirectory := []struct {
		name         string
		fileToMakeDir string // e.g., "__init__.py"
	}{
		{"__init__.py is a directory", "__init__.py"},
		{"hooks.py is a directory", "hooks.py"},
		{"modules.txt is a directory", "modules.txt"},
	}

	for _, tc := range testCasesIsDirectory {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir, err := os.MkdirTemp("", "test-isdir-")
			if err != nil {
				t.Fatalf("Failed to create temp dir: %v", err)
			}
			defer os.RemoveAll(tmpDir)

			// appDirToValidate is tmpDir/explicitAppName/
			appDirToValidate := filepath.Join(tmpDir, explicitAppName)
			if err := os.MkdirAll(appDirToValidate, 0755); err != nil {
				t.Fatalf("Failed to create app dir to validate '%s': %v", appDirToValidate, err)
			}

			// Create other required files
			if tc.fileToMakeDir != "__init__.py" {
				if err := createFile(filepath.Join(appDirToValidate, "__init__.py")); err != nil {
					t.Fatalf("Failed to create __init__.py: %v", err)
				}
			}
			if tc.fileToMakeDir != "hooks.py" {
				if err := createFile(filepath.Join(appDirToValidate, "hooks.py")); err != nil {
					t.Fatalf("Failed to create hooks.py: %v", err)
				}
			}
			if tc.fileToMakeDir != "modules.txt" {
				if err := createFile(filepath.Join(appDirToValidate, "modules.txt")); err != nil {
					t.Fatalf("Failed to create modules.txt: %v", err)
				}
			}

			// Create the component that should be a file as a directory
			pathAsDir := filepath.Join(appDirToValidate, tc.fileToMakeDir)
			if err := os.Mkdir(pathAsDir, 0755); err != nil {
				t.Fatalf("Failed to create %s as directory: %v", tc.fileToMakeDir, err)
			}

			err = validateFrappeAppStructure(tmpDir, explicitAppName)
			if err == nil {
				t.Errorf("Expected error for %s being a directory, got nil", tc.fileToMakeDir)
			} else {
				expectedErrorPart := fmt.Sprintf("'%s' is a directory, not a file", pathAsDir)
				if !strings.Contains(err.Error(), expectedErrorPart) {
					t.Errorf("Expected error message to contain '%s', but got '%s'", expectedErrorPart, err.Error())
				}
			}
		})
	}

	t.Run("valid complex mock app structure", func(t *testing.T) {
		sourceDir, err := os.MkdirTemp("", "test-complex-app-") // This is the sourceDir
		if err != nil {
			t.Fatalf("Failed to create temp dir for complex app: %v", err)
		}
		defer os.RemoveAll(sourceDir)

		// explicitComplexAppName simulates the value from --app-name flag.
		// This name defines the sub-directory within sourceDir that contains the app.
		const explicitComplexAppName = "mycomplexapp"

		// This is the actual directory where app files (__init__.py etc.) are located.
		// It's sourceDir/explicitComplexAppName/
		appActualDir := filepath.Join(sourceDir, explicitComplexAppName)
		if err := os.Mkdir(appActualDir, 0755); err != nil {
			t.Fatalf("Failed to create inner app dir '%s': %v", appActualDir, err)
		}

		// Create required files inside appActualDir
		requiredFiles := []string{"__init__.py", "hooks.py", "modules.txt"}
		for _, fName := range requiredFiles {
			if err := createFile(filepath.Join(appActualDir, fName)); err != nil {
				t.Fatalf("Failed to create required file %s: %v", fName, err)
			}
		}

		// Create additional realistic structure inside appActualDir
		publicJsDir := filepath.Join(appActualDir, "public", "js")
		if err := os.MkdirAll(publicJsDir, 0755); err != nil {
			t.Fatalf("Failed to create public/js dir: %v", err)
		}

		templatesPagesDir := filepath.Join(appActualDir, "templates", "pages")
		if err := os.MkdirAll(templatesPagesDir, 0755); err != nil {
			t.Fatalf("Failed to create templates/pages dir: %v", err)
		}
		if err := createFile(filepath.Join(templatesPagesDir, "test_page.html")); err != nil {
			t.Fatalf("Failed to create test_page.html: %v", err)
		}

		doctypeDirName := explicitComplexAppName + "_doctype"
		doctypeActualDir := filepath.Join(appActualDir, doctypeDirName)
		if err := os.Mkdir(doctypeActualDir, 0755); err != nil {
			t.Fatalf("Failed to create doctype dir %s: %v", doctypeDirName, err)
		}
		doctypeJsonFileName := explicitComplexAppName + "_doctype.json"
		if err := createFile(filepath.Join(doctypeActualDir, doctypeJsonFileName)); err != nil {
			t.Fatalf("Failed to create %s: %v", doctypeJsonFileName, err)
		}

		// Root files in sourceDir (e.g., setup.py) are not checked by validateFrappeAppStructure,
		// as it only looks inside sourceDir/appName. So, creating them here is for realism of a source repo,
		// but doesn't directly affect this specific validation unit test's core logic.
		if err := createFile(filepath.Join(sourceDir, "setup.py")); err != nil {
			t.Fatalf("Failed to create setup.py: %v", err)
		}
		if err := createFile(filepath.Join(sourceDir, "MANIFEST.in")); err != nil {
			t.Fatalf("Failed to create MANIFEST.in: %v", err)
		}

		// Call validateFrappeAppStructure with sourceDir and explicitComplexAppName.
		// It will check for sourceDir/explicitComplexAppName/__init__.py etc.
		err = validateFrappeAppStructure(sourceDir, explicitComplexAppName)

		if err != nil {
			t.Errorf("Expected no error for valid complex app structure, but got: %v", err)
		}
	})
}

// Note: This test file assumes that `validateFrappeAppStructure` is in the `cmd` package.
// If it's in a different package, the import path for `validateFrappeAppStructure` would need adjustment,
// but since they are in the same package `cmd`, direct calls are fine.
// The function `validateFrappeAppStructure` itself is not exported (lowercase 'v'),
// so this test file `package_test.go` must be in the same `cmd` package, which it is.
// Build constraints or tags are not needed as long as they are in the same package.

// To run these tests:
// cd <path_to_fpm_directory>/fpm/cmd
// go test

// To run with coverage:
// go test -coverprofile=coverage.out
// go tool cover -html=coverage.out
