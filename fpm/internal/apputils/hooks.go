package apputils

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// GetAppNameFromHooks reads a Python file (typically hooks.py) and
// extracts the value of a variable named "app_name".
// It looks for assignments like app_name = "value" or app_name = 'value'.
func GetAppNameFromHooks(hooksFilePath string) (appName string, err error) {
	file, err := os.Open(hooksFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("hooks file not found at %s: %w", hooksFilePath, err)
		}
		return "", fmt.Errorf("failed to open hooks file %s: %w", hooksFilePath, err)
	}
	defer file.Close()

	// Regex to find lines like: app_name = "my_app" or app_name = 'my_app'
	// - `^\s*`: Matches optional leading whitespace from the start of the line.
	// - `app_name`: Matches the literal string "app_name".
	// - `\s*=\s*`: Matches an equals sign, surrounded by optional whitespace.
	// - `["']`: Matches either a single or double quote.
	// - `([^"']*)`: Captures zero or more characters that are not a quote (this is the app name). This is group 1.
	// - `["']`: Matches the closing quote.
	// - `\s*(?:#.*)?$`: Matches optional trailing whitespace and an optional Python comment until the end of the line.
	re := regexp.MustCompile(`^\s*app_name\s*=\s*["']([^"']*)["']\s*(?:#.*)?$`)

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		matches := re.FindStringSubmatch(line)
		// matches[0] is the full matched line/substring
		// matches[1] is the content of the first capturing group (the app name itself)
		if len(matches) == 2 {
			return strings.TrimSpace(matches[1]), nil // Return the first match found
		}
	}

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("error scanning hooks file %s: %w", hooksFilePath, err)
	}

	return "", fmt.Errorf("app_name not found or pattern not matched in %s", hooksFilePath)
}

// GetAppVersionFromHooks reads a Python file (typically hooks.py) and
// extracts the value of a variable named "app_version" or "__version__".
// It looks for assignments like app_version = "value" or __version__ = 'value'.
// If both are present, it prefers "app_version".
func GetAppVersionFromHooks(hooksFilePath string) (string, error) {
	file, err := os.Open(hooksFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			// Not finding the file is not an error for this function, just means no version found.
			return "", nil
		}
		return "", fmt.Errorf("failed to open hooks file %s: %w", hooksFilePath, err)
	}
	defer file.Close()

	// Regex to find lines like: app_version = "0.0.1" or __version__ = '0.0.1'
	// - `(?m)`: Multiline mode, `^` and `$` match start/end of line.
	// - `^\s*`: Matches optional leading whitespace from the start of the line.
	// - `(app_version|__version__)`: Captures "app_version" or "__version__" into group 1.
	// - `\s*=\s*`: Matches an equals sign, surrounded by optional whitespace.
	// - `["']`: Matches either a single or double quote.
	// - `([^"']+)`: Captures one or more characters that are not a quote (this is the version string). This is group 2.
	// - `["']`: Matches the closing quote.
	// - `\s*(?:#.*)?$`: Matches optional trailing whitespace and an optional Python comment until the end of the line.
	re := regexp.MustCompile(`(?m)^\s*(app_version|__version__)\s*=\s*["']([^"']+)["']\s*(?:#.*)?$`)

	contentBytes, err := io.ReadAll(file)
	if err != nil {
		return "", fmt.Errorf("failed to read hooks file %s: %w", hooksFilePath, err)
	}
	content := string(contentBytes)

	allMatches := re.FindAllStringSubmatch(content, -1)

	appVersionVal := ""
	dunderVersionVal := ""

	for _, matches := range allMatches {
		if len(matches) == 3 { // Full match, group 1 (var name), group 2 (var value)
			varName := matches[1]
			varValue := strings.TrimSpace(matches[2])
			if varName == "app_version" {
				appVersionVal = varValue
			} else if varName == "__version__" {
				dunderVersionVal = varValue
			}
		}
	}

	// Prefer app_version if both are found
	if appVersionVal != "" {
		return appVersionVal, nil
	}
	if dunderVersionVal != "" {
		return dunderVersionVal, nil
	}

	// No version found, but file was read successfully.
	return "", nil
}
