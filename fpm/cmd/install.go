package cmd

import (
	"fmt"
	"github.com/spf13/cobra"
)

var installCmd = &cobra.Command{
	Use:   "install [package-name]",
	Short: "Install a Frappe application package",
	Long: `Installs a Frappe application from an .fpm file or a repository.
Example: fpm install my-app-1.0.0.fpm
         fpm install custom-app==1.0.0 --site mysite`,
	Args: cobra.MinimumNArgs(0), // Can be 0 if installing from a repo with version, or 1 if a file
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("fpm install called")
		if len(args) > 0 {
			fmt.Println("Package to install:", args[0])
		}
		// Logic for installation will go here
	},
}

func init() {
	rootCmd.AddCommand(installCmd)
	// Add flags for installCmd here, e.g.:
	// installCmd.Flags().String("site", "", "Specify the site for installation")
}
