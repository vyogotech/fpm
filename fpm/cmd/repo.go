package cmd

import (
	"github.com/spf13/cobra"
)

var repoCmd = &cobra.Command{
	Use:   "repo",
	Short: "Manage Frappe package repositories",
	Long:  `Provides commands to list, add, remove, and configure Frappe package repositories.`,
	// No Run function for the base 'repo' command itself, it's a group.
}

func init() {
	rootCmd.AddCommand(repoCmd)
}
