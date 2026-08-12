package cmd

import (
	"fmt"
	"os"

	"github.com/common-nighthawk/go-figure"
	"github.com/spf13/cobra"
)

// version and commit are stamped at build time by the Makefile, so a released binary can
// identify itself. A build made with plain `go build` reports "dev".
var (
	version = "dev"
	commit  = "unknown"
)

// VersionString renders the build identity reported by `fpm --version`.
func VersionString() string {
	if commit == "unknown" || commit == "" {
		return version
	}
	return fmt.Sprintf("%s (commit %s)", version, commit)
}

var rootCmd = &cobra.Command{
	Use:     "fpm",
	Short:   "Vyogo FPM - Frappe Package Manager CLI",
	Version: VersionString(),
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	myFigure := figure.NewFigure("Vyogo FPM", "", true)
	rootCmd.SetVersionTemplate("fpm {{.Version}}\n")
	rootCmd.Long = fmt.Sprintf("\n%s\n\nFPM (Frappe Package Manager) is a command-line interface to manage Frappe applications,\nproviding package creation, installation, and repository management\nto streamline Frappe app deployment.", myFigure.String())
	// Here you will define your flags and configuration settings.
	// Cobra supports persistent flags, which, if defined here,
	// will be global for your application.

	// rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.fpm.yaml)")

	// Cobra also supports local flags, which will only run
	// when this action is called directly.
	// rootCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}
