package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"fpm/internal/config"
	"fpm/internal/metadata"
	"fpm/internal/wheels"

	"github.com/spf13/cobra"
)

var depsCmd = &cobra.Command{
	Use:   "deps <package>",
	Short: "Inspect the dependencies a package declares and bundles",
	Long: `Shows the Python dependencies a package declares, and whether it bundles them
for offline installation.

The package may be a path to a local .fpm file, or an identifier resolved from the local
FPM app store in the form <org>/<app> or <org>/<app>==<version>. Without a version, the
latest in the local store is used.

Dependencies are read from the requirements.txt and pyproject.toml the package ships, so
this reports what an install would actually resolve, not what the source tree declares
today.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		fpmPath, err := resolveDepsTarget(args[0])
		if err != nil {
			return err
		}

		meta, err := metadata.ReadMetadataFromFPMArchive(fpmPath)
		if err != nil {
			return fmt.Errorf("failed to read metadata from %s: %w", fpmPath, err)
		}

		req, bundled, err := wheels.CollectFromArchive(fpmPath)
		if err != nil {
			return fmt.Errorf("failed to read dependencies from %s: %w", fpmPath, err)
		}

		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Package:  %s/%s\n", meta.Org, meta.AppName)
		fmt.Fprintf(out, "Version:  %s\n", meta.PackageVersion)
		if meta.PackageType != "" {
			fmt.Fprintf(out, "Type:     %s\n", meta.PackageType)
		}
		fmt.Fprintf(out, "Source:   %s\n", fpmPath)

		fmt.Fprintf(out, "\nDeclared Python dependencies")
		if len(req.Sources) > 0 {
			fmt.Fprintf(out, " (from %s)", req.Describe())
		}
		fmt.Fprintln(out, ":")
		if req.Empty() {
			fmt.Fprintf(out, "  none declared in %s or %s\n",
				wheels.RequirementsFileName, wheels.PyProjectFileName)
		} else {
			for _, spec := range req.Specs {
				fmt.Fprintf(out, "  %s\n", spec)
			}
		}

		fmt.Fprintln(out, "\nBundled dependencies:")
		if len(bundled) == 0 {
			fmt.Fprintf(out, "  none - installing this package resolves dependencies from the network\n")
		} else {
			platform := meta.WheelPlatform
			if platform == "" {
				platform = "unspecified"
			}
			fmt.Fprintf(out, "  %d wheel(s), built for %s\n", len(bundled), platform)
			for _, w := range bundled {
				fmt.Fprintf(out, "    %s\n", w)
			}
		}

		// FPM-level package dependencies are a separate concept from Python
		// dependencies and are not resolved yet; report them only when present so the
		// output does not imply support that does not exist.
		if len(meta.Dependencies) > 0 {
			fmt.Fprintln(out, "\nDeclared FPM package dependencies:")
			names := make([]string, 0, len(meta.Dependencies))
			for name := range meta.Dependencies {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				fmt.Fprintf(out, "  %s %s\n", name, meta.Dependencies[name])
			}
			fmt.Fprintln(out, "  (not resolved during install)")
		}

		return nil
	},
}

// resolveDepsTarget turns a user-supplied target into a path to an .fpm file. The target
// is either a path to a package, or an identifier resolved from the local app store.
func resolveDepsTarget(target string) (string, error) {
	// A path to an existing .fpm file is used directly.
	if strings.HasSuffix(strings.ToLower(target), ".fpm") {
		abs, err := filepath.Abs(target)
		if err != nil {
			return "", fmt.Errorf("failed to resolve path %s: %w", target, err)
		}
		if _, err := os.Stat(abs); err != nil {
			return "", fmt.Errorf("package file not found: %s", abs)
		}
		return abs, nil
	}

	org, appName, version, err := parseDepsIdentifier(target)
	if err != nil {
		return "", err
	}

	cfg, err := config.InitConfig()
	if err != nil {
		return "", fmt.Errorf("failed to initialize FPM configuration: %w", err)
	}

	if version == "" || version == "latest" {
		resolved, err := resolveLatestVersionFromLocalStore(cfg.AppsBasePath, org, appName)
		if err != nil {
			return "", fmt.Errorf("failed to resolve latest version for %s/%s: %w", org, appName, err)
		}
		if resolved == "" {
			return "", fmt.Errorf("no versions of %s/%s found in the local FPM app store at %s. "+
				"Install or package it first, or pass a path to an .fpm file",
				org, appName, cfg.AppsBasePath)
		}
		version = resolved
	}

	versionDir := filepath.Join(cfg.AppsBasePath, org, appName, version)
	fpmPath := filepath.Join(versionDir, fmt.Sprintf("_%s-%s.fpm", appName, version))
	if _, err := os.Stat(fpmPath); err != nil {
		return "", fmt.Errorf("package %s/%s version %s not found in the local FPM app store (expected %s)",
			org, appName, version, fpmPath)
	}
	return fpmPath, nil
}

// parseDepsIdentifier splits <org>/<app>[==<version>] into its parts.
func parseDepsIdentifier(identifier string) (org, appName, version string, err error) {
	parts := strings.Split(identifier, "/")
	if len(parts) != 2 {
		return "", "", "", fmt.Errorf("invalid package identifier %q. "+
			"Expected a path to an .fpm file, or <org>/<app>[==<version>]", identifier)
	}
	org = strings.TrimSpace(parts[0])

	appAndVersion := strings.SplitN(parts[1], "==", 2)
	appName = strings.TrimSpace(appAndVersion[0])
	if len(appAndVersion) == 2 {
		version = strings.TrimSpace(appAndVersion[1])
	}

	if org == "" || appName == "" {
		return "", "", "", fmt.Errorf("invalid package identifier %q: org and app name are both required", identifier)
	}
	return org, appName, version, nil
}

func init() {
	rootCmd.AddCommand(depsCmd)
}
