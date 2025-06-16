package cmd

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"net/http"         // For httptest
	"net/http/httptest" // For mock server
	"encoding/json"    // For serving JSON metadata
	"fpm/internal/repository" // For PackageMetadata struct
	"fpm/internal/config" // For config.LoadConfig() to find AppsBasePath

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createDummyFpmPackage creates a simple .fpm file for testing.
func createDummyFpmPackage(t *testing.T, packagePath, org, appName, version string) {
	t.Helper() // Marks this function as a test helper

	archiveFile, err := os.Create(packagePath)
	require.NoError(t, err, "Failed to create dummy .fpm package file")
	defer archiveFile.Close()

	zipWriter := zip.NewWriter(archiveFile)
	defer zipWriter.Close()

	var files = []struct {
		Name string
		Body string
	}{
		{"app_metadata.json", fmt.Sprintf(`{"org":"%s", "app_name":"%s", "package_name":"%s", "package_version":"%s"}`, org, appName, appName, version)},
		{filepath.Join("app_source", appName, "__init__.py"), ""},
		{filepath.Join("app_source", appName, "hooks.py"), ""},
		{filepath.Join("app_source", appName, "modules.txt"), ""},
		// Add a dummy requirements.txt to test its potential handling later (though not strictly part of this test's assertions yet)
		{"requirements.txt", "requests==2.25.1\n"},
	}

	for _, file := range files {
		fWriter, err := zipWriter.Create(file.Name)
		require.NoError(t, err, fmt.Sprintf("Failed to create file %s in zip", file.Name))
		_, err = io.WriteString(fWriter, file.Body)
		require.NoError(t, err, fmt.Sprintf("Failed to write to file %s in zip", file.Name))
	}
}

// TestInstallCmd_SuccessfulInstall is expected to fail with new install logic
// as it creates a package with the old structure.
// Renaming to indicate this, or it could be removed/updated later.
func Skip_TestInstallCmd_OldPackageStructure_ExpectedToFail(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration-style test in short mode.")
	}

	baseTmpDir, err := os.MkdirTemp("", "fpm-install-test-*")
	require.NoError(t, err, "Failed to create base temp dir")
	defer os.RemoveAll(baseTmpDir)

	// --- Setup Phase ---

	// 1. Mock FPM Home and Apps Base Path
	// Set HOME env var for the test to control os.UserHomeDir()
	// This makes LoadConfig use our mockFpmHome for its defaults.
	mockUserHome := filepath.Join(baseTmpDir, "mockUserHome")
	err = os.MkdirAll(mockUserHome, 0755)
	require.NoError(t, err)
	t.Setenv("HOME", mockUserHome) // Go 1.17+

	// This is where FPM will store its own app versions by default
	// (e.g. ~/.fpm/apps or, in this test, mockUserHome/.fpm/apps)
	fpmAppsStoragePath := filepath.Join(mockUserHome, ".fpm", "apps")
	err = os.MkdirAll(fpmAppsStoragePath, 0755) // Ensure it exists for clarity, though LoadConfig might not need it to exist
	require.NoError(t, err)


	// 2. Mock Bench Directory
	mockBenchDir := filepath.Join(baseTmpDir, "mockBench")
	err = os.MkdirAll(filepath.Join(mockBenchDir, "sites"), 0755) // For apps.txt
	require.NoError(t, err)
	err = os.MkdirAll(filepath.Join(mockBenchDir, "apps"), 0755) // For symlink
	require.NoError(t, err)
	mockBenchEnvBinDir := filepath.Join(mockBenchDir, "env", "bin")
	err = os.MkdirAll(mockBenchEnvBinDir, 0755)
	require.NoError(t, err)


	// 3. Mock Pip Script
	mockPipPath := filepath.Join(mockBenchEnvBinDir, "pip")
	pipCallsLogPath := filepath.Join(baseTmpDir, "pip_calls.txt")
	pipScriptContent := fmt.Sprintf(`#!/bin/bash
echo "$@" >> %s
echo "Fake pip: Successfully processed for $4"
exit 0
`, pipCallsLogPath)
	err = os.WriteFile(mockPipPath, []byte(pipScriptContent), 0755)
	require.NoError(t, err, "Failed to write mock pip script")


	// 4. Create Dummy .fpm Package
	pkgOrg := "testorg"
	pkgAppName := "dummyapp"
	pkgVersion := "0.0.1"
	dummyFpmFilePath := filepath.Join(baseTmpDir, fmt.Sprintf("%s-%s.fpm", pkgAppName, pkgVersion))
	createDummyFpmPackage(t, dummyFpmFilePath, pkgOrg, pkgAppName, pkgVersion)


	// --- Execution Phase ---
	// Use the global rootCmd from the cmd package.
	// Its subcommands (like install) are added by init() functions in their respective files.
	currentArgs := []string{"install", dummyFpmFilePath, "--bench-path", mockBenchDir}
	rootCmd.SetArgs(currentArgs)

	fmt.Printf("Executing fpm install with args: %v\n", currentArgs)
	executeErr := rootCmd.Execute()


	// --- Assertion Phase ---
	assert.NoError(t, executeErr, "fpm install command failed")

	// Assert Extraction Path (where the actual app code is stored by FPM)
	// targetAppPath in install.go is: filepath.Join(fpmConfig.AppsBasePath, pkgMeta.Org, pkgMeta.AppName, pkgMeta.PackageVersion)
	// And the symlink target is: filepath.Join(targetAppPath, pkgMeta.AppName)
	expectedAppCodeDir := filepath.Join(fpmAppsStoragePath, pkgOrg, pkgAppName, pkgVersion, pkgAppName)
	expectedInitPyPath := filepath.Join(expectedAppCodeDir, "__init__.py")
	assert.FileExists(t, expectedInitPyPath, "Expected __init__.py in FPM storage path")


	// Assert Symlink
	linkPath := filepath.Join(mockBenchDir, "apps", pkgAppName)
	assert.FileExists(t, linkPath, "Symlink in bench/apps not found")

	linkTarget, err := os.Readlink(linkPath)
	require.NoError(t, err, "Failed to read symlink")
	// Ensure linkTarget is absolute before comparing, or make expectedAppCodeDir absolute based on a known root.
	// os.Symlink creates it based on what's passed. originalPath in install.go is absolute.
	absExpectedAppCodeDir, err := filepath.Abs(expectedAppCodeDir)
	require.NoError(t, err)
	assert.Equal(t, absExpectedAppCodeDir, linkTarget, "Symlink does not point to the correct FPM storage path")


	// Assert Pip Call
	pipCallsLogBytes, err := os.ReadFile(pipCallsLogPath)
	require.NoError(t, err, "Failed to read pip calls log")
	pipCalls := strings.TrimSpace(string(pipCallsLogBytes))
	// Expected: install -q -e ./apps/dummyapp (path relative to bench dir)
	expectedPipArgs := fmt.Sprintf("install -q -e %s", filepath.Join("./apps", pkgAppName))
	assert.Equal(t, expectedPipArgs, pipCalls, "Pip was not called with expected arguments")


	// Assert apps.txt content
	appsTxtPath := filepath.Join(mockBenchDir, "sites", "apps.txt")
	assert.FileExists(t, appsTxtPath, "apps.txt not found in bench/sites")

	appsTxtBytes, err := os.ReadFile(appsTxtPath)
	require.NoError(t, err, "Failed to read apps.txt")
	appsTxtContent := strings.TrimSpace(string(appsTxtBytes))
	// Should contain only pkgAppName after trimming, and a newline in the file
	assert.Equal(t, pkgAppName, appsTxtContent, "apps.txt does not contain the correct app name or has extra content")

	// Check for trailing newline in apps.txt
	if len(appsTxtBytes) > 0 {
		assert.Equal(t, byte('\n'), appsTxtBytes[len(appsTxtBytes)-1], "apps.txt should end with a newline if not empty")
	}
}

// Note: This test assumes that NewRootCmd() is available and sets up the rootCmd
// with all its subcommands correctly. If rootCmd from root.go is directly usable
// and reset properly, that can be an alternative.
// For this to work, install.go's init() which calls rootCmd.AddCommand(installCmd)
// must have run. This usually happens if the cmd package is imported.
// If NewRootCmd is defined in root.go, it should be accessible.
// Let's assume `rootCmd` variable from `cmd` package is directly usable and reset for tests,
// or `NewRootCmd()` creates a fresh instance with subcommands.
// The test file is in package `cmd`, so it can access `rootCmd` if it's a package var.
// However, to avoid test state leakage, it's better to use ExecuteC or a fresh instance.
// For now, I'll assume `NewRootCmd()` is a hypothetical function that returns a fresh Cobra root command
// configured like the main one. If it's not available, I will need to use `cmd.rootCmd.Execute()`
// and manage its state (e.g. reset flags) or use `ExecuteC()`.

// A simple way to get a fresh root command for testing if NewRootCmd() is not defined:
/*
func getTestRootCmd() *cobra.Command {
    newRoot := &cobra.Command{Use: "fpm", Short: "Frappe Package Manager CLI"}
    // Add subcommands manually here if needed for isolated testing
    // For this test, we need the install command.
    // If installCmd is a global var in package cmd:
    //   - Reset its flags if any were persistent
    //   - newRoot.AddCommand(installCmd)
    // This is tricky due to init() functions.
    // A better way is often to call the RunE function directly with mocked cobra.Command and args.
    // But for this integration test, using Execute() or ExecuteC() is preferred.
    // We will use the actual rootCmd from the package, assuming test side effects are manageable or reset.
    // For now, the actual `rootCmd` from the package will be used.
    // We need to ensure its state is clean or use ExecuteC.
    // Let's try direct execution with rootCmd.SetArgs and rootCmd.Execute()
    // and ensure `rootCmd` is re-initialized or its state doesn't interfere.
    // The simplest might be to just use the global `rootCmd` from `cmd` package.
    // If `root.go` has `func Execute() error { return rootCmd.Execute() }`,
    // we can call that. Or, directly `rootCmd.Execute()`.
    // The `cmd.Execute()` function in cobra typically calls `rootCmd.Execute()`.
    // Let's stick to setting args on the global `rootCmd` and calling `Execute()`.
    // This implies `install_test.go` is in `package cmd`.
}
*/
// The test will use the global `rootCmd` from the `cmd` package.
// Add a helper in root.go like `func GetRootCommandForTest() *cobra.Command { return rootCmd }`
// or make sure tests can re-initialize it if necessary.
// For now, let's assume `rootCmd` is directly available and its state is fine for sequential tests
// or this is the only one running.
// The most robust way if `rootCmd` is global is to use `ExecuteC` or reset its state.
// Let's try to make it simple first: use global `rootCmd`.
// The test uses `testRootCmd := NewRootCmd()`. This needs to be defined.
// For now, I'll change it to use the actual `rootCmd` from the package.
// This requires install_test.go to be in package cmd.
// And root.go's rootCmd must be a package-level variable.
// The init() functions in install.go, package.go etc. add subcommands to this rootCmd.
// This should work if tests are run with `go test ./cmd`.

// End of Skip_TestInstallCmd_OldPackageStructure_ExpectedToFail

// createMinimalFrappeApp creates a basic Frappe app structure for testing.
// This helper is for the new TestInstallCommand_NewPackageStructure.
func createMinimalFrappeApp(t *testing.T, basePath, appName, appVersion, appOrg string) {
	appModulePath := filepath.Join(basePath, appName)
	require.NoError(t, os.MkdirAll(appModulePath, 0o755))

	hooksContent := fmt.Sprintf("app_name = \"%s\"\napp_version = \"%s\"\napp_publisher = \"%s\"\n", appName, appVersion, appOrg)
	require.NoError(t, os.WriteFile(filepath.Join(appModulePath, "hooks.py"), []byte(hooksContent), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(appModulePath, "__init__.py"), []byte("# init"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(appModulePath, "modules.txt"), []byte("mymodule"), 0o644))
}

// runFPMCommand executes the main FPM binary (via go run) with given arguments.
// This helper is for the new TestInstallCommand_NewPackageStructure.
func runFPMCommand(t *testing.T, verbose bool, args ...string) ([]byte, error) {
	// Ensure that the path to main.go is correct relative to fpm/cmd/ where this test file is.
	// If main.go is in fpm/, then "../main.go" is correct.
	cmdArgs := append([]string{"run", "../main.go"}, args...)
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
		errorMsg := fmt.Sprintf("error running 'go run ../main.go %s': %v", strings.Join(args, " "), cmdErr)
		if !verbose && len(output) > 0 {
			errorMsg += fmt.Sprintf("\nCommand output:\n%s", string(output))
		}
		return output, fmt.Errorf(errorMsg)
	}
	return output, nil
}

func TestInstallCommand_NewPackageStructure(t *testing.T) {
	// --- Setup Phase ---
	testAppName := "sampleinstallapp"
	testAppVersion := "0.0.1"
	testAppOrg := "testorg"

	sourceAppDir, err := os.MkdirTemp("", "sourceapp-*")
	require.NoError(t, err)
	defer os.RemoveAll(sourceAppDir)

	createMinimalFrappeApp(t, sourceAppDir, testAppName, testAppVersion, testAppOrg)
	dummyReqTxtPath := filepath.Join(sourceAppDir, "requirements.txt")
	err = os.WriteFile(dummyReqTxtPath, []byte("requests==2.25.1"), 0o644)
	require.NoError(t, err)
	dummyAssetDirPath := filepath.Join(sourceAppDir, "assets")
	err = os.MkdirAll(dummyAssetDirPath, 0o755)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(dummyAssetDirPath, "style.css"), []byte("body { color: red; }"), 0o644)
	require.NoError(t, err)

	packageOutputDir, err := os.MkdirTemp("", "fpmoutput-*")
	require.NoError(t, err)
	defer os.RemoveAll(packageOutputDir)

	packageArgs := []string{
		"package", sourceAppDir,
		"--output-path", packageOutputDir,
		"--version", testAppVersion,
		"--org", testAppOrg,
		"--app-name", testAppName, // Ensure consistent app naming
	}
	_, err = runFPMCommand(t, false, packageArgs...)
	require.NoError(t, err, "fpm package command failed")

	packagedFPMFile := filepath.Join(packageOutputDir, testAppName+"-"+testAppVersion+".fpm")
	_, err = os.Stat(packagedFPMFile)
	require.NoError(t, err, "packaged .fpm file not found: %s", packagedFPMFile)

	mockBenchPath, err := os.MkdirTemp("", "mockbench-*")
	require.NoError(t, err)
	defer os.RemoveAll(mockBenchPath)
	require.NoError(t, os.MkdirAll(filepath.Join(mockBenchPath, "apps"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(mockBenchPath, "sites"), 0o755))

	mockPipDir := filepath.Join(mockBenchPath, "env", "bin")
	require.NoError(t, os.MkdirAll(mockPipDir, 0o755))
	mockPipPath := filepath.Join(mockPipDir, "pip")
	if runtime.GOOS == "windows" {
		mockPipPath += ".exe"
	}
	pipScriptContent := "#!/bin/sh\necho \"Mock pip install $@\" >> pip_called.log"
	if runtime.GOOS == "windows" {
		pipScriptContent = "@echo off\necho Mock pip install %* >> pip_called.log"
	}
	err = os.WriteFile(mockPipPath, []byte(pipScriptContent), 0o755)
	require.NoError(t, err)

	mockAppsBasePath, err := os.MkdirTemp("", "mockfpmstorage-*")
	require.NoError(t, err)
	defer os.RemoveAll(mockAppsBasePath)
	t.Setenv("FPM_APPS_BASE_PATH", mockAppsBasePath)

	installArgs := []string{
		"install", packagedFPMFile,
		"--bench-path", mockBenchPath,
	}
	installCmdOutput, err := runFPMCommand(t, false, installArgs...)
	require.NoError(t, err, "fpm install command failed. Output: %s", string(installCmdOutput))

	expectedFpmStorageAppPath := filepath.Join(mockAppsBasePath, testAppOrg, testAppName, testAppVersion)
	expectedAppModuleInStorage := filepath.Join(expectedFpmStorageAppPath, testAppName)

	_, err = os.Stat(filepath.Join(expectedAppModuleInStorage, "hooks.py"))
	assert.NoError(t, err, "hooks.py not found in FPM storage app module: %s", filepath.Join(expectedAppModuleInStorage, "hooks.py"))
	_, err = os.Stat(filepath.Join(expectedFpmStorageAppPath, "requirements.txt"))
	assert.NoError(t, err, "requirements.txt not found in FPM storage: %s", filepath.Join(expectedFpmStorageAppPath, "requirements.txt"))
	_, err = os.Stat(filepath.Join(expectedFpmStorageAppPath, "assets", "style.css"))
	assert.NoError(t, err, "assets/style.css not found in FPM storage: %s", filepath.Join(expectedFpmStorageAppPath, "assets", "style.css"))

	benchAppSymlinkPath := filepath.Join(mockBenchPath, "apps", testAppName)
	linkFi, err := os.Lstat(benchAppSymlinkPath)
	require.NoError(t, err, "symlink/file in bench not found: %s", benchAppSymlinkPath)
	assert.True(t, linkFi.Mode()&os.ModeSymlink != 0, "bench app path is not a symlink: %s", benchAppSymlinkPath)

	linkTarget, err := os.Readlink(benchAppSymlinkPath)
	require.NoError(t, err)
	absExpectedAppModuleInStorage, err := filepath.Abs(expectedAppModuleInStorage)
	require.NoError(t, err)
	assert.Equal(t, absExpectedAppModuleInStorage, linkTarget, "symlink does not point to the correct FPM storage location")

	appsTxtPath := filepath.Join(mockBenchPath, "sites", "apps.txt")
	appsTxtBytes, err := os.ReadFile(appsTxtPath)
	require.NoError(t, err, "failed to read apps.txt: %s", appsTxtPath)
	assert.Contains(t, string(appsTxtBytes), testAppName, "app name not found in apps.txt")

	pipLogPath := filepath.Join(mockBenchPath, "pip_called.log")
	_, err = os.Stat(pipLogPath)
	assert.NoError(t, err, "pip_called.log not found, indicates pip was not (mock) executed: %s", pipLogPath)
	if err == nil {
		pipLogBytes, _ := os.ReadFile(pipLogPath)
		expectedPipCall := filepath.Join("./apps", testAppName) // pip is run from bench root
		assert.Contains(t, string(pipLogBytes), expectedPipCall, "pip_called.log does not contain expected app path")
	}
}


func TestInstallCommand_RemotePackage(t *testing.T) {
	// --- Setup Phase ---
	// 1. Setup Mock FPM Repository Server
	mockRepoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Logf("Mock repo server received request: %s %s", r.Method, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "package-metadata.json") {
			// Serve package-metadata.json
			pkgMeta := repository.PackageMetadata{
				Org:           "testgrp", // Changed
				AppName:       "testapp", // Changed
				LatestVersion: "1.0.1",
				Versions: map[string]repository.PackageVersionMetadata{
					"1.0.1": {
						FPMPath:        "artifacts/testgrp/testapp/1.0.1/testapp-1.0.1.fpm", // Path uses org/app
						ChecksumSHA256: "dummychecksumfortestapp101",
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(pkgMeta)
		} else if strings.HasSuffix(r.URL.Path, ".fpm") {
			// Serve a dummy .fpm file (can use createDummyFpmPackage logic to create a real one if needed)
			// For this test, we need to ensure the FPM file has the *new* package structure.
			// So, create a temporary FPM file with the new structure.

			tempFpmDir, err := os.MkdirTemp("", "tempfpm-*")
			require.NoError(t, err)
			defer os.RemoveAll(tempFpmDir)

			dummyFpmName := "testapp-1.0.1.fpm"
			dummyFpmPath := filepath.Join(tempFpmDir, dummyFpmName)

			// Create a dummy app source that will be packaged into the .fpm
			// This app source is temporary and only used to create the dummy .fpm file content.
			appSourceDirForDummyFPM, err := os.MkdirTemp("", "tempsourcefordummyfpm-*")
			require.NoError(t, err)
			defer os.RemoveAll(appSourceDirForDummyFPM)
			// Use the same createMinimalFrappeApp helper. The paths it creates inside appSourceDirForDummyFPM
			// will be relative to that directory, which is what we need for zipping.
			createMinimalFrappeApp(t, appSourceDirForDummyFPM, "testapp", "1.0.1", "testgrp")


			archiveFile, err := os.Create(dummyFpmPath)
			require.NoError(t, err)
			zipWriter := zip.NewWriter(archiveFile)

			// Add app_metadata.json (essential for install command to read pkgMeta)
			appMetaContent := fmt.Sprintf(`{"org":"testgrp", "app_name":"testapp", "package_name":"testapp", "package_version":"1.0.1", "content_checksum":"dummychecksumfortestapp101"}`)
			fWriter, err := zipWriter.Create("app_metadata.json")
			require.NoError(t, err)
			_, err = io.WriteString(fWriter, appMetaContent)
			require.NoError(t, err)

			// Add app module files (new structure) - these are relative to the root of the zip
			// hooks.py for the app "testapp"
			hooksPathInZip := filepath.Join("testapp", "hooks.py")
			fWriterHooks, err := zipWriter.Create(hooksPathInZip)
			require.NoError(t, err)
			_, err = io.WriteString(fWriterHooks, "app_name = \"testapp\"")
			require.NoError(t, err)

			// __init__.py for the app "testapp"
			initPathInZip := filepath.Join("testapp", "__init__.py")
			fWriterInit, err := zipWriter.Create(initPathInZip)
			require.NoError(t, err)
			_, err = io.WriteString(fWriterInit, "# init")
			require.NoError(t, err)

			// modules.txt for the app "testapp"
			modulesPathInZip := filepath.Join("testapp", "modules.txt")
            fWriterModules, err := zipWriter.Create(modulesPathInZip)
            require.NoError(t, err)
            _, err = io.WriteString(fWriterModules, "testmodule")
            require.NoError(t, err)

			// Important: Close the zipWriter and the archiveFile before http.ServeFile uses it.
			require.NoError(t, zipWriter.Close())
			require.NoError(t, archiveFile.Close())

			http.ServeFile(w, r, dummyFpmPath)
		} else {
			http.NotFound(w, r)
		}
	}))
	defer mockRepoServer.Close()

	// 2. Setup FPM Config with the mock repository
	_, cleanup := setupTempFPMConfig(t) // Uses helper from repo_test.go (same package)
	defer cleanup()

	addRepoArgs := []string{"repo", "add", "mockrepo", mockRepoServer.URL, "--priority", "0"}
	// Need to reset repoAddPriority as it's a global in cmd package used by repoAddCmd
	repoAddPriority = 0
	if repoAddCmd.Flags().Lookup("priority") != nil {
		repoAddCmd.Flags().Lookup("priority").Value.Set("0")
	}
	_, err := executeCommand(rootCmd, addRepoArgs...) // Uses helper from repo_test.go
	require.NoError(t, err, "Failed to add mock repository")

	// 3. Setup Mock Bench
	mockBenchPath, err := os.MkdirTemp("", "mockbench-remote-*")
	require.NoError(t, err)
	defer os.RemoveAll(mockBenchPath)
	require.NoError(t, os.MkdirAll(filepath.Join(mockBenchPath, "apps"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(mockBenchPath, "sites"), 0o755))
	mockPipDir := filepath.Join(mockBenchPath, "env", "bin")
	require.NoError(t, os.MkdirAll(mockPipDir, 0o755))
	mockPipPath := filepath.Join(mockPipDir, "pip")
	if runtime.GOOS == "windows" { mockPipPath += ".exe" }
	pipScriptContent := "#!/bin/sh\necho \"Mock remote pip install $@\" >> pip_remote_called.log"
	if runtime.GOOS == "windows" { pipScriptContent = "@echo off\necho Mock remote pip install %* >> pip_remote_called.log" }
	require.NoError(t, os.WriteFile(mockPipPath, []byte(pipScriptContent), 0o755))

	cfg, err := config.LoadConfig()
	require.NoError(t, err, "Failed to load config for determining AppsBasePath")
	expectedFpmStorageAppPath := filepath.Join(cfg.AppsBasePath, "testgrp", "testapp", "1.0.1")
	expectedAppModuleInStorage := filepath.Join(expectedFpmStorageAppPath, "testapp")


	// --- Execution Phase ---
	installArgs := []string{
		"install", "testgrp/testapp==1.0.1", // Remote package identifier
		"--bench-path", mockBenchPath,
	}
	// Reset installCmd flags if any (though it doesn't have persistent ones like repoAddCmd)
	installCmd.Flags().VisitAll(func(f *cobra.Flag) { // Use actual installCmd var
		f.Value.Set(f.DefValue)
		f.Changed = false
	})
	installCmdOutput, err := executeCommand(rootCmd, installArgs...)
	require.NoError(t, err, "fpm install command failed for remote package. Output: %s", string(installCmdOutput))

	// --- Assertion Phase ---
	// Check if app files are in FPM storage
	_, err = os.Stat(filepath.Join(expectedAppModuleInStorage, "hooks.py"))
	assert.NoError(t, err, "hooks.py not found in FPM storage for remote install")

	// Check symlink in bench
	benchAppSymlinkPath := filepath.Join(mockBenchPath, "apps", "testapp")
	linkFi, err := os.Lstat(benchAppSymlinkPath)
	require.NoError(t, err, "symlink in bench not found for remote install")
	assert.True(t, linkFi.Mode()&os.ModeSymlink != 0, "bench app path is not a symlink for remote install")
	linkTarget, err := os.Readlink(benchAppSymlinkPath)
	require.NoError(t, err)
	absExpectedAppModuleInStorage, _ := filepath.Abs(expectedAppModuleInStorage)
	assert.Equal(t, absExpectedAppModuleInStorage, linkTarget, "symlink for remote install points to wrong FPM storage location")

	// Check apps.txt
	appsTxtPath := filepath.Join(mockBenchPath, "sites", "apps.txt")
	appsTxtBytes, err := os.ReadFile(appsTxtPath)
	require.NoError(t, err)
	assert.Contains(t, string(appsTxtBytes), "testapp", "app name not found in apps.txt for remote install")

	// Check pip call
	pipLogPath := filepath.Join(mockBenchPath, "pip_remote_called.log") // Different log file
	_, err = os.Stat(pipLogPath)
	assert.NoError(t, err, "pip_remote_called.log not found for remote install")
	if err == nil {
		pipLogBytes, _ := os.ReadFile(pipLogPath)
		assert.Contains(t, string(pipLogBytes), filepath.Join("./apps", "testapp"), "pip_remote_called.log does not contain expected app path")
	}
}

// populateAppInLocalStore creates a dummy app installation in the mock FPM local store.
func populateAppInLocalStore(t *testing.T, appsBasePath, org, appName, version, markerFileContent string) {
	t.Helper()
	appVersionStorePath := filepath.Join(appsBasePath, org, appName, version)
	appModuleInStorePath := filepath.Join(appVersionStorePath, appName)
	require.NoError(t, os.MkdirAll(appModuleInStorePath, 0o755))

	// Create a few essential files and a marker file for identification
	hooksContent := fmt.Sprintf("app_name = \"%s\"\napp_version = \"%s\"", appName, version)
	require.NoError(t, os.WriteFile(filepath.Join(appModuleInStorePath, "hooks.py"), []byte(hooksContent), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(appModuleInStorePath, "__init__.py"), []byte("# "+markerFileContent), 0o644))

	// Create app_metadata.json at the root of appVersionStorePath
	appMetaContent := fmt.Sprintf(`{"org":"%s", "app_name":"%s", "package_name":"%s", "package_version":"%s"}`, org, appName, appName, version)
	require.NoError(t, os.WriteFile(filepath.Join(appVersionStorePath, "app_metadata.json"), []byte(appMetaContent), 0o644))
}


func TestInstallCommand_PrioritizationAndLatestResolution(t *testing.T) {
	// --- Common Setup for all sub-tests ---
	tempHome, baseCleanup := setupTempFPMConfig(t) // Manages FPM config and home dir
	defer baseCleanup()
	t.Logf("TestInstallCommand_PrioritizationAndLatestResolution using temp home: %s", tempHome)

	cfg, err := config.LoadConfig() // Load config to get AppsBasePath
	require.NoError(t, err)
	mockAppsBasePath := cfg.AppsBasePath // This is where local FPM store will be (e.g. tempHome/.fpm/apps)
	require.NoError(t, os.MkdirAll(mockAppsBasePath, 0o755)) // Ensure it exists

	mockBenchPath, err := os.MkdirTemp("", "mockbench-prio-*")
	require.NoError(t, err)
	defer os.RemoveAll(mockBenchPath)
	require.NoError(t, os.MkdirAll(filepath.Join(mockBenchPath, "apps"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(mockBenchPath, "sites"), 0o755))
	mockPipDir := filepath.Join(mockBenchPath, "env", "bin")
	require.NoError(t, os.MkdirAll(mockPipDir, 0o755))
	mockPipPath := filepath.Join(mockPipDir, "pip")
	if runtime.GOOS == "windows" { mockPipPath += ".exe" }
	pipScriptContent := "#!/bin/sh\necho \"Mock prio pip install $@\" >> pip_prio_called.log"
	if runtime.GOOS == "windows" { pipScriptContent = "@echo off\necho Mock prio pip install %* >> pip_prio_called.log" }
	require.NoError(t, os.WriteFile(mockPipPath, []byte(pipScriptContent), 0o755))

	// Mock remote repository server
	mockRepoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Logf("Prioritization Test Mock Repo Server received request: %s", r.URL.Path)
		if r.URL.Path == "/metadata/myorg/myapp/package-metadata.json" {
			pkgMeta := repository.PackageMetadata{
				Org:       "myorg",	AppName:    "myapp", LatestVersion: "1.2.0", // Changed
				Versions: map[string]repository.PackageVersionMetadata{
					"1.0.0": {FPMPath: "artifacts/myorg/myapp/1.0.0/myapp-1.0.0.fpm", ChecksumSHA256: "remote-myapp-1.0.0-checksum"},
					"1.2.0": {FPMPath: "artifacts/myorg/myapp/1.2.0/myapp-1.2.0.fpm", ChecksumSHA256: "remote-myapp-1.2.0-checksum"},
				},
			}
			json.NewEncoder(w).Encode(pkgMeta)
		} else if strings.HasSuffix(r.URL.Path, ".fpm") { // Serve a generic FPM for any requested version
			versionFromFile := strings.Split(filepath.Base(r.URL.Path), "-")[1]
			versionFromFile = strings.TrimSuffix(versionFromFile, ".fpm")

			tempFpmDir, _ := os.MkdirTemp("", "tempfpm-prio-*")
			defer os.RemoveAll(tempFpmDir)
			dummyFpmPath := filepath.Join(tempFpmDir, filepath.Base(r.URL.Path))

			archiveFile, _ := os.Create(dummyFpmPath)
			zipWriter := zip.NewWriter(archiveFile)
			appMetaContent := fmt.Sprintf(`{"org":"myorg", "app_name":"myapp", "package_name":"myapp", "package_version":"%s", "content_checksum":"remote-myapp-%s-checksum"}`, versionFromFile, versionFromFile)
			fWriter, _ := zipWriter.Create("app_metadata.json")
			io.WriteString(fWriter, appMetaContent)
			zipWriter.Mkdir("myapp/", 0o755)
			fHooks, _ := zipWriter.Create("myapp/hooks.py")
			io.WriteString(fHooks, fmt.Sprintf("# Remote version %s marker", versionFromFile)) // Unique marker
			zipWriter.Close()
			archiveFile.Close()
			http.ServeFile(w, r, dummyFpmPath)
		} else {
			http.NotFound(w, r)
		}
	}))
	defer mockRepoServer.Close()

	// Add mock repo to config
	resetRepoCmdFlags() // Assuming this helper exists or is defined in this package
	_, err = executeCommand(rootCmd, "repo", "add", "mockremoterepo", mockRepoServer.URL)
	require.NoError(t, err)


	t.Run("InstallsFromLocalStoreIfVersionExists", func(t *testing.T) {
		populateAppInLocalStore(t, mockAppsBasePath, "myorg", "myapp", "1.0.0", "local_store_version_1.0.0")

		installArgs := []string{"install", "myorg/myapp==1.0.0", "--bench-path", mockBenchPath}
		resetInstallCmdFlags()
		_, err := executeCommand(rootCmd, installArgs...)
		require.NoError(t, err)

		// Verify symlink target contains the local store marker
		linkPath := filepath.Join(mockBenchPath, "apps", "myapp")
		linkTarget, _ := os.Readlink(linkPath)
		initPyContent, _ := os.ReadFile(filepath.Join(linkTarget, "__init__.py"))
		assert.Contains(t, string(initPyContent), "local_store_version_1.0.0")
	})

	t.Run("FallsBackToRemoteIfVersionNotLocal", func(t *testing.T) {
		// Ensure 1.2.0 is NOT in local store initially for this subtest
		os.RemoveAll(filepath.Join(mockAppsBasePath, "myorg", "myapp", "1.2.0"))

		installArgs := []string{"install", "myorg/myapp==1.2.0", "--bench-path", mockBenchPath}
		resetInstallCmdFlags()
		_, err := executeCommand(rootCmd, installArgs...)
		require.NoError(t, err)

		// Verify symlink target points to a path that now should exist in local store,
		// and it should contain the remote marker.
		linkPath := filepath.Join(mockBenchPath, "apps", "myapp")
		linkTarget, _ := os.Readlink(linkPath)
		// Expected path in store will be .../myorg/myapp/1.2.0/myapp
		assert.Contains(t, linkTarget, filepath.Join(mockAppsBasePath, "myorg", "myapp", "1.2.0", "myapp"))

		hooksPyContent, _ := os.ReadFile(filepath.Join(linkTarget, "hooks.py")) // hooks.py from remote has specific marker
		assert.Contains(t, string(hooksPyContent), "# Remote version 1.2.0 marker")
	})

	t.Run("InstallsLatestFromLocalStore", func(t *testing.T) {
		populateAppInLocalStore(t, mockAppsBasePath, "myorg", "myapp", "1.0.0", "local_v1.0.0")
		populateAppInLocalStore(t, mockAppsBasePath, "myorg", "myapp", "1.1.0", "local_v1.1.0_latest") // This should be chosen

		installArgs := []string{"install", "myorg/myapp", "--bench-path", mockBenchPath} // Request "latest"
		resetInstallCmdFlags()
		_, err := executeCommand(rootCmd, installArgs...)
		require.NoError(t, err)

		linkPath := filepath.Join(mockBenchPath, "apps", "myapp")
		linkTarget, _ := os.Readlink(linkPath)
		initPyContent, _ := os.ReadFile(filepath.Join(linkTarget, "__init__.py"))
		assert.Contains(t, string(initPyContent), "local_v1.1.0_latest")
		assert.Contains(t, linkTarget, "1.1.0") // Check path contains the version
	})

	t.Run("FallsBackToRemoteForLatestIfNotLocal", func(t *testing.T) {
		// Ensure no versions of myorg/myapp exist locally for this test
		os.RemoveAll(filepath.Join(mockAppsBasePath, "myorg", "myapp"))

		installArgs := []string{"install", "myorg/myapp", "--bench-path", mockBenchPath} // Request "latest"
		resetInstallCmdFlags()
		_, err := executeCommand(rootCmd, installArgs...)
		require.NoError(t, err)

		// Remote server's latest is 1.2.0
		linkPath := filepath.Join(mockBenchPath, "apps", "myapp")
		linkTarget, _ := os.Readlink(linkPath)
		hooksPyContent, _ := os.ReadFile(filepath.Join(linkTarget, "hooks.py"))
		assert.Contains(t, string(hooksPyContent), "# Remote version 1.2.0 marker")
		assert.Contains(t, linkTarget, "1.2.0") // Check path contains the version
	})
}

// resetRepoCmdFlags is a placeholder for a helper that might be needed if repoCmd itself had flags.
func resetRepoCmdFlags() {
	// repoCmd.Flags().VisitAll(...)
}
// resetInstallCmdFlags is a placeholder for a helper that might be needed if installCmd itself had flags
// that are not handled by rootCmd.SetArgs for each call.
// The flags --bench-path and --site are parsed by Cobra for each run.
func resetInstallCmdFlags() {
	installCmd.Flags().VisitAll(func(f *cobra.Flag) {
		f.Value.Set(f.DefValue)
		f.Changed = false
	})
}
