package cmd

import (
	"fmt"
	"github.com/spf13/cobra"
)

var depsCmd = &cobra.Command{
	Use:   "deps [package-name]",
	Short: "Inspect package dependencies",
	Long:  `Shows the dependency tree for a given package or provides other dependency-related information.`,
	Args:  cobra.MinimumNArgs(0), // Or ExactArgs(1) if a package is always required
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("fpm deps called")
		if len(args) > 0 {
			fmt.Println("Package to inspect:", args[0])
		}
		// Logic for dependency inspection will go here
	},
}

func init() {
	rootCmd.AddCommand(depsCmd)
	// depsCmd.Flags().Bool("tree", false, "Display dependencies as a tree")
}
