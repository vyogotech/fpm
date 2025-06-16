package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort" // For ListRepositories
)

// RepositoryConfig defines the structure for a single FPM repository configuration.
type RepositoryConfig struct {
	Name     string `json:"name"`      // User-defined unique name for the repository
	URL      string `json:"url"`       // Base URL of the FPM repository
	Priority int    `json:"priority"`  // Lower numbers mean higher priority
}

// FPMConfig defines the structure for FPM's configuration.
type FPMConfig struct {
	AppsBasePath             string                        `json:"apps_base_path,omitempty"`
	Repositories             map[string]RepositoryConfig `json:"repositories,omitempty"`
	DefaultPublishRepository string                        `json:"default_publish_repository,omitempty"`
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
		Repositories: make(map[string]RepositoryConfig),
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
	// Ensure Repositories map is not nil if it was missing in JSON
	if conf.Repositories == nil {
		conf.Repositories = make(map[string]RepositoryConfig)
	}

	return conf, nil
}

// SaveConfig saves the provided FPMConfig to the predefined configuration path.
func SaveConfig(conf *FPMConfig) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get user home directory: %w", err)
	}
	configDir := filepath.Join(homeDir, ".fpm")
	configFilePath := filepath.Join(configDir, "config.json")

	// Ensure the config directory exists
	if err := os.MkdirAll(configDir, 0750); err != nil { // 0750: rwxr-x---
		return fmt.Errorf("failed to create config directory %s: %w", configDir, err)
	}

	fileBytes, err := json.MarshalIndent(conf, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config to JSON: %w", err)
	}

	if err := os.WriteFile(configFilePath, fileBytes, 0640); err != nil { // 0640: rw-r-----
		return fmt.Errorf("failed to write config file %s: %w", configFilePath, err)
	}
	return nil
}

// AddRepository adds a new repository configuration.
// Returns an error if a repository with the same name already exists.
func AddRepository(config *FPMConfig, repo RepositoryConfig) error {
	if _, exists := config.Repositories[repo.Name]; exists {
		return fmt.Errorf("repository with name '%s' already exists", repo.Name)
	}
	if config.Repositories == nil { // Should be initialized by LoadConfig or DefaultConfig
		config.Repositories = make(map[string]RepositoryConfig)
	}
	config.Repositories[repo.Name] = repo
	return nil
}

// GetRepository retrieves a repository configuration by its name.
func GetRepository(config *FPMConfig, name string) (RepositoryConfig, bool) {
	if config.Repositories == nil {
		return RepositoryConfig{}, false
	}
	repo, found := config.Repositories[name]
	return repo, found
}

// ListRepositories returns a slice of all repository configurations, sorted by priority then name.
func ListRepositories(config *FPMConfig) []RepositoryConfig {
	if config.Repositories == nil {
		return []RepositoryConfig{}
	}
	list := make([]RepositoryConfig, 0, len(config.Repositories))
	for _, repo := range config.Repositories {
		list = append(list, repo)
	}

	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Priority != list[j].Priority {
			return list[i].Priority < list[j].Priority
		}
		return list[i].Name < list[j].Name
	})
	return list
}

// RemoveRepository removes a repository configuration by its name.
// Returns true if the repository was found and removed, false otherwise.
func RemoveRepository(config *FPMConfig, name string) bool {
	if config.Repositories == nil {
		return false
	}
	_, found := config.Repositories[name]
	if found {
		delete(config.Repositories, name)
	}
	return found
}

// DefaultConfig returns a new FPMConfig with default values.
func DefaultConfig() (*FPMConfig, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get user home directory: %w", err)
	}
	defaultAppsBasePath := filepath.Join(homeDir, ".fpm", "apps")
	return &FPMConfig{
		AppsBasePath:             defaultAppsBasePath,
		Repositories:             make(map[string]RepositoryConfig),
		DefaultPublishRepository: "", // Default is empty
	}, nil
}

// InitConfig ensures a configuration file exists, creating one with defaults if not.
// It returns the loaded or newly created configuration.
func InitConfig() (*FPMConfig, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get user home directory for InitConfig: %w", err)
	}
	configFilePath := filepath.Join(homeDir, ".fpm", "config.json")

	_, err = os.Stat(configFilePath)
	if os.IsNotExist(err) {
		// Config file does not exist, create it with defaults
		fmt.Printf("Configuration file not found at %s. Creating with default settings.\n", configFilePath)
		defaultConf, errCreate := DefaultConfig()
		if errCreate != nil {
			return nil, fmt.Errorf("failed to get default config for InitConfig: %w", errCreate)
		}
		if errSave := SaveConfig(defaultConf); errSave != nil {
			return nil, fmt.Errorf("failed to save initial default config: %w", errSave)
		}
		return defaultConf, nil
	} else if err != nil {
		// Some other error trying to stat the file
		return nil, fmt.Errorf("error checking config file %s: %w", configFilePath, err)
	}

	// Config file exists, load it
	return LoadConfig()
}
