package apputils

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// HooksMetadata represents metadata extracted from a Frappe app's hooks.py.
type HooksMetadata struct {
	AppName        string
	AppTitle       string
	AppDescription string
	AppPublisher   string
	AppEmail       string
	AppLicense     string
	AppIcon        string
	AppLogoUrl     string
	AppColor       string
}

// GetAppMetadataFromHooks reads hooks.py and extracts standard metadata fields.
func GetAppMetadataFromHooks(hooksFilePath string) (*HooksMetadata, error) {
	fileBytes, err := os.ReadFile(hooksFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("hooks file not found at %s: %w", hooksFilePath, err)
		}
		return nil, fmt.Errorf("failed to open hooks file %s: %w", hooksFilePath, err)
	}

	content := string(fileBytes)
	meta := &HooksMetadata{}

	// Keys to extract
	keys := []struct {
		varName string
		target  *string
	}{
		{"app_name", &meta.AppName},
		{"app_title", &meta.AppTitle},
		{"app_description", &meta.AppDescription},
		{"app_publisher", &meta.AppPublisher},
		{"app_email", &meta.AppEmail},
		{"app_license", &meta.AppLicense},
		{"app_icon", &meta.AppIcon},
		{"app_logo_url", &meta.AppLogoUrl},
		{"app_color", &meta.AppColor},
	}

	for _, k := range keys {
		// Matches:
		// varName = """...""" OR varName = '''...''' OR varName = "..." OR varName = '...'
		pattern := `(?m)^\s*` + regexp.QuoteMeta(k.varName) + `\s*=\s*(?:"""([\s\S]*?)"""|'''([\s\S]*?)'''|["']([^"']*)["'])`
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(content); len(matches) > 0 {
			val := ""
			for i := 1; i < len(matches); i++ {
				if matches[i] != "" {
					val = matches[i]
					break
				}
			}
			*k.target = strings.TrimSpace(val)
		}
	}

	return meta, nil
}

// GetAppNameFromHooks reads a Python file (typically hooks.py) and
// extracts the value of a variable named "app_name".
// It looks for assignments like app_name = "value" or app_name = 'value'.
func GetAppNameFromHooks(hooksFilePath string) (appName string, err error) {
	fileBytes, err := os.ReadFile(hooksFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("hooks file not found at %s: %w", hooksFilePath, err)
		}
		return "", fmt.Errorf("failed to open hooks file %s: %w", hooksFilePath, err)
	}
	re := regexp.MustCompile(`(?m)^\s*app_name\s*=\s*(?:"""([\s\S]*?)"""|'''([\s\S]*?)'''|["']([^"']*)["'])`)
	matches := re.FindStringSubmatch(string(fileBytes))
	if len(matches) > 0 {
		for i := 1; i < len(matches); i++ {
			if matches[i] != "" {
				return strings.TrimSpace(matches[i]), nil
			}
		}
		return "", nil
	}
	return "", fmt.Errorf("app_name not found or pattern not matched in %s", hooksFilePath)
}
