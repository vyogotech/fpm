package cmd

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fpm/internal/metadata"

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
	resetPackageCmdFlags := func() {
		packageCmd.Flags().VisitAll(func(f *pflag.Flag) {
			f.Value.Set(f.DefValue)
			f.Changed = false
		})
	}

	t.Run("derivation success no flags", func(t *testing.T) {
		resetPackageCmdFlags()
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
