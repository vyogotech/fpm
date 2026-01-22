package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInstallCommand_AssetDeployment(t *testing.T) {
	// --- Setup ---
	testAppName := "assetapp"
	testAppVersion := "1.2.3"
	testAppOrg := "testorg"

	tempBaseDir, err := os.MkdirTemp("", "fpm-asset-test-root-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempBaseDir)

	sourceAppDir := SharedCreateMinimalAppForPackage(t, tempBaseDir, testAppName, nil)

	// Create compiled_assets in source
	compiledAssetsDir := filepath.Join(sourceAppDir, "compiled_assets")
	require.NoError(t, os.MkdirAll(filepath.Join(compiledAssetsDir, "js"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(compiledAssetsDir, "js", "main.min.js"), []byte("console.log('minified');"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(compiledAssetsDir, "style.min.css"), []byte("body{margin:0}"), 0644))

	// 2. Package the app
	packageOutputDir := filepath.Join(tempBaseDir, "output")
	require.NoError(t, os.MkdirAll(packageOutputDir, 0755))

	packageArgs := []string{
		"package", sourceAppDir,
		"--output-path", packageOutputDir,
		"--version", testAppVersion,
		"--org", testAppOrg,
		"--app-name", testAppName,
	}
	_, err = SharedRunFPMCommand(t, false, packageArgs...)
	require.NoError(t, err, "fpm package command failed")

	packagedFPMFile := filepath.Join(packageOutputDir, testAppName+"-"+testAppVersion+".fpm")

	// 3. Setup Mock Bench
	mockBenchPath := filepath.Join(tempBaseDir, "mockbench")
	require.NoError(t, os.MkdirAll(filepath.Join(mockBenchPath, "apps"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(mockBenchPath, "sites", "assets"), 0755))
	
	// Mock Pip
	mockPipDir := filepath.Join(mockBenchPath, "env", "bin")
	require.NoError(t, os.MkdirAll(mockPipDir, 0755))
	mockPipPath := filepath.Join(mockPipDir, "pip")
	if runtime.GOOS == "windows" { mockPipPath += ".exe" }
	require.NoError(t, os.WriteFile(mockPipPath, []byte("#!/bin/sh\nexit 0"), 0755))

	// Mock FPM Storage
	mockAppsBasePath := filepath.Join(tempBaseDir, "fpmstorage")
	require.NoError(t, os.MkdirAll(mockAppsBasePath, 0755))
	t.Setenv("FPM_APPS_BASE_PATH", mockAppsBasePath)

	// --- Execution ---
	installArgs := []string{
		"install", packagedFPMFile,
		"--bench-path", mockBenchPath,
	}
	_, err = SharedRunFPMCommand(t, false, installArgs...)
	require.NoError(t, err, "fpm install command failed")

	// --- Verification ---
	deployedAssetsPath := filepath.Join(mockBenchPath, "sites", "assets", testAppName)
	
	// Check main.min.js
	jsFile := filepath.Join(deployedAssetsPath, "js", "main.min.js")
	_, err = os.Stat(jsFile)
	assert.NoError(t, err, "Deployed asset JS file not found: %s", jsFile)
	if err == nil {
		content, _ := os.ReadFile(jsFile)
		assert.Equal(t, "console.log('minified');", string(content))
	}

	// Check style.min.css
	cssFile := filepath.Join(deployedAssetsPath, "style.min.css")
	_, err = os.Stat(cssFile)
	assert.NoError(t, err, "Deployed asset CSS file not found: %s", cssFile)
	if err == nil {
		content, _ := os.ReadFile(cssFile)
		assert.Equal(t, "body{margin:0}", string(content))
	}
}
