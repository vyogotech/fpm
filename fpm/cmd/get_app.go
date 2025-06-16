package cmd

import (
	"fmt"
	"net/http"
	"strings"
	"time"
	// "os" // Not directly used, but path/filepath might be useful if more path manipulation was needed
	// "path/filepath" // Not strictly needed for this implementation based on current plan

	"fpm/internal/appstore"
	"fpm/internal/config"
	"fpm/internal/repository"

	"github.com/spf13/cobra"
)

var getAppCmd = &cobra.Command{
	Use:   "get-app <repository_name>/<org>/<app_name>[:<version>]",
	Short: "Download app from a specific repository to the local FPM app store",
	Long: `Downloads the specified application package from a named remote repository
and installs it into the local FPM application store (~/.fpm/apps).
It does not install the application into any specific Frappe bench.
If version is not specified, 'latest' is assumed.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		fullIdentifier := args[0]

		// Parse fullIdentifier: <repository_name>/<org>/<app_name>[:<version>]
		parts := strings.SplitN(fullIdentifier, "/", 2)
		if len(parts) < 2 {
			return fmt.Errorf("invalid identifier format. Expected <repository_name>/<org>/<app_name>[:<version>], got '%s'", fullIdentifier)
		}
		repoName := strings.TrimSpace(parts[0])
		packageIdentifier := parts[1]

		if repoName == "" {
			return fmt.Errorf("repository name cannot be empty in identifier '%s'", fullIdentifier)
		}

		// Parse packageIdentifier: <org>/<app_name>[:<version>]
		var org, appName, version string
		appParts := strings.SplitN(packageIdentifier, "/", 2)
		if len(appParts) < 2 {
			return fmt.Errorf("invalid package component format. Expected <org>/<app_name>[:<version>], got '%s'", packageIdentifier)
		}
		org = strings.TrimSpace(appParts[0])

		nameAndVersion := strings.SplitN(appParts[1], ":", 2)
		appName = strings.TrimSpace(nameAndVersion[0])
		if len(nameAndVersion) == 2 {
			version = strings.TrimSpace(nameAndVersion[1])
		} else {
			version = "latest" // Default to latest if no version is specified
		}

		if org == "" || appName == "" {
			return fmt.Errorf("org and appName cannot be empty in identifier '%s'", fullIdentifier)
		}
		if version == "" { // Handles case like "repo/org/app:"
			version = "latest"
		}


		cfg, err := config.InitConfig()
		if err != nil {
			return fmt.Errorf("failed to initialize FPM configuration: %w", err)
		}

		repoConfig, exists := cfg.Repositories[repoName]
		if !exists {
			return fmt.Errorf("repository '%s' not configured. Use 'fpm repo add %s <url>' to configure it", repoName, repoName)
		}

		httpClient := &http.Client{Timeout: 120 * time.Second} // Shared client

		fmt.Printf("Fetching %s/%s (version: '%s') from repository %s (%s)...\n", org, appName, version, repoName, repoConfig.URL)
		downloadedPkg, err := repository.FindPackageInSpecificRepo(repoName, repoConfig.URL, org, appName, version, httpClient)
		if err != nil {
			return fmt.Errorf("failed to find or download package from repository %s: %w", repoName, err)
		}
		// Note: downloadedPkg.Org, downloadedPkg.AppName, downloadedPkg.Version are the canonical values from metadata
		fmt.Printf("Package %s/%s version %s downloaded to cache: %s.\n", downloadedPkg.Org, downloadedPkg.AppName, downloadedPkg.Version, downloadedPkg.LocalPath)
		fmt.Printf("Installing to local FPM app store...\n")

		// ManageAppInLocalStore will read metadata again, which is fine. It ensures consistency.
		installedOrg, installedAppName, installedAppVersion, _, _, err := appstore.ManageAppInLocalStore(downloadedPkg.LocalPath, cfg)
		if err != nil {
			return fmt.Errorf("failed to install downloaded package %s to local FPM app store: %w", downloadedPkg.LocalPath, err)
		}

		fmt.Printf("App %s/%s version %s successfully fetched from %s and installed to local FPM app store.\n",
			installedOrg, installedAppName, installedAppVersion, repoName)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(getAppCmd)
}
