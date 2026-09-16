package cmd

// Publishing a dependency-closure bundle as ONE OCI artifact: the "fat package"
// for a stack whose apps are always installed together. The counterpart to
// `fpm install <org>/<app>==<v>` recognising a bundle and installing it in order.
//
// Only OCI repositories can carry a bundle. The HTTP registry's layout is one
// file per package version, so a bundle would have to be flattened back into
// individual packages there, which is the thing a bundle exists to avoid.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"fpm/internal/bundle"
	"fpm/internal/config"
	"fpm/internal/ociregistry"

	"github.com/spf13/cobra"
)

// resolvePublishRepo picks the repository to publish to, honouring --repo then
// the configured default. Shared with the single-package path's rules.
func resolvePublishRepo(cfg *config.FPMConfig, repoName string) (config.RepositoryConfig, error) {
	if repoName != "" {
		repo, found := cfg.Repositories[repoName]
		if !found {
			return config.RepositoryConfig{}, fmt.Errorf("specified repository '%s' not found in FPM configuration", repoName)
		}
		return repo, nil
	}
	if cfg.DefaultPublishRepository != "" {
		repo, found := cfg.Repositories[cfg.DefaultPublishRepository]
		if !found {
			return config.RepositoryConfig{}, fmt.Errorf("default publish repository '%s' not found in FPM configuration", cfg.DefaultPublishRepository)
		}
		return repo, nil
	}
	return config.RepositoryConfig{}, fmt.Errorf("no repository specified with --repo and no default publish repository is set. Use 'fpm repo add' and 'fpm repo default'")
}

// parseBundleCoordinate splits "org/name" for --as. An empty value means the
// bundle publishes under its root package's own coordinate.
func parseBundleCoordinate(as string, m *bundle.Manifest) (org, name string, err error) {
	if as == "" {
		if m.Root.Org == "" || m.Root.App == "" {
			return "", "", fmt.Errorf("bundle root has no org/app; name the bundle explicitly with --as <org>/<name>")
		}
		return m.Root.Org, m.Root.App, nil
	}
	parts := strings.Split(as, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("invalid --as value %q: expected <org>/<name>", as)
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
}

// publishBundle pushes a bundle directory as a single OCI artifact.
func publishBundle(cmd *cobra.Command, cfg *config.FPMConfig, dir, as, repoName, version string, force bool) error {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("failed to resolve --from-bundle path: %w", err)
	}
	m, err := bundle.Read(absDir)
	if err != nil {
		return err
	}

	org, name, err := parseBundleCoordinate(as, m)
	if err != nil {
		return err
	}
	if version == "" {
		version = m.Root.Version
	}
	if version == "" {
		return fmt.Errorf("bundle root has no version; pass --bundle-version")
	}

	targetRepo, err := resolvePublishRepo(cfg, repoName)
	if err != nil {
		return err
	}
	if targetRepo.Type != "oci" {
		return fmt.Errorf("repository %q is type %q; a bundle can only be published to an OCI repository, "+
			"because the HTTP registry stores one file per package version and would have to flatten it back "+
			"into individual packages", targetRepo.Name, targetRepo.Type)
	}

	out := cmd.OutOrStdout()
	shipped := m.Shipped()
	fmt.Fprintf(out, "Publishing bundle from %s to %s (%s)\n", absDir, targetRepo.Name, targetRepo.URL)
	fmt.Fprintf(out, "  as %s/%s:%s — %d package(s), install order:\n", org, name, version, len(shipped))
	for i, e := range m.InstallOrder {
		note := e.File
		if !e.Shipped() {
			note = "provided by the bench, not shipped"
		}
		fmt.Fprintf(out, "    %d. %s  %s\n", i+1, e.Identifier(), note)
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	exists, _, err := ociregistry.Exists(ctx, targetRepo, org, name, version)
	if err != nil {
		return fmt.Errorf("failed to check whether %s/%s:%s exists in %s: %w", org, name, version, targetRepo.Name, err)
	}
	if exists && !force {
		return fmt.Errorf("version %s for %s/%s already exists in OCI repository %s (use --force to overwrite)", version, org, name, targetRepo.Name)
	}

	desc, err := ociregistry.PushBundle(ctx, targetRepo, absDir, m, org, name, version, nil)
	if err != nil {
		return fmt.Errorf("failed to publish bundle to OCI repository %s: %w", targetRepo.Name, err)
	}
	fmt.Fprintf(out, "Successfully published bundle %s/%s version %s to OCI repository %s.\n", org, name, version, targetRepo.Name)
	fmt.Fprintf(out, "  Manifest Digest: %s (%d bytes)\n", desc.Digest.String(), desc.Size)
	fmt.Fprintf(out, "  Install with: fpm install %s/%s==%s --bench-path <bench> --site <site>\n", org, name, version)
	return nil
}
