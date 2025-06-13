package cmd

import (
	"fmt"
	"github.com/spf13/cobra"
)

var repoAddCmd = &cobra.Command{
	Use:   "add [repo-name] [repo-url]",
	Short: "Add a new Frappe package repository",
	Long:  `Adds a new Frappe package repository to the FPM configuration.`,
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("fpm repo add called for name: %s, url: %s\n", args[0], args[1])
		// Logic for adding a repository will go here
	},
}

func init() {
	repoCmd.AddCommand(repoAddCmd) // Add 'addCmd' as a subcommand of 'repoCmd'
}
