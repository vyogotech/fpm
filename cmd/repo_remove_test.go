package cmd

import (
	"strings"
	"testing"

	"fpm/internal/config"

	"github.com/stretchr/testify/require"
)

func TestRepoRemoveCommand(t *testing.T) {
	_, cleanup := setupTempFPMConfig(t)
	defer cleanup()

	SharedResetRepoCmdFlags()
	_, err := SharedExecuteCommand(rootCmd, "repo", "add", "keepme", "http://keep.example")
	require.NoError(t, err)
	SharedResetRepoCmdFlags()
	_, err = SharedExecuteCommand(rootCmd, "repo", "add", "dropme", "http://drop.example")
	require.NoError(t, err)

	output, err := SharedExecuteCommand(rootCmd, "repo", "remove", "dropme")
	require.NoError(t, err)
	require.Contains(t, output, "removed")

	cfg, err := config.LoadConfig()
	require.NoError(t, err)
	require.NotContains(t, cfg.Repositories, "dropme")
	require.Contains(t, cfg.Repositories, "keepme", "removing one repository must not affect others")
}

// TestRepoRemoveClearsDefault covers the trap: a default pointing at a removed repository
// would otherwise fail later with a message about a missing repository the user thought
// they had configured.
func TestRepoRemoveClearsDefault(t *testing.T) {
	_, cleanup := setupTempFPMConfig(t)
	defer cleanup()

	SharedResetRepoCmdFlags()
	_, err := SharedExecuteCommand(rootCmd, "repo", "add", "primary", "http://primary.example")
	require.NoError(t, err)
	_, err = SharedExecuteCommand(rootCmd, "repo", "default", "primary")
	require.NoError(t, err)

	output, err := SharedExecuteCommand(rootCmd, "repo", "remove", "primary")
	require.NoError(t, err)
	require.Contains(t, output, "default publish repository")

	cfg, err := config.LoadConfig()
	require.NoError(t, err)
	require.Empty(t, cfg.DefaultPublishRepository, "a dangling default must not survive removal")
}

// TestRepoRemoveKeepsOtherDefault checks the default is only cleared when it is the
// repository actually being removed.
func TestRepoRemoveKeepsOtherDefault(t *testing.T) {
	_, cleanup := setupTempFPMConfig(t)
	defer cleanup()

	for _, name := range []string{"alpha", "beta"} {
		SharedResetRepoCmdFlags()
		_, err := SharedExecuteCommand(rootCmd, "repo", "add", name, "http://"+name+".example")
		require.NoError(t, err)
	}
	_, err := SharedExecuteCommand(rootCmd, "repo", "default", "alpha")
	require.NoError(t, err)

	_, err = SharedExecuteCommand(rootCmd, "repo", "remove", "beta")
	require.NoError(t, err)

	cfg, err := config.LoadConfig()
	require.NoError(t, err)
	require.Equal(t, "alpha", cfg.DefaultPublishRepository)
}

func TestRepoRemoveUnknownRepository(t *testing.T) {
	_, cleanup := setupTempFPMConfig(t)
	defer cleanup()

	_, err := SharedExecuteCommand(rootCmd, "repo", "remove", "ghost")
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "not found"),
		"error should say the repository is unknown, got: %v", err)
}
