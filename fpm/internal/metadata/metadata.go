package metadata

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// AppMetadata defines the structure of the app_metadata.json file
// that will be included in the .fpm package.
type AppMetadata struct {
	PackageName         string            `json:"package_name,omitempty"` // This might be the same as AppName or the repo name
	PackageVersion      string            `json:"package_version,omitempty"`
	Description         string            `json:"description,omitempty"`
	Author              string            `json:"author,omitempty"`
	Org                 string            `json:"org,omitempty"`      // GitHub organization or similar
	AppName             string            `json:"app_name,omitempty"` // The actual Frappe app name (e.g., erpnext)
	Dependencies        map[string]string `json:"dependencies,omitempty"` // e.g., "erpnext": "13.2.1"
	FrappeCompatibility []string          `json:"frappe_compatibility,omitempty"` // e.g., ["13.x.x", "14.x.x"]
	Hooks               map[string]string `json:"hooks,omitempty"` // e.g., "install_hooks": "install_hooks.py"
	// Add other fields as necessary
}

// LoadAppMetadata loads metadata from app_metadata.json file in the given appPath.
// If the file doesn't exist, it returns an empty AppMetadata struct and no error.
func LoadAppMetadata(appPath string) (*AppMetadata, error) {
	metadataFilePath := filepath.Join(appPath, "app_metadata.json")
	data := &AppMetadata{
		Dependencies:        make(map[string]string),
		FrappeCompatibility: make([]string, 0),
		Hooks:               make(map[string]string),
	}

	if _, err := os.Stat(metadataFilePath); os.IsNotExist(err) {
		// File doesn't exist, return a new struct (already initialized)
		return data, nil
	} else if err != nil {
		return nil, err // Other stat error
	}

	fileBytes, err := os.ReadFile(metadataFilePath)
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(fileBytes, data); err != nil {
		return nil, err
	}
	return data, nil
}

// GenerateAppMetadata creates a basic AppMetadata struct.
// It infers the packageName from the appPath's directory name.
// It sets the packageVersion from the provided argument.
func GenerateAppMetadata(appPath string, version string) (*AppMetadata, error) {
	absPath, err := filepath.Abs(appPath)
	if err != nil {
		return nil, err
	}
	// Infer package name from the directory name
	// This might need to be more sophisticated, e.g. looking for a specific module name
	packageName := filepath.Base(absPath)
	// A common convention for frappe apps is that the actual app module is one level deeper
    // e.g. my_app_repo/my_app_module. So we check if there's a directory with the same name inside.
    internalAppDir := filepath.Join(absPath, packageName)
    if stat, err := os.Stat(internalAppDir); err == nil && stat.IsDir() {
        // If my_app_repo/my_app_repo exists, that's likely the app's name
        // This is a simple heuristic.
    } else {
        // If not, check parent dir for common "apps" folder structure like in a bench
        parentDir := filepath.Base(filepath.Dir(absPath))
        if parentDir == "apps" {
             // we are likely in frappe-bench/apps/my_app, so packageName is correct
        } else {
            // Could not reliably infer app name, user might need to specify it
            // For now, we stick with the base directory name.
            // Consider adding a warning or requiring explicit app name if complex.
        }
    }


	return &AppMetadata{
		PackageName:    packageName,
		PackageVersion: version,
		Dependencies:   make(map[string]string), // Initialize to avoid nil map
		FrappeCompatibility: make([]string, 0), // Initialize to avoid nil slice
		Hooks:          make(map[string]string),
	}, nil
}

// SaveAppMetadata saves the AppMetadata struct to an app_metadata.json file
// in the specified directory (usually the staging directory for the package).
func SaveAppMetadata(targetDir string, data *AppMetadata) error {
	metadataFilePath := filepath.Join(targetDir, "app_metadata.json")
	fileBytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(metadataFilePath, fileBytes, 0644)
}
