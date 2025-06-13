package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// FPMConfig defines the structure for FPM's configuration.
type FPMConfig struct {
	AppsBasePath string `json:"apps_base_path,omitempty"`
}

// LoadConfig loads the FPM configuration from a predefined path.
// It returns a configuration with defaults if the file doesn't exist
// or if specific fields are missing/empty in the file.
func LoadConfig() (*FPMConfig, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get user home directory: %w", err)
	}

	// Define default values
	defaultAppsBasePath := filepath.Join(homeDir, ".fpm", "apps")
	// configDir := filepath.Join(homeDir, ".fpm") // Not strictly needed here but good for context
	configFilePath := filepath.Join(homeDir, ".fpm", "config.json")

	// Initialize with default configuration
	conf := &FPMConfig{
		AppsBasePath: defaultAppsBasePath,
	}

	// Check if config file exists
	_, err = os.Stat(configFilePath)
	if os.IsNotExist(err) {
		// Config file does not exist.
		return conf, nil
	} else if err != nil {
		// Other error when stating file (permissions, etc.)
		return nil, fmt.Errorf("failed to stat config file %s: %w", configFilePath, err)
	}

	// Config file exists, try to read and unmarshal it
	fileBytes, err := os.ReadFile(configFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", configFilePath, err)
	}

	if len(fileBytes) == 0 {
		// Treat as if no config file found (or empty file), return defaults.
		return conf, nil
	}

	// Unmarshal JSON into the conf struct. This will overwrite defaults if keys exist in JSON.
	if err := json.Unmarshal(fileBytes, conf); err != nil {
		// Log the error and the content for debugging if possible
		// fmt.Fprintf(os.Stderr, "Error unmarshalling config data: %s\nData: %s\n", err, string(fileBytes))
		return nil, fmt.Errorf("failed to unmarshal config file %s: %w. Content: %s", configFilePath, err, string(fileBytes))
	}

	// Post-unmarshal checks: if critical fields are empty, revert to default.
	if conf.AppsBasePath == "" {
		conf.AppsBasePath = defaultAppsBasePath
	}

	return conf, nil
}
