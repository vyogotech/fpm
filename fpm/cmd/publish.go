package cmd

import (
	"fmt"
	"github.com/spf13/cobra"
)

var publishCmd = &cobra.Command{
	Use:   "publish [fpm-file]",
	Short: "Publish a Frappe application package to a repository",
	Long:  `Uploads a .fpm package file to a configured Frappe package repository.`,
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("fpm publish called for file:", args[0])
		// Logic for publishing will go here
	},
}

func init() {
	rootCmd.AddCommand(publishCmd)
	// Add flags for publishCmd here, e.g.:
	// publishCmd.Flags().StringP("repo", "r", "", "Repository to publish to")
	// publishCmd.MarkFlagRequired("repo")
}
