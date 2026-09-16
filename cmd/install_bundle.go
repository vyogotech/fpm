package cmd

// Installing a dependency-closure bundle straight from an OCI registry.
//
// `fpm install <org>/<name>==<version>` cannot know in advance whether that
// coordinate holds a single package or a whole stack, so it probes: if the
// manifest is a bundle, the closure is pulled into a temporary directory and
// handed to the SAME installer a local bundle directory uses. Anything else
// falls through to the single-package path untouched.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"fpm/internal/config"
	"fpm/internal/ociregistry"
	"fpm/internal/semver"

	"github.com/spf13/cobra"
)

// identifierPart is what an org or app name may contain. Deliberately strict:
// the loose "does it contain a slash" test accepted "./bundle-dir" as
// org "." / app "bundle-dir", so `fpm install ./some-dir` would have gone to
// the network instead of looking on disk. Frappe app names are Python module
// names and registry namespaces are alphanumeric, so nothing legitimate is
// excluded — and a path component (".", "..", "dist", "pkg.fpm") is.
var identifierPart = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]*$`)

// parseRemoteIdentifier splits "<org>/<app>[==<version>]". ok is false for
// anything not shaped like a remote coordinate — a path, most importantly.
// The version is not constrained here: semver carries dots and dashes.
func parseRemoteIdentifier(arg string) (org, app, version string, ok bool) {
	parts := strings.Split(arg, "/")
	if len(parts) != 2 {
		return "", "", "", false
	}
	org = strings.TrimSpace(parts[0])
	rest := strings.SplitN(parts[1], "==", 2)
	app = strings.TrimSpace(rest[0])
	if len(rest) == 2 {
		version = strings.TrimSpace(rest[1])
	}
	if !identifierPart.MatchString(org) || !identifierPart.MatchString(app) {
		return "", "", "", false
	}
	return org, app, version, true
}

// ociRepositoriesToProbe returns the OCI repositories to look in, honouring
// --repo when given. Order is stable so the same coordinate resolves the same
// way run to run.
func ociRepositoriesToProbe(cfg *config.FPMConfig, repoName string) []config.RepositoryConfig {
	if repoName != "" {
		if r, ok := config.GetRepository(cfg, repoName); ok && r.Type == "oci" {
			return []config.RepositoryConfig{r}
		}
		return nil
	}
	var out []config.RepositoryConfig
	for _, r := range cfg.Repositories {
		if r.Type == "oci" {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// resolveBundleVersion picks the version to pull when the caller named none.
func resolveBundleVersion(ctx context.Context, repo config.RepositoryConfig, org, app string) (string, error) {
	meta, found, err := ociregistry.FetchMetadata(ctx, repo, org, app)
	if err != nil || !found || meta == nil {
		return "", fmt.Errorf("no versions found for %s/%s in %s", org, app, repo.Name)
	}
	versions := make([]string, 0, len(meta.Versions))
	for v := range meta.Versions {
		versions = append(versions, v)
	}
	latest := semver.Latest(versions)
	if latest == "" {
		return "", fmt.Errorf("no installable version found for %s/%s in %s", org, app, repo.Name)
	}
	return latest, nil
}

// tryRemoteBundle pulls <arg> as a bundle if any configured OCI repository
// holds one at that coordinate. It returns ok=false — with no error — when the
// coordinate is a single package or is not found, so the caller proceeds with
// the ordinary install path.
//
// The caller is responsible for removing dir.
func tryRemoteBundle(cmd *cobra.Command, cfg *config.FPMConfig, arg, repoName string) (dir string, ok bool, err error) {
	org, app, version, parsed := parseRemoteIdentifier(arg)
	if !parsed {
		return "", false, nil
	}
	repos := ociRepositoriesToProbe(cfg, repoName)
	if len(repos) == 0 {
		return "", false, nil
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	for _, repo := range repos {
		v := version
		if v == "" {
			resolved, rerr := resolveBundleVersion(ctx, repo, org, app)
			if rerr != nil {
				continue // not in this repository; try the next
			}
			v = resolved
		}

		tmp, terr := os.MkdirTemp("", "fpm-bundle-")
		if terr != nil {
			return "", false, fmt.Errorf("failed to create a temporary directory for the bundle: %w", terr)
		}
		m, perr := ociregistry.PullBundle(ctx, repo, org, app, v, tmp)
		if perr != nil {
			_ = os.RemoveAll(tmp)
			if errors.Is(perr, ociregistry.ErrNotBundle) {
				// Definitively a single package — stop probing and let the normal
				// path handle it, rather than reporting it as missing.
				return "", false, nil
			}
			continue // not found here, or unreadable; try the next repository
		}

		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Resolved %s/%s==%s to a bundle in %s — %d package(s):\n", org, app, v, repo.Name, len(m.Shipped()))
		for i, e := range m.InstallOrder {
			note := e.File
			if !e.Shipped() {
				note = "provided by the bench"
			}
			fmt.Fprintf(out, "  %d. %s  %s\n", i+1, e.Identifier(), note)
		}
		return tmp, true, nil
	}
	return "", false, nil
}
