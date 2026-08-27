package cmd

import (
	"fmt"
	"strings"

	"fpm/internal/config"
	"fpm/internal/repository"

	"github.com/spf13/cobra"
)

var repoCmd = &cobra.Command{
	Use:   "repo",
	Short: "Manage FPM repositories",                                // Updated short description
	Long:  `Add, list, remove, or update FPM package repositories.`, // Updated long description
	// No Run function for the base 'repo' command itself, it's a group.
}

var (
	repoAddPriority  int    // Priority flag for the add command
	repoAddUsername  string // Username for repositories requiring authentication
	repoAddType      string // Repository backend type: "http" or "oci"
	repoAddPlainHTTP bool   // Use plain HTTP for OCI registries
	repoAddInsecure  bool   // Skip TLS verification
)

// repoAddCmd represents the repo add command
var repoAddCmd = &cobra.Command{
	Use:   "add <name> <url>",
	Short: "Add an FPM repository",
	Long:  `Adds a new FPM package repository to the local configuration.`,
	Args:  cobra.ExactArgs(2), // Ensures exactly two arguments: name and url
	RunE: func(cmd *cobra.Command, args []string) error {
		repoName := args[0]
		repoURL := args[1]

		cfg, err := config.InitConfig()
		if err != nil {
			return fmt.Errorf("failed to initialize FPM configuration: %w", err)
		}

		backendType := strings.ToLower(strings.TrimSpace(repoAddType))
		if backendType != "" && backendType != "http" && backendType != "webdav" && backendType != "oci" {
			return fmt.Errorf("invalid repository type %q: expected 'http' or 'oci'", repoAddType)
		}
		if backendType == "webdav" {
			backendType = "http"
		}

		newRepo := config.RepositoryConfig{
			Name:      repoName,
			URL:       repoURL,
			Priority:  repoAddPriority,
			Username:  repoAddUsername,
			Type:      backendType,
			PlainHTTP: repoAddPlainHTTP,
			Insecure:  repoAddInsecure,
		}

		if err := config.AddRepository(cfg, newRepo); err != nil {
			return fmt.Errorf("failed to add repository '%s': %w", repoName, err)
		}

		if err := config.SaveConfig(cfg); err != nil {
			return fmt.Errorf("failed to save updated FPM configuration: %w", err)
		}

		if backendType == "oci" {
			fmt.Printf("Repository '%s' (%s, type: oci) added successfully with priority %d.\n", repoName, repoURL, repoAddPriority)
		} else {
			fmt.Printf("Repository '%s' (%s) added successfully with priority %d.\n", repoName, repoURL, repoAddPriority)
		}
		if repoAddUsername != "" {
			// Say where the password comes from, since it is deliberately not stored.
			fmt.Printf("Authenticating as '%s'. Supply the password via %s (or %s), "+
				"or you will be prompted when running interactively.\n",
				repoAddUsername, repository.PasswordEnvVar(repoName), repository.PasswordEnvFallback)
		}
		return nil
	},
}

var repoSetDefaultCmd = &cobra.Command{
	Use:   "default [repo_name]",
	Short: "Set or show the default FPM repository for publishing",
	Long: `Sets the specified repository name as the default for 'fpm publish' operations.
If no repository name is provided, it displays the current default publish repository.`,
	Args: cobra.MaximumNArgs(1), // 0 or 1 argument
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.InitConfig()
		if err != nil {
			return fmt.Errorf("failed to initialize FPM configuration: %w", err)
		}

		if len(args) == 1 { // repo_name is provided, set it
			repoName := args[0]
			// Check if the repo exists
			if _, exists := cfg.Repositories[repoName]; !exists {
				// Suggest listing available repositories
				return fmt.Errorf("repository '%s' not found. Use 'fpm repo list' to see available repositories or 'fpm repo add %s <url>' to add it first", repoName, repoName)
			}
			cfg.DefaultPublishRepository = repoName
			if err := config.SaveConfig(cfg); err != nil {
				return fmt.Errorf("failed to save updated FPM configuration: %w", err)
			}
			fmt.Printf("Default publish repository set to '%s'.\n", repoName)
		} else { // No repo_name provided, show current default
			if cfg.DefaultPublishRepository == "" {
				fmt.Println("No default publish repository is currently set.")
				fmt.Println("Use 'fpm repo default <repo_name>' to set one.")
			} else {
				// Verify the currently set default repository still exists
				if _, exists := cfg.Repositories[cfg.DefaultPublishRepository]; !exists {
					fmt.Printf("Warning: The currently set default repository '%s' no longer exists in the configuration.\n", cfg.DefaultPublishRepository)
					fmt.Println("Please set a new default using 'fpm repo default <repo_name>'.")
					// Optionally, clear the invalid default here:
					// cfg.DefaultPublishRepository = ""
					// config.SaveConfig(cfg) // Persist the clearing
				} else {
					fmt.Printf("Current default publish repository: %s\n", cfg.DefaultPublishRepository)
				}
			}
		}
		return nil
	},
}

// repoRemoveCmd represents the repo remove command
var repoRemoveCmd = &cobra.Command{
	Use:     "remove <name>",
	Aliases: []string{"rm"},
	Short:   "Remove a configured FPM repository",
	Long: `Removes an FPM package repository from the local configuration.

Packages already downloaded from the repository stay in the local FPM app store; only
the repository configuration is removed.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		repoName := args[0]

		cfg, err := config.InitConfig()
		if err != nil {
			return fmt.Errorf("failed to initialize FPM configuration: %w", err)
		}

		if !config.RemoveRepository(cfg, repoName) {
			return fmt.Errorf("repository '%s' not found. Use 'fpm repo list' to see configured repositories", repoName)
		}

		// A default pointing at a repository that no longer exists would fail later with
		// a confusing message, so clear it here and say so.
		clearedDefault := false
		if cfg.DefaultPublishRepository == repoName {
			cfg.DefaultPublishRepository = ""
			clearedDefault = true
		}

		if err := config.SaveConfig(cfg); err != nil {
			return fmt.Errorf("failed to save updated FPM configuration: %w", err)
		}

		fmt.Printf("Repository '%s' removed.\n", repoName)
		if clearedDefault {
			fmt.Printf("It was the default publish repository, so that is now unset. " +
				"Use 'fpm repo default <repo_name>' to choose another.\n")
		}
		return nil
	},
}

// repoListCmd represents the repo list command
var repoListCmd = &cobra.Command{
	Use:   "list",
	Short: "List configured FPM repositories",
	Long:  `Lists all FPM package repositories that are currently configured.`,
	Args:  cobra.NoArgs, // No arguments expected
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.InitConfig()
		if err != nil {
			return fmt.Errorf("failed to initialize FPM configuration: %w", err)
		}

		repos := config.ListRepositories(cfg) // This returns a sorted list

		if len(repos) == 0 {
			fmt.Println("No repositories configured.")
			return nil
		}

		// Print a header
		fmt.Printf("%-20s %-10s %-50s %-10s %s\n", "NAME", "TYPE", "URL", "PRIORITY", "USERNAME")
		fmt.Printf("%-20s %-10s %-50s %-10s %s\n", "----", "----", "---", "--------", "--------")

		for _, repo := range repos {
			username := repo.Username
			if username == "" {
				username = "-"
			}
			repoType := repo.Type
			if repoType == "" {
				repoType = "http"
			}
			fmt.Printf("%-20s %-10s %-50s %-10d %s\n", repo.Name, repoType, repo.URL, repo.Priority, username)
		}

		return nil
	},
}

func init() {
	// Flags for repoAddCmd
	repoAddCmd.Flags().StringVar(&repoAddUsername, "username", "", "Username for a repository requiring authentication (the password is supplied via environment or prompt, never stored)")
	repoAddCmd.Flags().IntVarP(&repoAddPriority, "priority", "p", 0, "Priority of the repository (lower number means higher priority)")
	repoAddCmd.Flags().StringVar(&repoAddType, "type", "", "Repository type: 'http' (WebDAV/Nginx, default) or 'oci' (OCI container registry)")
	repoAddCmd.Flags().BoolVar(&repoAddPlainHTTP, "plain-http", false, "Use plain HTTP for OCI registry connections (e.g. localhost:5000)")
	repoAddCmd.Flags().BoolVar(&repoAddInsecure, "insecure", false, "Skip TLS verification for OCI registry connections")

	// Add subcommands to repoCmd
	repoCmd.AddCommand(repoAddCmd)
	repoCmd.AddCommand(repoListCmd)
	repoCmd.AddCommand(repoRemoveCmd)
	repoCmd.AddCommand(repoSetDefaultCmd) // Add the new 'default' subcommand

	// Add repoCmd to rootCmd (this was already here, ensuring it stays)
	rootCmd.AddCommand(repoCmd)
}
