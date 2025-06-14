package cmd

import (
	"bytes"
	// "encoding/json" // Not used directly in these tests
	"fmt"
	// "io" // Not used directly
	"os"
	"path/filepath"
	"runtime" // For OS-specific HOME env var
	"strings"
	"testing"

	"fpm/internal/config"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTempFPMConfig creates a temporary FPM config for testing.
// It sets the HOME (or USERPROFILE on Windows) env var to a temp dir, so FPM uses this for config.
// Returns the path to the temp home dir and a cleanup function.
func setupTempFPMConfig(t *testing.T) (string, func()) {
	t.Helper()
	var origHome string
	var homeSet bool

	if runtime.GOOS == "windows" {
		origHome, homeSet = os.LookupEnv("USERPROFILE")
		require.NoError(t, os.Setenv("USERPROFILE", t.TempDir()), "Failed to set USERPROFILE for test")
	} else {
		origHome, homeSet = os.LookupEnv("HOME")
		require.NoError(t, os.Setenv("HOME", t.TempDir()), "Failed to set HOME for test")
	}

	currentHome := os.Getenv("HOME")
	if runtime.GOOS == "windows" {
		currentHome = os.Getenv("USERPROFILE")
	}


	// Ensure config is initialized within this new temp home
	// This will create ~/.fpm/config.json in the temp home dir
	_, err := config.InitConfig()
	require.NoError(t, err, "Failed to init config in temp home dir: %s", currentHome)

	cleanup := func() {
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
		// The temp dir set for HOME/USERPROFILE is automatically cleaned up by t.TempDir()
		// We don't need os.RemoveAll(currentHome) here.

		// Reset global config instance in config package if it caches it
		// This might be needed if config.LoadConfig() or InitConfig() caches its result.
		// For now, assume InitConfig/LoadConfig always re-reads or handles it for subsequent calls.
		// A more robust way would be for config package to offer a ResetForTesting() func.
	}
	return currentHome, cleanup
}

// executeCommand is a helper to execute Cobra commands and capture their output.
func executeCommand(root *cobra.Command, args ...string) (string, error) {
	buf := new(bytes.Buffer)
	root.SetOut(buf)
	root.SetErr(buf) // Capture stderr as well, useful for errors from cobra itself
	root.SetArgs(args)

	err := root.Execute()
	output := buf.String()
	// For debugging:
	// fmt.Printf("Executing command: %s %s\nOutput:\n%s\nError: %v\n", root.Use, strings.Join(args, " "), output, err)
	return output, err
}


func TestRepoAddCmd(t *testing.T) {
	tempHome, cleanup := setupTempFPMConfig(t)
	defer cleanup()

	t.Logf("TestRepoAddCmd using temp home: %s", tempHome)


	testCases := []struct {
		name        string
		args        []string
		expectedOut string
		expectErr   bool
		repoToVerify *config.RepositoryConfig // For verification after successful add
	}{
		{
			name:        "add new repo",
			args:        []string{"repo", "add", "central", "http://localhost:8080/fpm-repo", "--priority", "10"},
			expectedOut: "Repository 'central' (http://localhost:8080/fpm-repo) added successfully with priority 10.",
			expectErr:   false,
			repoToVerify: &config.RepositoryConfig{Name: "central", URL: "http://localhost:8080/fpm-repo", Priority: 10},
		},
		{
			name:        "add repo with default priority",
			args:        []string{"repo", "add", "local", "file:///var/tmp/fpm-repo"},
			expectedOut: "Repository 'local' (file:///var/tmp/fpm-repo) added successfully with priority 0.",
			expectErr:   false,
			repoToVerify: &config.RepositoryConfig{Name: "local", URL: "file:///var/tmp/fpm-repo", Priority: 0},
		},
		{
			name:      "add existing repo",
			args:      []string{"repo", "add", "central", "http://new-url.com"}, // "central" already added
			expectErr: true,
		},
		{
			name:      "missing url",
			args:      []string{"repo", "add", "another"},
			expectErr: true,
		},
		{
			name:      "missing name and url",
			args:      []string{"repo", "add"},
			expectErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Reset package-level variable repoAddPriority before each relevant command execution
			// This is crucial because it's not reset automatically by Cobra for package-level vars.
			originalRepoAddPriority := repoAddPriority // Save to restore if needed, though not strictly necessary here
			repoAddPriority = 0 // Reset to its default value (as defined by its flag)

			// Also reset the flag value in the command's flagset to its default.
			// This ensures that if a previous test case set it, it doesn't persist for cases that don't set it.
			if repoAddCmd.Flags().Lookup("priority") != nil {
				repoAddCmd.Flags().Lookup("priority").Value.Set(repoAddCmd.Flags().Lookup("priority").DefValue)
			}


			output, err := executeCommand(rootCmd, tc.args...)

			if tc.expectErr {
				assert.Error(t, err, "Expected an error for test case: %s. Output: %s", tc.name, output)
			} else {
				assert.NoError(t, err, "Did not expect an error for test case: %s. Output: %s", tc.name, output)
				assert.Contains(t, output, tc.expectedOut, "Output for test case '%s' did not match", tc.name)

				if tc.repoToVerify != nil {
					cfg, loadErr := config.LoadConfig() // LoadConfig should now read from tempHome
					require.NoError(t, loadErr)

					repoConf, found := config.GetRepository(cfg, tc.repoToVerify.Name)
					assert.True(t, found, "Repository %s not found in config after adding", tc.repoToVerify.Name)
					if found {
						assert.Equal(t, tc.repoToVerify.URL, repoConf.URL, "Repo URL not saved correctly")
						assert.Equal(t, tc.repoToVerify.Priority, repoConf.Priority, "Repo priority not saved correctly")
					}
				}
			}
			repoAddPriority = originalRepoAddPriority // Restore, mainly for clarity
		})
	}
}

func TestRepoListCmd(t *testing.T) {
	tempHome, cleanup := setupTempFPMConfig(t)
	defer cleanup()
	t.Logf("TestRepoListCmd using temp home: %s", tempHome)


	// Initial list: should be empty
	output, err := executeCommand(rootCmd, "repo", "list")
	require.NoError(t, err)
	assert.Contains(t, output, "No repositories configured.")

	// Helper to reset repoAddCmd flags and priority var
	resetAddCmd := func() {
		repoAddPriority = 0
		if repoAddCmd.Flags().Lookup("priority") != nil {
			repoAddCmd.Flags().Lookup("priority").Value.Set(repoAddCmd.Flags().Lookup("priority").DefValue)
		}
	}

	// Add some repositories
	resetAddCmd()
	_, err = executeCommand(rootCmd, "repo", "add", "repo1", "url1", "--priority", "10")
	require.NoError(t, err)

	resetAddCmd()
	_, err = executeCommand(rootCmd, "repo", "add", "repo2", "url2", "--priority", "5")
	require.NoError(t, err)

	resetAddCmd()
	_, err = executeCommand(rootCmd, "repo", "add", "repo3", "url3") // Default priority 0
	require.NoError(t, err)


	// List again
	output, err = executeCommand(rootCmd, "repo", "list")
	require.NoError(t, err)

	// Output should be sorted: repo3 (prio 0), then repo2 (prio 5), then repo1 (prio 10)
	expectedLines := []string{
		// Header lines are present in the actual output, so we check for content lines
		// "NAME                 URL                                                PRIORITY",
		// "----                 ---                                                --------",
		"repo3                url3                                               0",
		"repo2                url2                                               5",
		"repo1                url1                                               10",
	}

	normalizedOutput := strings.ReplaceAll(strings.TrimSpace(output), "\r\n", "\n")
	actualLines := strings.Split(normalizedOutput, "\n")

	// Check if the content lines are present and in order
	lineIdx := 0
	for _, expectedLine := range expectedLines {
		foundLine := false
		for ; lineIdx < len(actualLines); lineIdx++ {
			if strings.Contains(actualLines[lineIdx], strings.Fields(expectedLine)[0]) { // Check based on repo name primarily
				trimmedActual := strings.TrimSpace(actualLines[lineIdx])
				trimmedExpected := strings.TrimSpace(expectedLine)
				// Normalize spacing for comparison, as Printf formatting can be tricky
				assert.Equal(t,
					strings.Join(strings.Fields(trimmedExpected), " "),
					strings.Join(strings.Fields(trimmedActual), " "),
					"Sorted list order or content mismatch")
				foundLine = true
				lineIdx++ // Move to next actual line for next expected line
				break
			}
		}
		assert.True(t, foundLine, "Expected line containing '%s' not found in order", strings.Fields(expectedLine)[0])
	}
}

// TODO: Add TestRepoRemoveCmd
// TODO: Add TestRepoUpdateCmd (if/when implemented)
// Ensure that runtime.GOOS is imported if not already present.
// It's used in setupTempFPMConfig.
// encoding/json might be needed if we directly assert config file content.
// io and os/exec are not directly used in repo_test.go but in helpers it might call.
// The executeCommand helper uses bytes.Buffer.
