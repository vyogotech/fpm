package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig(t *testing.T) {
	// Helper struct for marshalling config for tests
	type testFPMConfig struct {
		AppsBasePath string `json:"apps_base_path,omitempty"`
	}

	t.Run("No Config File Exists", func(t *testing.T) {
		mockHomeDir := t.TempDir()
		originalHome, err := os.UserHomeDir() // Save original home for restoration if needed, though t.Setenv handles it.
		require.NoError(t, err)
		t.Setenv("HOME", mockHomeDir)
		defer t.Setenv("HOME", originalHome) // Restore original HOME after test

		// Ensure no .fpm/config.json exists in mockHomeDir
		fpmConfigDir := filepath.Join(mockHomeDir, ".fpm")
		_ = os.RemoveAll(fpmConfigDir) // Remove if it somehow exists

		loadedCfg, err := LoadConfig()
		require.NoError(t, err)

		expectedAppsBasePath := filepath.Join(mockHomeDir, ".fpm", "apps")
		assert.Equal(t, expectedAppsBasePath, loadedCfg.AppsBasePath)
	})

	t.Run("Config File Exists and is Valid", func(t *testing.T) {
		mockHomeDir := t.TempDir()
		originalHome, err := os.UserHomeDir()
		require.NoError(t, err)
		t.Setenv("HOME", mockHomeDir)
		defer t.Setenv("HOME", originalHome)

		fpmConfigDir := filepath.Join(mockHomeDir, ".fpm")
		err = os.MkdirAll(fpmConfigDir, 0755)
		require.NoError(t, err)

		customAppsPath := filepath.Join(mockHomeDir, "custom_fpm_apps_location")
		configFile := filepath.Join(fpmConfigDir, "config.json")

		confToWrite := testFPMConfig{AppsBasePath: customAppsPath}
		confBytes, err := json.Marshal(confToWrite)
		require.NoError(t, err)
		err = os.WriteFile(configFile, confBytes, 0644)
		require.NoError(t, err)

		loadedCfg, err := LoadConfig()
		require.NoError(t, err)
		assert.Equal(t, customAppsPath, loadedCfg.AppsBasePath)
	})

	t.Run("Config File Exists, AppsBasePath Empty", func(t *testing.T) {
		mockHomeDir := t.TempDir()
		originalHome, err := os.UserHomeDir()
		require.NoError(t, err)
		t.Setenv("HOME", mockHomeDir)
		defer t.Setenv("HOME", originalHome)

		fpmConfigDir := filepath.Join(mockHomeDir, ".fpm")
		err = os.MkdirAll(fpmConfigDir, 0755)
		require.NoError(t, err)

		configFile := filepath.Join(fpmConfigDir, "config.json")
		// Write config with empty apps_base_path
		confToWrite := testFPMConfig{AppsBasePath: ""} // Explicitly empty
		confBytes, err := json.Marshal(confToWrite)
		require.NoError(t, err)
		err = os.WriteFile(configFile, confBytes, 0644)
		require.NoError(t, err)

		loadedCfg, err := LoadConfig()
		require.NoError(t, err)

		expectedAppsBasePath := filepath.Join(mockHomeDir, ".fpm", "apps") // Should revert to default
		assert.Equal(t, expectedAppsBasePath, loadedCfg.AppsBasePath)
	})

	t.Run("Config File Exists, AppsBasePath Key Missing", func(t *testing.T) {
		mockHomeDir := t.TempDir()
		originalHome, err := os.UserHomeDir()
		require.NoError(t, err)
		t.Setenv("HOME", mockHomeDir)
		defer t.Setenv("HOME", originalHome)

		fpmConfigDir := filepath.Join(mockHomeDir, ".fpm")
		err = os.MkdirAll(fpmConfigDir, 0755)
		require.NoError(t, err)

		configFile := filepath.Join(fpmConfigDir, "config.json")
		// Write config with apps_base_path key missing (e.g., an empty JSON object)
		err = os.WriteFile(configFile, []byte("{}"), 0644)
		require.NoError(t, err)

		loadedCfg, err := LoadConfig()
		require.NoError(t, err)

		expectedAppsBasePath := filepath.Join(mockHomeDir, ".fpm", "apps") // Should revert to default
		assert.Equal(t, expectedAppsBasePath, loadedCfg.AppsBasePath)
	})


	t.Run("Config File Invalid JSON", func(t *testing.T) {
		mockHomeDir := t.TempDir()
		originalHome, err := os.UserHomeDir()
		require.NoError(t, err)
		t.Setenv("HOME", mockHomeDir)
		defer t.Setenv("HOME", originalHome)

		fpmConfigDir := filepath.Join(mockHomeDir, ".fpm")
		err = os.MkdirAll(fpmConfigDir, 0755)
		require.NoError(t, err)

		configFile := filepath.Join(fpmConfigDir, "config.json")
		// Write malformed JSON
		err = os.WriteFile(configFile, []byte(`{"apps_base_path": "some_path"`), 0644) // Missing closing brace
		require.NoError(t, err)

		_, err = LoadConfig()
		assert.Error(t, err, "Expected an error for malformed JSON")
		// Optionally, check for a specific error type or message part related to JSON unmarshalling
		assert.Contains(t, err.Error(), "unmarshal config file", "Error message should indicate JSON unmarshal failure")
	})

	t.Run("Config File is an Empty File", func(t *testing.T) {
		mockHomeDir := t.TempDir()
		originalHome, err := os.UserHomeDir()
		require.NoError(t, err)
		t.Setenv("HOME", mockHomeDir)
		defer t.Setenv("HOME", originalHome)

		fpmConfigDir := filepath.Join(mockHomeDir, ".fpm")
		err = os.MkdirAll(fpmConfigDir, 0755)
		require.NoError(t, err)

		configFile := filepath.Join(fpmConfigDir, "config.json")
		// Create an empty file
		err = os.WriteFile(configFile, []byte{}, 0644)
		require.NoError(t, err)

		loadedCfg, err := LoadConfig()
		require.NoError(t, err)

		expectedAppsBasePath := filepath.Join(mockHomeDir, ".fpm", "apps") // Should use default
		assert.Equal(t, expectedAppsBasePath, loadedCfg.AppsBasePath)
	})

}
