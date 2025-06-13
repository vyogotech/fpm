package cmd

import (
	"fmt"
	"github.com/spf13/cobra"
)

var packageCmd = &cobra.Command{
	Use:   "package",
	Short: "Package a Frappe application",
	Long:  `Packages a Frappe application from a local development directory into an .fpm file.`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("fpm package called")
		// Logic for packaging will go here
	},
}

func init() {
	rootCmd.AddCommand(packageCmd)
	// Add flags for packageCmd here, e.g.:
	// packageCmd.Flags().StringP("version", "v", "", "Version of the package (e.g., 1.2.3)")
	// packageCmd.MarkFlagRequired("version")
}
