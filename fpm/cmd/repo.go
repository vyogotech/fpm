package cmd

import (
	"fmt"
	"fpm/internal/config"
	"github.com/spf13/cobra"
)

var repoCmd = &cobra.Command{
	Use:   "repo",
	Short: "Manage FPM repositories", // Updated short description
	Long:  `Add, list, remove, or update FPM package repositories.`, // Updated long description
	// No Run function for the base 'repo' command itself, it's a group.
}

var repoAddPriority int // Variable to hold the priority flag for the add command

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

		newRepo := config.RepositoryConfig{
			Name:     repoName,
			URL:      repoURL,
			Priority: repoAddPriority,
		}

		if err := config.AddRepository(cfg, newRepo); err != nil {
			return fmt.Errorf("failed to add repository '%s': %w", repoName, err)
		}

		if err := config.SaveConfig(cfg); err != nil {
			return fmt.Errorf("failed to save updated FPM configuration: %w", err)
		}

		fmt.Printf("Repository '%s' (%s) added successfully with priority %d.\n", repoName, repoURL, repoAddPriority)
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
		// Consider using a table library for nicer output if more fields are added later
		fmt.Printf("%-20s %-50s %s\n", "NAME", "URL", "PRIORITY")
		fmt.Printf("%-20s %-50s %s\n", "----", "---", "--------") // Simple separator

		for _, repo := range repos {
			fmt.Printf("%-20s %-50s %d\n", repo.Name, repo.URL, repo.Priority)
		}

		return nil
	},
}

func init() {
	// Flags for repoAddCmd
	repoAddCmd.Flags().IntVarP(&repoAddPriority, "priority", "p", 0, "Priority of the repository (lower number means higher priority)")

	// Add subcommands to repoCmd
	repoCmd.AddCommand(repoAddCmd)
	repoCmd.AddCommand(repoListCmd)
	repoCmd.AddCommand(repoSetDefaultCmd) // Add the new 'default' subcommand

	// Add repoCmd to rootCmd (this was already here, ensuring it stays)
	rootCmd.AddCommand(repoCmd)
}
