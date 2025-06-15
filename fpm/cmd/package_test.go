package cmd

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"os/exec"
	"strings"
	"testing"
	"runtime" // For OS-specific HOME for setupTempFPMConfig
	"bytes"   // For executeCommand buffer

	"fpm/internal/metadata"
	"fpm/internal/config" // For FPM_APPS_BASE_PATH logic

	"github.com/spf13/pflag" // For resetting flags
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		require.NoError(t, err)
		defer os.RemoveAll(tmpDir)

		appDirToValidate := filepath.Join(tmpDir, explicitAppName)
		require.NoError(t, os.Mkdir(appDirToValidate, 0755))

		require.NoError(t, createFile(filepath.Join(appDirToValidate, "__init__.py")))
		require.NoError(t, createFile(filepath.Join(appDirToValidate, "hooks.py")))
		require.NoError(t, createFile(filepath.Join(appDirToValidate, "modules.txt")))

		err = validateFrappeAppStructure(tmpDir, explicitAppName)
		assert.NoError(t, err)
	})

	t.Run("missing inner app directory", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "test-missing-inner-")
		require.NoError(t, err)
		defer os.RemoveAll(tmpDir)

		err = validateFrappeAppStructure(tmpDir, explicitAppName)
		assert.Error(t, err)
		if err != nil {
			expectedPath := filepath.Join(tmpDir, explicitAppName)
			assert.Contains(t, err.Error(), fmt.Sprintf("app directory '%s' not found", expectedPath))
		}
	})

	t.Run("inner app path is a file not a directory", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "test-inner-is-file-")
		require.NoError(t, err)
		defer os.RemoveAll(tmpDir)

		filePathAsDir := filepath.Join(tmpDir, explicitAppName)
		require.NoError(t, createFile(filePathAsDir))

		err = validateFrappeAppStructure(tmpDir, explicitAppName)
		assert.Error(t, err)
		if err != nil {
			assert.Contains(t, err.Error(), fmt.Sprintf("'%s' is not a directory", filePathAsDir))
		}
	})

	t.Run("missing __init__.py", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "test-missing-init-")
		require.NoError(t, err)
		defer os.RemoveAll(tmpDir)

		appDirToValidate := filepath.Join(tmpDir, explicitAppName)
		require.NoError(t, os.Mkdir(appDirToValidate, 0755))
		require.NoError(t, createFile(filepath.Join(appDirToValidate, "hooks.py")))
		require.NoError(t, createFile(filepath.Join(appDirToValidate, "modules.txt")))

		err = validateFrappeAppStructure(tmpDir, explicitAppName)
		assert.Error(t, err)
		if err != nil {
			assert.Contains(t, err.Error(), fmt.Sprintf("file '%s' not found", filepath.Join(appDirToValidate, "__init__.py")))
		}
	})

	t.Run("missing hooks.py", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "test-missing-hooks-")
		require.NoError(t, err)
		defer os.RemoveAll(tmpDir)

		appDirToValidate := filepath.Join(tmpDir, explicitAppName)
		require.NoError(t, os.Mkdir(appDirToValidate, 0755))
		require.NoError(t, createFile(filepath.Join(appDirToValidate, "__init__.py")))
		require.NoError(t, createFile(filepath.Join(appDirToValidate, "modules.txt")))

		err = validateFrappeAppStructure(tmpDir, explicitAppName)
		assert.Error(t, err)
		if err != nil {
			assert.Contains(t, err.Error(), fmt.Sprintf("file '%s' not found", filepath.Join(appDirToValidate, "hooks.py")))
		}
	})

	t.Run("missing modules.txt", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "test-missing-modules-")
		require.NoError(t, err)
		defer os.RemoveAll(tmpDir)

		appDirToValidate := filepath.Join(tmpDir, explicitAppName)
		require.NoError(t, os.Mkdir(appDirToValidate, 0755))
		require.NoError(t, createFile(filepath.Join(appDirToValidate, "__init__.py")))
		require.NoError(t, createFile(filepath.Join(appDirToValidate, "hooks.py")))

		err = validateFrappeAppStructure(tmpDir, explicitAppName)
		assert.Error(t, err)
		if err != nil {
			assert.Contains(t, err.Error(), fmt.Sprintf("file '%s' not found", filepath.Join(appDirToValidate, "modules.txt")))
		}
	})

	testCasesIsDirectory := []struct {
		name         string
		fileToMakeDir string
	}{
		{"__init__.py is a directory", "__init__.py"},
		{"hooks.py is a directory", "hooks.py"},
		{"modules.txt is a directory", "modules.txt"},
	}

	for _, tc := range testCasesIsDirectory {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir, err := os.MkdirTemp("", "test-isdir-")
			require.NoError(t, err)
			defer os.RemoveAll(tmpDir)

			appDirToValidate := filepath.Join(tmpDir, explicitAppName)
			require.NoError(t, os.MkdirAll(appDirToValidate, 0755))

			if tc.fileToMakeDir != "__init__.py" {
				require.NoError(t, createFile(filepath.Join(appDirToValidate, "__init__.py")))
			}
			if tc.fileToMakeDir != "hooks.py" {
				require.NoError(t, createFile(filepath.Join(appDirToValidate, "hooks.py")))
			}
			if tc.fileToMakeDir != "modules.txt" {
				require.NoError(t, createFile(filepath.Join(appDirToValidate, "modules.txt")))
			}

			pathAsDir := filepath.Join(appDirToValidate, tc.fileToMakeDir)
			require.NoError(t, os.Mkdir(pathAsDir, 0755))

			err = validateFrappeAppStructure(tmpDir, explicitAppName)
			assert.Error(t, err)
			if err != nil {
				assert.Contains(t, err.Error(), fmt.Sprintf("'%s' is a directory, not a file", pathAsDir))
			}
		})
	}

	t.Run("valid complex mock app structure", func(t *testing.T) {
		sourceDir, err := os.MkdirTemp("", "test-complex-app-")
		require.NoError(t, err)
		defer os.RemoveAll(sourceDir)
		const explicitComplexAppName = "mycomplexapp"
		appActualDir := filepath.Join(sourceDir, explicitComplexAppName)
		require.NoError(t, os.Mkdir(appActualDir, 0755))

		requiredFiles := []string{"__init__.py", "hooks.py", "modules.txt"}
		for _, fName := range requiredFiles {
			require.NoError(t, createFile(filepath.Join(appActualDir, fName)))
		}
		require.NoError(t, os.MkdirAll(filepath.Join(appActualDir, "public", "js"), 0755))
		templatesPagesDir := filepath.Join(appActualDir, "templates", "pages")
		require.NoError(t, os.MkdirAll(templatesPagesDir, 0755))
		require.NoError(t, createFile(filepath.Join(templatesPagesDir, "test_page.html")))
		doctypeDirName := explicitComplexAppName + "_doctype"
		doctypeActualDir := filepath.Join(appActualDir, doctypeDirName)
		require.NoError(t, os.Mkdir(doctypeActualDir, 0755))
		require.NoError(t, createFile(filepath.Join(doctypeActualDir, explicitComplexAppName+"_doctype.json")))
		require.NoError(t, createFile(filepath.Join(sourceDir, "setup.py")))
		require.NoError(t, createFile(filepath.Join(sourceDir, "MANIFEST.in")))

		err = validateFrappeAppStructure(sourceDir, explicitComplexAppName)
		assert.NoError(t, err)
	})
}

// --- New tests for package command derivation and overrides ---

func createMockGitConfig(t *testing.T, baseDir, remoteURL string) {
	t.Helper()
	gitDir := filepath.Join(baseDir, ".git")
	require.NoError(t, os.MkdirAll(gitDir, 0755), "Failed to create .git directory")
	configContent := fmt.Sprintf("[remote \"origin\"]\n\turl = %s\n", remoteURL)
	require.NoError(t, os.WriteFile(filepath.Join(gitDir, "config"), []byte(configContent), 0644), "Failed to write .git/config")
}

func createMockHooksFile(t *testing.T, appModuleDir, appNameVarContent string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(appModuleDir, 0755), "Failed to create app module directory for hooks.py: "+appModuleDir)
	hooksContent := ""
	if appNameVarContent != "" {
		hooksContent = fmt.Sprintf("app_name = \"%s\"\n", appNameVarContent)
	}
	require.NoError(t, os.WriteFile(filepath.Join(appModuleDir, "hooks.py"), []byte(hooksContent), 0644), "Failed to write hooks.py")
}

func readMetadataFromFpm(t *testing.T, fpmFilePath string) (*metadata.AppMetadata, error) {
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

func TestPackageCmd_DerivationAndOverrides(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration-style test for 'package' command in short mode.")
	}

	// Helper to reset flags on packageCmd before each execution in subtests
	// Moved to package level for use by multiple test functions.
	// resetPackageCmdFlags := func() {
	// 	packageCmd.Flags().VisitAll(func(f *pflag.Flag) {
	// 		f.Value.Set(f.DefValue)
	// 		f.Changed = false
	// 	})
	// }

	t.Run("derivation success no flags", func(t *testing.T) {
		// resetPackageCmdFlags() // Call the package-level helper
		baseTestDir := t.TempDir()
		sourceDirName := "testapp_src_hooks" // Directory that will be source
		sourceDir := filepath.Join(baseTestDir, sourceDirName)
		// hooks.py should be in sourceDir/sourceDirName/hooks.py for initial detection
		appModuleDirForHooks := filepath.Join(sourceDir, sourceDirName)
		outputDir := filepath.Join(baseTestDir, "output_fpm_hooks")
		require.NoError(t, os.MkdirAll(appModuleDirForHooks, 0755))
		require.NoError(t, os.MkdirAll(outputDir, 0755))

		createMockGitConfig(t, sourceDir, "git@github.com:test-derived-org/test-derived-repo.git")
		createMockHooksFile(t, appModuleDirForHooks, "app_from_hooks")

		// validateFrappeAppStructure will check based on the final app_name, "app_from_hooks"
		appValidateDir := filepath.Join(sourceDir, "app_from_hooks")
		require.NoError(t, os.MkdirAll(appValidateDir, 0755))
		for _, fname := range []string{"__init__.py", "hooks.py", "modules.txt"} {
			require.NoError(t, os.WriteFile(filepath.Join(appValidateDir, fname), []byte(""), 0644))
		}

		cmdArgs := []string{"package", "--version", "0.0.1", "--output-path", outputDir, sourceDir}
		rootCmd.SetArgs(cmdArgs)

		oldStdout := os.Stdout; oldStderr := os.Stderr
		rOut, wOut, _ := os.Pipe(); rErr, wErr, _ := os.Pipe()
		os.Stdout = wOut; os.Stderr = wErr
		executeErr := rootCmd.Execute()
		wOut.Close(); wErr.Close()
		outBytes, _ := io.ReadAll(rOut); errBytes, _ := io.ReadAll(rErr)
		os.Stdout = oldStdout; os.Stderr = oldStderr
		t.Logf("Captured Stdout:\n%s", string(outBytes))
		t.Logf("Captured Stderr:\n%s", string(errBytes))

		require.NoError(t, executeErr, "fpm package command failed")
		expectedFpmFilePath := filepath.Join(outputDir, "app_from_hooks-0.0.1.fpm")
		assert.FileExists(t, expectedFpmFilePath)
		meta, err := readMetadataFromFpm(t, expectedFpmFilePath)
		require.NoError(t, err)
		assert.Equal(t, "test-derived-org", meta.Org)
		assert.Equal(t, "app_from_hooks", meta.AppName)
		assert.Equal(t, "app_from_hooks", meta.PackageName)
		assert.Equal(t, "0.0.1", meta.PackageVersion)
	})

	t.Run("derivation fallback appname from git", func(t *testing.T) {
		resetPackageCmdFlags()
		baseTestDir := t.TempDir()
		sourceDirName := "gitapp_src_fallback"
		sourceDir := filepath.Join(baseTestDir, sourceDirName)
		appModuleDirForHooksCreate := filepath.Join(sourceDir, sourceDirName)
		outputDir := filepath.Join(baseTestDir, "output_fpm_git_fallback")
		require.NoError(t, os.MkdirAll(appModuleDirForHooksCreate, 0755))
		require.NoError(t, os.MkdirAll(outputDir, 0755))

		createMockGitConfig(t, sourceDir, "git@github.com:test-org/actual-repo-name.git")
		createMockHooksFile(t, appModuleDirForHooksCreate, "") // No app_name in hooks.py

		appValidateDir := filepath.Join(sourceDir, "actual-repo-name")
		require.NoError(t, os.MkdirAll(appValidateDir, 0755))
		for _, fname := range []string{"__init__.py", "hooks.py", "modules.txt"} {
			require.NoError(t, os.WriteFile(filepath.Join(appValidateDir, fname), []byte(""), 0644))
		}

		cmdArgs := []string{"package", "--version", "0.0.2", "--output-path", outputDir, sourceDir}
		rootCmd.SetArgs(cmdArgs)
		executeErr := rootCmd.Execute()
		require.NoError(t, executeErr)
		expectedFpmFilePath := filepath.Join(outputDir, "actual-repo-name-0.0.2.fpm")
		assert.FileExists(t, expectedFpmFilePath)
		meta, err := readMetadataFromFpm(t, expectedFpmFilePath)
		require.NoError(t, err)
		assert.Equal(t, "test-org", meta.Org)
		assert.Equal(t, "actual-repo-name", meta.AppName)
	})

	t.Run("flag overrides derivation", func(t *testing.T) {
		resetPackageCmdFlags()
		baseTestDir := t.TempDir()
		sourceDirName := "override_src_flags"
		sourceDir := filepath.Join(baseTestDir, sourceDirName)
		appModuleDirForHooksCreate := filepath.Join(sourceDir, sourceDirName)
		outputDir := filepath.Join(baseTestDir, "output_fpm_override_flags")
		require.NoError(t, os.MkdirAll(appModuleDirForHooksCreate, 0755))
		require.NoError(t, os.MkdirAll(outputDir, 0755))

		createMockGitConfig(t, sourceDir, "git@github.com:derived-org/derived-repo.git")
		createMockHooksFile(t, appModuleDirForHooksCreate, "derived-app-hooks")

		appValidateDir := filepath.Join(sourceDir, "flag-app")
		require.NoError(t, os.MkdirAll(appValidateDir, 0755))
		for _, fname := range []string{"__init__.py", "hooks.py", "modules.txt"} {
			require.NoError(t, os.WriteFile(filepath.Join(appValidateDir, fname), []byte(""), 0644))
		}

		cmdArgs := []string{"package", "--version", "0.0.3", "--output-path", outputDir,
			"--org", "flag-org", "--app-name", "flag-app", sourceDir}
		rootCmd.SetArgs(cmdArgs)
		executeErr := rootCmd.Execute()
		require.NoError(t, executeErr)
		expectedFpmFilePath := filepath.Join(outputDir, "flag-app-0.0.3.fpm")
		assert.FileExists(t, expectedFpmFilePath)
		meta, err := readMetadataFromFpm(t, expectedFpmFilePath)
		require.NoError(t, err)
		assert.Equal(t, "flag-org", meta.Org)
		assert.Equal(t, "flag-app", meta.AppName)
	})

	t.Run("derivation fails no appname flag error", func(t *testing.T) {
		resetPackageCmdFlags()
		baseTestDir := t.TempDir()
		sourceDirName := "fail_derive_src_no_flag"
		sourceDir := filepath.Join(baseTestDir, sourceDirName)
		appModuleDirForHooks := filepath.Join(sourceDir, sourceDirName)
		outputDir := filepath.Join(baseTestDir, "output_fpm_fail_derive")
		require.NoError(t, os.MkdirAll(appModuleDirForHooks, 0755))
		require.NoError(t, os.MkdirAll(outputDir, 0755))

		createMockHooksFile(t, appModuleDirForHooks, "") // Empty hooks.py, no git config

		cmdArgs := []string{"package", "--version", "0.0.4", "--output-path", outputDir, sourceDir}
		rootCmd.SetArgs(cmdArgs)
		executeErr := rootCmd.Execute()
		assert.Error(t, executeErr)
		if executeErr != nil {
			assert.Contains(t, executeErr.Error(), "app_name could not be determined")
		}
	})

	t.Run("appname flag provided org derived", func(t *testing.T) {
		resetPackageCmdFlags()
		baseTestDir := t.TempDir()
		sourceDirName := "appname_flag_org_derived_src"
		sourceDir := filepath.Join(baseTestDir, sourceDirName)
		// Derivation of app_name from hooks uses initial guess for module path.
		// Initial guess is basename of sourceDir.
		appModuleDirForHooksCreate := filepath.Join(sourceDir, sourceDirName)
		outputDir := filepath.Join(baseTestDir, "output_fpm_appname_flag_org_derived")
		require.NoError(t, os.MkdirAll(appModuleDirForHooksCreate, 0755))
		require.NoError(t, os.MkdirAll(outputDir, 0755))

		createMockGitConfig(t, sourceDir, "git@github.com:derived-org-for-flagtest/some-repo.git")
		createMockHooksFile(t, appModuleDirForHooksCreate, "appname_in_hooks_ignored_by_flag")

		// validateFrappeAppStructure uses the final app_name from the flag
		appValidateDir := filepath.Join(sourceDir, "flag-appname")
		require.NoError(t, os.MkdirAll(appValidateDir, 0755))
		for _, fname := range []string{"__init__.py", "hooks.py", "modules.txt"} {
			require.NoError(t, os.WriteFile(filepath.Join(appValidateDir, fname), []byte(""), 0644))
		}

		cmdArgs := []string{"package", "--version", "0.0.5", "--output-path", outputDir,
			"--app-name", "flag-appname", sourceDir}
		rootCmd.SetArgs(cmdArgs)
		executeErr := rootCmd.Execute()
		require.NoError(t, executeErr)
		expectedFpmFilePath := filepath.Join(outputDir, "flag-appname-0.0.5.fpm")
		assert.FileExists(t, expectedFpmFilePath)
		meta, err := readMetadataFromFpm(t, expectedFpmFilePath)
		require.NoError(t, err)
		assert.Equal(t, "derived-org-for-flagtest", meta.Org)
		assert.Equal(t, "flag-appname", meta.AppName)
	})
}

// testAppParams holds parameters for creating a test Frappe app for new tests
// Re-evaluating if this struct is needed or if existing helpers are enough.
// The existing tests use direct setup. Let's try to adapt.
// For simplicity, I'll define a focused helper for the new tests if needed,
// or structure them similar to TestPackageCmd_DerivationAndOverrides.

// Helper to run package command and return metadata
// This simplifies running the command and then immediately getting metadata.
func runPackageAndGetMeta(t *testing.T, sourceDir string, appName string, version string, pkgType string, extraArgs ...string) (*metadata.AppMetadata, string) {
	t.Helper()
	resetPackageCmdFlags() // Ensure flags are clean for each run
	outputDir := t.TempDir()

	cmdArgs := []string{"package", "--version", version, "--output-path", outputDir}
	if appName != "" {
		cmdArgs = append(cmdArgs, "--app-name", appName)
	}
	if pkgType != "" {
		cmdArgs = append(cmdArgs, "--package-type", pkgType)
	}
	cmdArgs = append(cmdArgs, extraArgs...)
	cmdArgs = append(cmdArgs, sourceDir) // sourceDir is the last argument

	rootCmd.SetArgs(cmdArgs)

	// Capture stdout/stderr for debugging if needed
	oldStdout, oldStderr := os.Stdout, os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout = wOut
	os.Stderr = wErr
	executeErr := rootCmd.Execute()
	wOut.Close()
	wErr.Close()
	outBytes, _ := io.ReadAll(rOut)
	errBytes, _ := io.ReadAll(rErr)
	os.Stdout = oldStdout
	os.Stderr = oldStderr

	t.Logf("Package command stdout:\n%s", string(outBytes))
	if executeErr != nil {
		t.Logf("Package command stderr:\n%s", string(errBytes))
		t.Fatalf("fpm package command failed: %v", executeErr)
	}
	require.NoError(t, executeErr, "fpm package command failed")

	// Determine appName for filename construction (app_name from flag, or inferred from source dir if flag not used)
	// The actual app name used for the .fpm file name and metadata.PackageName
	// is determined by complex logic (flag > hooks > git > dir).
	// For these tests, we typically provide --app-name.
	finalAppName := appName
	if finalAppName == "" { // If --app-name not provided, it's harder to predict filename here without replicating logic.
		// Fallback to predicting from sourceDir, but this might not match if hooks/git changed it.
		// This part is tricky. For robust testing, we should probably try to find the generated .fpm file.
		// Or, ensure --app-name is always passed in tests for predictability.
		// For now, assume appName is passed if predictability is key for filename.
		// If appName is not passed, the command might succeed but we might not find the file easily.
		// Let's assume `appName` arg to this helper is the one that will be in the filename.
		if appName == "" { // If appName is not passed to helper, this will fail.
			t.Fatal("appName must be provided to runPackageAndGetMeta for predictable FPM filename")
		}
	}


	fpmFileName := fmt.Sprintf("%s-%s.fpm", finalAppName, version)
	expectedFpmFilePath := filepath.Join(outputDir, fpmFileName)
	require.FileExists(t, expectedFpmFilePath, "Expected .fpm file was not created at %s", expectedFpmFilePath)

	meta, err := readMetadataFromFpm(t, expectedFpmFilePath)
	require.NoError(t, err, "Failed to read metadata from FPM package")
	return meta, expectedFpmFilePath
}

// createMinimalFrappeApp creates a very basic app structure for testing.
// App name is the directory name.
func createMinimalFrappeApp(t *testing.T, baseDir string, appName string, files map[string]string) string {
	t.Helper()
	sourceDir := filepath.Join(baseDir, appName+"_source") // Unique source dir name
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


func TestPackageSourceControlURL(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration-style test in short mode.")
	}
	baseTestDir := t.TempDir()
	appName := "test_git_app"
	version := "0.0.1"
	gitRemoteURL := "https://github.com/test_org/test_repo.git"

	sourceDir := createMinimalFrappeApp(t, baseTestDir, appName, nil)
	createMockGitConfig(t, sourceDir, gitRemoteURL) // This creates .git/config

	// Need to init and commit for GetFullGitRemoteOriginURL to work as it might read from actual git commands or HEAD
	cmdInGitRepo := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = sourceDir
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "git command %v failed in %s: %s", args, sourceDir, string(output))
	}
	cmdInGitRepo("init")
	cmdInGitRepo("add", ".")
	cmdInGitRepo("config", "user.email", "test@example.com")
	cmdInGitRepo("config", "user.name", "Test User")
	cmdInGitRepo("commit", "-m", "initial commit")


	meta, _ := runPackageAndGetMeta(t, sourceDir, appName, version, "" /* pkgType */)
	assert.Equal(t, gitRemoteURL, meta.SourceControlURL)
}

func TestPackageTypeFlag(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration-style test in short mode.")
	}
	baseTestDir := t.TempDir()
	appName := "test_pkg_type_app"
	version := "1.0.0"
	sourceDir := createMinimalFrappeApp(t, baseTestDir, appName, nil)

	testCases := []struct {
		name            string
		packageTypeFlag string
		expectedType    string
	}{
		{"dev type", "dev", "dev"},
		{"prod type", "prod", "prod"},
		{"default type (prod)", "", "prod"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			meta, _ := runPackageAndGetMeta(t, sourceDir, appName, version, tc.packageTypeFlag)
			assert.Equal(t, tc.expectedType, meta.PackageType)
		})
	}
}

// inspectFPM opens the FPM file and allows assertions on its contents.
// It provides a map of file names to their zip.File struct and the zip.ReadCloser itself.
func inspectFPM(t *testing.T, fpmPath string, checkFn func(filesInArchive map[string]*zip.File, r *zip.ReadCloser)) {
	t.Helper()
	r, err := zip.OpenReader(fpmPath)
	require.NoError(t, err, "Failed to open .fpm file %s", fpmPath)
	defer r.Close()

	filesInArchive := make(map[string]*zip.File)
	for _, f := range r.File {
		filesInArchive[f.Name] = f
	}
	checkFn(filesInArchive, r)
}

// resetPackageCmdFlags resets all flags for packageCmd to their default values.
// This is important for running packageCmd multiple times in tests.
func resetPackageCmdFlags() {
	packageCmd.Flags().VisitAll(func(f *pflag.Flag) {
		// For some flag types, Value.Set might not correctly reset to default if the
		// default value string is complex (e.g. for slices or maps).
		// The most reliable way to reset pflag values to their true defaults is often
		// to re-initialize them or use specific methods if available.
		// However, for string, bool, int flags, f.Value.Set(f.DefValue) is usually fine.
		f.Value.Set(f.DefValue)
		f.Changed = false
	})
	// Also reset global variables bound to flags if they are not reset by the above.
	// For example, packageSourcePath, packageOutputPath, packageVersion, packageOverwrite, packageType
	// are global variables in cmd/package.go. These need to be reset manually if they are directly used.
	// The runPackageAndGetMeta helper re-sets them by not relying on global state but passing params.
	// The TestPackageCmd_DerivationAndOverrides test explicitly calls packageCmd.Flags().Set for overrides.
	// The `resetPackageCmdFlags` in TestPackageCmd_DerivationAndOverrides was scoped locally.
	// Making it package-level means it can be used by runPackageAndGetMeta too.
	// The global variables bound to flags in package.go's init() are:
	// packageOutputPath, packageVersion, packageOverwrite, packageType (for --package-type)
	// And String flags for "org", "app-name".
	// The `packageCmd.Flags().VisitAll` should handle resetting these at the flagset level.
	// The variables themselves if modified directly would need manual reset, but cobra usually works via flags.
}

// executeCommand is a helper to execute Cobra commands and capture their output.
// Copied/adapted from repo_test.go for use here.
func executeCommand(root *cobra.Command, args ...string) (string, error) {
	buf := new(bytes.Buffer)
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs(args)

	err := root.Execute()
	output := buf.String()
	return output, err
}

// verifyAppInLocalStore checks if an app is correctly installed in the local FPM store.
func verifyAppInLocalStore(t *testing.T, appsBasePath, org, appName, version string, expectExists bool, expectedFiles ...string) {
	t.Helper()
	appVersionPath := filepath.Join(appsBasePath, org, appName, version)

	_, err := os.Stat(appVersionPath)
	if expectExists {
		require.NoError(t, err, "App version path %s should exist in local store", appVersionPath)
		require.DirExists(t, appVersionPath, "App version path %s should be a directory", appVersionPath)

		for _, expectedFileRelPath := range expectedFiles {
			fullExpectedFilePath := filepath.Join(appVersionPath, expectedFileRelPath)
			assert.FileExists(t, fullExpectedFilePath, "Expected file %s not found in local store at %s", expectedFileRelPath, fullExpectedFilePath)
		}
	} else {
		assert.True(t, os.IsNotExist(err), "App version path %s should NOT exist in local store, but it does (or other error: %v)", appVersionPath, err)
	}
}


func TestPackageCmd_LocalInstallBehavior(t *testing.T) {
	origHome, homeSet := os.LookupEnv("HOME")
	if runtime.GOOS == "windows" {
		origHome, homeSet = os.LookupEnv("USERPROFILE")
	}

	tempHomeForTest, err := os.MkdirTemp("", "fpm-testhome-pkglocal-*")
	require.NoError(t, err)
	defer func() {
		if homeSet {
			if runtime.GOOS == "windows" {
				os.Setenv("USERPROFILE", origHome)
			} else {
				os.Setenv("HOME", origHome)
			}
		} else {
			if runtime.GOOS == "windows" {
				os.Unsetenv("USERPROFILE")
			} else {
				os.Unsetenv("HOME")
			}
		}
		os.RemoveAll(tempHomeForTest)
	}()

	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", tempHomeForTest)
	} else {
		t.Setenv("HOME", tempHomeForTest)
	}

	// This call to InitConfig ensures that if a default config is created,
	// it's within tempHomeForTest. The AppsBasePath will be derived based on this home.
	cfg, err := config.InitConfig()
	require.NoError(t, err, "Failed to init config in temp home for package local install test")
	mockAppsBasePath := cfg.AppsBasePath // Use the AppsBasePath from the initialized config
	// Ensure this base path itself is created, as InitConfig might only create the .fpm dir, not .fpm/apps
	require.NoError(t, os.MkdirAll(mockAppsBasePath, 0o755))


	sourceAppDir, err := os.MkdirTemp("", "sourceapp-pkglocal-*")
	require.NoError(t, err)
	defer os.RemoveAll(sourceAppDir)

	testAppName := "samplelocalapp"
	testAppVersion := "0.0.1"
	testAppOrg := "testorg"

	// Using the existing createMinimalFrappeApp from this file (package_test.go)
	// It creates source structure like: <sourceAppDir>/<testAppName>/hooks.py
	// This is correct for `fpm package <sourceAppDir> --app-name <testAppName>`
	createMinimalFrappeApp(t, sourceAppDir, testAppName, map[string]string{
		"requirements.txt": "requests", // A root file
	})


	t.Run("DefaultInstallsToLocalStore", func(t *testing.T) {
		packageOutputDir, err := os.MkdirTemp("", "fpmoutput-defaultinstall-*")
		require.NoError(t, err)
		defer os.RemoveAll(packageOutputDir)

		// Reset flags for packageCmd
		packageCmd.Flags().VisitAll(func(f *pflag.Flag) { f.Value.Set(f.DefValue); f.Changed = false; })
		// Manually reset package-level flag variables to their defaults
		packageSkipLocalInstall = false


		args := []string{
			"package", sourceAppDir,
			"--output-path", packageOutputDir,
			"--version", testAppVersion,
			"--org", testAppOrg,
			"--app-name", testAppName,
		}
		output, err := executeCommand(rootCmd, args...)
		t.Logf("fpm package (default install) output: %s", output)
		require.NoError(t, err, "fpm package command failed for default local install")

		packagedFPMFile := filepath.Join(packageOutputDir, testAppName+"-"+testAppVersion+".fpm")
		assert.FileExists(t, packagedFPMFile, "Packaged .fpm file not found")

		expectedFilesInStore := []string{
			filepath.Join(testAppName, "hooks.py"), // App module files
			filepath.Join(testAppName, "__init__.py"),
			filepath.Join(testAppName, "modules.txt"),
			"requirements.txt",      // Root files from package
			"app_metadata.json",     // Metadata file
		}
		verifyAppInLocalStore(t, mockAppsBasePath, testAppOrg, testAppName, testAppVersion, true, expectedFilesInStore...)
	})

	t.Run("SkipLocalInstallFlag", func(t *testing.T) {
		packageOutputDir, err := os.MkdirTemp("", "fpmoutput-skipinstall-*")
		require.NoError(t, err)
		defer os.RemoveAll(packageOutputDir)

		// Clean the local store path to ensure it's not from a previous run
		appVersionPathInStore := filepath.Join(mockAppsBasePath, testAppOrg, testAppName, testAppVersion)
		os.RemoveAll(appVersionPathInStore)


		// Reset flags for packageCmd
		packageCmd.Flags().VisitAll(func(f *pflag.Flag) { f.Value.Set(f.DefValue); f.Changed = false; })
		// Manually reset package-level flag variables
		packageSkipLocalInstall = false // Reset before setting for this test specifically


		args := []string{
			"package", sourceAppDir,
			"--output-path", packageOutputDir,
			"--version", testAppVersion,
			"--org", testAppOrg,
			"--app-name", testAppName,
			"--skip-local-install", // Crucial flag for this test
		}
		output, err := executeCommand(rootCmd, args...)
		t.Logf("fpm package (--skip-local-install) output: %s", output)
		require.NoError(t, err, "fpm package command failed with --skip-local-install")

		packagedFPMFile := filepath.Join(packageOutputDir, testAppName+"-"+testAppVersion+".fpm")
		assert.FileExists(t, packagedFPMFile, "Packaged .fpm file not found with --skip-local-install")

		verifyAppInLocalStore(t, mockAppsBasePath, testAppOrg, testAppName, testAppVersion, false)
		assert.Contains(t, output, "Skipping installation to local FPM app store.")
	})
}


func TestProductionExclusions(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration-style test in short mode.")
	}
	baseTestDir := t.TempDir()
	appName := "sample_exclusion_app" // Renamed to avoid conflict with /test_* pattern
	version := "1.0.0"

	// Files that should always be included (unless .fpmignore says otherwise, not tested here)
	alwaysIncludeFiles := map[string]string{
		filepath.Join(appName, "main.py"):      "print('hello')",
		filepath.Join(appName, "module_utils/util.py"): "def helper(): pass",
		"assets/important_asset.txt":         "important data",
	}

	// Files that should be excluded only in production mode
	prodOnlyExcludeFiles := map[string]string{
		".git/config":                                     "[remote \"origin\"]\nurl = someurl",
		filepath.Join(appName, "__pycache__/some.pyc"):          "bytecode",
		filepath.Join(appName, "tests/test_main.py"):            "import main",
		filepath.Join(appName, "test_specific_feature.py"): "def test_feat(): pass",
		// A file directly in app module matching test*
		filepath.Join(appName, "test_another.py"): "test code",
		// A top-level file/dir matching test*
		"test_data/data.json": `{"key":"value"}`,
	}

	// Create all files for the source directory
	allFilesForSource := make(map[string]string)
	for k, v := range alwaysIncludeFiles { allFilesForSource[k] = v }
	for k, v := range prodOnlyExcludeFiles { allFilesForSource[k] = v }

	sourceDir := createMinimalFrappeApp(t, baseTestDir, appName, allFilesForSource)
	// createMinimalFrappeApp already creates appName/__init__.py, hooks.py, modules.txt

	t.Run("prod mode applies exclusions", func(t *testing.T) {
		meta, fpmPath := runPackageAndGetMeta(t, sourceDir, appName, version, "prod")
		assert.Equal(t, "prod", meta.PackageType)

		inspectFPM(t, fpmPath, func(filesInArchive map[string]*zip.File, r *zip.ReadCloser) {
			for relPath := range alwaysIncludeFiles {
				normalizedPath := filepath.ToSlash(relPath)
				assert.Contains(t, filesInArchive, normalizedPath, "Expected file '%s' to be present in PROD archive", normalizedPath)
			}
			for relPath := range prodOnlyExcludeFiles {
				normalizedPath := filepath.ToSlash(relPath)
				assert.NotContains(t, filesInArchive, normalizedPath, "Expected file '%s' to be ABSENT in PROOD archive", normalizedPath)
			}
			// Check standard app files also present
			assert.Contains(t, filesInArchive, filepath.ToSlash(filepath.Join(appName, "__init__.py")))
		})
	})

	t.Run("dev mode does not apply prod exclusions", func(t *testing.T) {
		meta, fpmPath := runPackageAndGetMeta(t, sourceDir, appName, version, "dev")
		assert.Equal(t, "dev", meta.PackageType)

		inspectFPM(t, fpmPath, func(filesInArchive map[string]*zip.File, r *zip.ReadCloser) {
			for relPath := range alwaysIncludeFiles {
				normalizedPath := filepath.ToSlash(relPath)
				assert.Contains(t, filesInArchive, normalizedPath, "Expected file '%s' to be present in DEV archive", normalizedPath)
			}
			// In dev mode, production-specific exclusions should NOT be applied.
			// However, defaultIgnorePatterns like .git/ still apply.
			// The productionExclusionPatterns are: .git, __pycache__, *.pyc, test*, tests
			// Default ignores are: .git/, *.pyc, __pycache__/, .DS_Store, etc.
			// So .git/, __pycache__, *.pyc will be excluded in BOTH dev and prod due to default + prod specific overlap.
			// Only test* and tests/ are unique to prod exclusions.

			for relPath := range prodOnlyExcludeFiles {
				normalizedPath := filepath.ToSlash(relPath)
				isActuallyExcludedByDefaultForDev := strings.HasPrefix(normalizedPath, ".git/") ||
				                                   strings.Contains(normalizedPath, "__pycache__/") ||
				                                   strings.HasSuffix(normalizedPath, ".pyc")

				if isActuallyExcludedByDefaultForDev {
					assert.NotContains(t, filesInArchive, normalizedPath, "File '%s' should be ABSENT in DEV due to default ignores", normalizedPath)
				} else {
					// These are files like test_*.py or tests/* which are only excluded in prod.
					assert.Contains(t, filesInArchive, normalizedPath, "Expected file '%s' to be PRESENT in DEV archive", normalizedPath)
				}
			}
		})
	})
}

func TestArchiveStructure(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration-style test in short mode.")
	}
	baseTestDir := t.TempDir()
	appName := "sample_structure_app" // Renamed to avoid conflict
	version := "0.1.0"

	files := map[string]string{
		filepath.Join(appName, "models/item.py"): "class Item:",
		"assets/css/style.css":                 ".body {}",
		"requirements.txt":                     "frappe-sdk",
		// Ensure a file that might be accidentally put in app_source by old logic is tested
		"file_at_root.txt": "should be at root",
	}
	sourceDir := createMinimalFrappeApp(t, baseTestDir, appName, files)

	_, fpmPath := runPackageAndGetMeta(t, sourceDir, appName, version, "prod" /* any type */)

	expectedPathsInArchive := []string{
		"app_metadata.json",
		filepath.ToSlash(filepath.Join(appName, "models/item.py")),
		"assets/css/style.css",
		"requirements.txt",
		"file_at_root.txt",
		filepath.ToSlash(filepath.Join(appName, "__init__.py")),
		filepath.ToSlash(filepath.Join(appName, "hooks.py")),
		filepath.ToSlash(filepath.Join(appName, "modules.txt")),
	}

	inspectFPM(t, fpmPath, func(filesInArchive map[string]*zip.File, r *zip.ReadCloser) {
		for _, expectedPath := range expectedPathsInArchive {
			assert.Contains(t, filesInArchive, expectedPath, "Expected file '%s' to be in archive at root level", expectedPath)
		}

		// Explicitly check no app_source directory
		foundAppSourceDir := false
		foundAppSourceFile := false
		for pathInArchive := range filesInArchive {
			if strings.HasPrefix(pathInArchive, "app_source/") {
				foundAppSourceDir = true // Found something that looks like the old dir
				if pathInArchive == filepath.ToSlash(filepath.Join("app_source", appName, "models/item.py")) {
					foundAppSourceFile = true
				}
			}
		}
		assert.False(t, foundAppSourceDir, "No files or directories should be under 'app_source/'")
		assert.False(t, foundAppSourceFile, "File 'app_source/%s/models/item.py' should not exist", appName)
	})
}

func TestContentChecksum(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration-style test in short mode.")
	}
	baseTestDir := t.TempDir()
	appName := "sample_checksum_app" // Renamed to avoid conflict
	version := "1.0.0" // Base version

	initialFileRelPath := filepath.Join(appName, "file_to_check.txt") // Relative to sourceDir
	initialContent := "original checksum content"
	modifiedContent := "new checksum content"

	// Create initial app state
	sourceDir := createMinimalFrappeApp(t, baseTestDir, appName, map[string]string{
		initialFileRelPath:                   initialContent,
		filepath.Join(appName, "another.py"): "some consistent code",
	})

	// Package 1: Initial content
	meta1, fpmPath1 := runPackageAndGetMeta(t, sourceDir, appName, version, "prod")
	require.NotEmpty(t, meta1.ContentChecksum, "ContentChecksum should not be empty (meta1)")
	t.Logf("Checksum 1 (initial): %s for version %s, path %s", meta1.ContentChecksum, version, fpmPath1)

	// Package 2: Modify a file and repackage.
	// We modify the file in the existing sourceDir.
	absInitialFilePath := filepath.Join(sourceDir, initialFileRelPath)
	require.NoError(t, os.WriteFile(absInitialFilePath, []byte(modifiedContent), 0644), "Failed to modify file for checksum test")

	versionMod := version + "-mod" // Use a different package version for clarity in logs/outputs
	meta2, fpmPath2 := runPackageAndGetMeta(t, sourceDir, appName, versionMod, "prod")
	require.NotEmpty(t, meta2.ContentChecksum, "ContentChecksum should not be empty (meta2)")
	t.Logf("Checksum 2 (modified): %s for version %s, path %s", meta2.ContentChecksum, versionMod, fpmPath2)
	assert.NotEqual(t, meta1.ContentChecksum, meta2.ContentChecksum, "ContentChecksum should change when file content changes.")

	// Package 3: Revert modification. Checksum should revert to original.
	require.NoError(t, os.WriteFile(absInitialFilePath, []byte(initialContent), 0644), "Failed to revert file for checksum test")
	versionRevert := version + "-revert"
	meta3, fpmPath3 := runPackageAndGetMeta(t, sourceDir, appName, versionRevert, "prod")
	require.NotEmpty(t, meta3.ContentChecksum, "ContentChecksum should not be empty (meta3)")
	t.Logf("Checksum 3 (reverted): %s for version %s, path %s", meta3.ContentChecksum, versionRevert, fpmPath3)
	assert.Equal(t, meta1.ContentChecksum, meta3.ContentChecksum, "ContentChecksum should revert to original when content is reverted.")

	// Package 4: Package the original content again, but with a different package version in app_metadata.json.
	// The content checksum should remain the same as meta1, because app_metadata.json (which would contain
	// the different package version) is ignored during checksum calculation.
	versionDifferentMeta := version + "-diffmeta"
	// sourceDir currently has initialFileRelPath with initialContent.
	meta4, fpmPath4 := runPackageAndGetMeta(t, sourceDir, appName, versionDifferentMeta, "prod")
	require.NotEmpty(t, meta4.ContentChecksum, "ContentChecksum should not be empty (meta4)")
	t.Logf("Checksum 4 (diffmeta): %s for version %s, path %s", meta4.ContentChecksum, versionDifferentMeta, fpmPath4)

	// Verify meta4 from FPM (after packaging) has the new version, but ContentChecksum matches original (meta1)
	// This uses the readMetadataFromFpm helper defined in this test file.
	fpmMeta4Data, err := readMetadataFromFpm(t, fpmPath4)
	require.NoError(t, err, "Failed to read metadata from FPM for meta4 check")
	assert.Equal(t, versionDifferentMeta, fpmMeta4Data.PackageVersion, "PackageVersion in app_metadata.json should be the new one.")
	assert.Equal(t, meta1.ContentChecksum, fpmMeta4Data.ContentChecksum, "ContentChecksum in app_metadata.json should match original (meta1) despite other metadata changes.")
	// Also check against meta4.ContentChecksum which was derived directly from runPackageAndGetMeta
	assert.Equal(t, meta1.ContentChecksum, meta4.ContentChecksum, "ContentChecksum from runPackageAndGetMeta should match original (meta1).")


	// Package 5: Add a new file. Checksum should change.
	newFileRelPath := filepath.Join(appName, "newly_added_file.txt")
	absNewFilePath := filepath.Join(sourceDir, newFileRelPath)
	require.NoError(t, os.WriteFile(absNewFilePath, []byte("new file data"), 0644))
	versionAddedFile := version + "-addfile"
	meta5, fpmPath5 := runPackageAndGetMeta(t, sourceDir, appName, versionAddedFile, "prod")
	require.NotEmpty(t, meta5.ContentChecksum, "ContentChecksum should not be empty (meta5)")
	t.Logf("Checksum 5 (file added): %s for version %s, path %s", meta5.ContentChecksum, versionAddedFile, fpmPath5)
	assert.NotEqual(t, meta1.ContentChecksum, meta5.ContentChecksum, "ContentChecksum should change when a new file is added.")
	// Cleanup the added file for subsequent tests in this function
	require.NoError(t, os.Remove(absNewFilePath))


	// Package 6: Rename a file. Checksum should change.
	// Ensure back to original state first (no newFileRelPath, initialFileRelPath has initialContent)
	// This is already the state as newFileRelPath was removed, and initialFileRelPath content is initialContent.
	renamedFileRelPath := filepath.Join(appName, "renamed_" + filepath.Base(initialFileRelPath))
	absRenamedFilePath := filepath.Join(sourceDir, renamedFileRelPath)
	require.NoError(t, os.Rename(absInitialFilePath, absRenamedFilePath)) // Rename initialFileRelPath

	versionRenamedFile := version + "-renamed"
	meta6, fpmPath6 := runPackageAndGetMeta(t, sourceDir, appName, versionRenamedFile, "prod")
	require.NotEmpty(t, meta6.ContentChecksum, "ContentChecksum should not be empty (meta6)")
	t.Logf("Checksum 6 (file renamed): %s for version %s, path %s", meta6.ContentChecksum, versionRenamedFile, fpmPath6)
	assert.NotEqual(t, meta1.ContentChecksum, meta6.ContentChecksum, "ContentChecksum should change when a file is renamed.")

	// Restore file name for hygiene, though sourceDir is temp and will be cleaned up
	require.NoError(t, os.Rename(absRenamedFilePath, absInitialFilePath))
}
