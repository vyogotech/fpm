package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"fpm/internal/config"
	"fpm/internal/metadata"
	"fpm/internal/repository"
	"fpm/internal/resolver"
	"fpm/internal/semver"
	"fpm/internal/wheels"

	"github.com/spf13/cobra"
)

var (
	existsCommit        string
	existsPlatform      string
	existsPythonVersion string
	existsRepo          string
	existsRemote        bool
	existsJSON          bool
)

// ExistsResult is the machine-readable answer of `fpm exists`.
type ExistsResult struct {
	Exists bool   `json:"exists"`
	Org    string `json:"org"`
	App    string `json:"app"`
	// Version is the version that matched, or the one asked for when nothing did.
	Version string `json:"version,omitempty"`
	// Source names where the match was found: "local-store" or "repo:<name>".
	Source             string `json:"source,omitempty"`
	CommitSHA          string `json:"commit_sha,omitempty"`
	GitRef             string `json:"git_ref,omitempty"`
	WheelPlatform      string `json:"wheel_platform,omitempty"`
	WheelPythonVersion string `json:"wheel_python_version,omitempty"`
	// Candidates lists versions that exist but did not satisfy the identity
	// constraints, so a caller can see what is published without a second query.
	Candidates []ExistsCandidate `json:"candidates,omitempty"`
	// Reason explains a negative answer.
	Reason string `json:"reason,omitempty"`
}

// ExistsCandidate is a published version considered by `fpm exists`.
type ExistsCandidate struct {
	Version            string `json:"version"`
	Source             string `json:"source"`
	CommitSHA          string `json:"commit_sha,omitempty"`
	WheelPlatform      string `json:"wheel_platform,omitempty"`
	WheelPythonVersion string `json:"wheel_python_version,omitempty"`
	Rejected           string `json:"rejected,omitempty"`
}

var existsCmd = &cobra.Command{
	Use:   "exists <org>/<app>[==<version>]",
	Short: "Check whether a package (optionally at a commit, for a platform) is already available, without downloading it",
	Long: `Answers "does <org>/<app> already exist?" from metadata alone, so build tooling can
skip a redundant package build.

The local FPM store is consulted first, then — with --remote — every configured
repository's package-metadata.json (never the artifact). Identity constraints narrow
the question:

  --commit <sha>          the package must have been built from this commit (prefix ok)
  --platform <tag>        its vendored wheels must target this platform tag
  --python-version <ver>  its vendored wheels must target this Python version

Without ==<version>, every version is considered and the newest match wins.

Exit status: 0 when a matching package exists, ` + fmt.Sprint(ExitNotFound) + ` when none does, 1 on error.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		org, app, version, err := parseDepsIdentifier(args[0])
		if err != nil {
			return err
		}
		if version == "latest" {
			version = ""
		}
		cfg, err := config.InitConfig()
		if err != nil {
			return fmt.Errorf("failed to initialize FPM configuration: %w", err)
		}

		result := lookupExists(cfg, org, app, version, existsQuery{
			commit: existsCommit, platform: existsPlatform, python: existsPythonVersion,
			remote: existsRemote, repo: existsRepo,
		})

		out := cmd.OutOrStdout()
		if existsJSON {
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			if err := enc.Encode(result); err != nil {
				return err
			}
		} else if result.Exists {
			fmt.Fprintf(out, "%s/%s==%s exists (%s)\n", result.Org, result.App, result.Version, result.Source)
			if result.CommitSHA != "" {
				fmt.Fprintf(out, "  commit:   %s\n", result.CommitSHA)
			}
			if result.WheelPlatform != "" {
				fmt.Fprintf(out, "  wheels:   %s", result.WheelPlatform)
				if result.WheelPythonVersion != "" {
					fmt.Fprintf(out, " (python %s)", result.WheelPythonVersion)
				}
				fmt.Fprintln(out)
			}
		} else {
			fmt.Fprintf(out, "%s/%s%s does not exist: %s\n", result.Org, result.App, versionSuffix(version), result.Reason)
			for _, c := range result.Candidates {
				fmt.Fprintf(out, "  candidate %s (%s): %s\n", c.Version, c.Source, c.Rejected)
			}
		}
		if !result.Exists {
			return fmt.Errorf("%w: %s/%s%s", errNotFound, org, app, versionSuffix(version))
		}
		return nil
	},
}

func versionSuffix(version string) string {
	if version == "" {
		return ""
	}
	return "==" + version
}

type existsQuery struct {
	commit, platform, python string
	remote                   bool
	repo                     string
}

// identity is the subset of metadata `fpm exists` matches on.
type identity struct {
	version, commit, ref, platform, python string
}

// reject explains why an identity fails the query, or "" when it matches.
func (q existsQuery) reject(id identity) string {
	if q.commit != "" {
		want := strings.ToLower(q.commit)
		have := strings.ToLower(id.commit)
		if have == "" {
			return "no commit recorded"
		}
		if !(have == want || (len(want) >= 7 && strings.HasPrefix(have, want))) {
			return fmt.Sprintf("commit %s, not %s", have, q.commit)
		}
	}
	if q.platform != "" {
		if id.platform == "" {
			// No vendored wheels: nothing platform-specific, installable anywhere.
		} else {
			matched := false
			for _, p := range append(wheels.ParseTag(id.platform), id.platform) {
				if p == q.platform {
					matched = true
				}
			}
			if !matched {
				return fmt.Sprintf("wheels target %s, not %s", id.platform, q.platform)
			}
		}
	}
	if q.python != "" && id.python != "" && id.python != q.python {
		return fmt.Sprintf("wheels target python %s, not %s", id.python, q.python)
	}
	return ""
}

func lookupExists(cfg *config.FPMConfig, org, app, version string, q existsQuery) ExistsResult {
	result := ExistsResult{Org: org, App: app, Version: version}
	type found struct {
		id     identity
		source string
	}
	var matches []found

	consider := func(id identity, source string) {
		if version != "" && id.version != version {
			return
		}
		if why := q.reject(id); why != "" {
			result.Candidates = append(result.Candidates, ExistsCandidate{
				Version: id.version, Source: source, CommitSHA: id.commit,
				WheelPlatform: id.platform, WheelPythonVersion: id.python, Rejected: why,
			})
			return
		}
		matches = append(matches, found{id, source})
	}

	// Local store.
	var orgs []string
	if org != "" {
		orgs = []string{org}
	}
	for _, o := range orgs {
		for _, v := range resolver.StoreVersions(cfg.AppsBasePath, o, app) {
			meta, err := metadata.LoadAppMetadata(filepath.Join(cfg.AppsBasePath, o, app, v))
			if err != nil {
				continue
			}
			consider(identity{v, meta.CommitSHA, meta.GitRef, meta.WheelPlatform, meta.WheelPythonVersion}, "local-store")
		}
	}

	// Repositories, metadata only.
	if q.remote && len(matches) == 0 {
		repos := config.ListRepositories(cfg)
		if q.repo != "" {
			if r, ok := config.GetRepository(cfg, q.repo); ok {
				repos = []config.RepositoryConfig{r}
			} else {
				result.Reason = fmt.Sprintf("repository %q is not configured", q.repo)
				return result
			}
		}
		for _, repo := range repos {
			client, err := resolver.NewHTTPClient(repo, 30*time.Second)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Skipping repository %s: %v\n", repo.Name, err)
				continue
			}
			pkg, ok, err := repository.FetchRemotePackageMetadata(repo.URL, org, app, client)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error querying repository %s: %v\n", repo.Name, err)
				continue
			}
			if !ok || pkg == nil {
				continue
			}
			for v, vm := range pkg.Versions {
				consider(identity{v, vm.CommitSHA, vm.GitRef, vm.WheelPlatform, vm.WheelPythonVersion}, "repo:"+repo.Name)
			}
		}
	}

	if len(matches) == 0 {
		switch {
		case len(result.Candidates) > 0:
			result.Reason = "published versions exist but none matches the requested identity"
		case q.remote:
			result.Reason = "not in the local FPM store or any configured repository"
		default:
			result.Reason = "not in the local FPM store (pass --remote to also query repositories)"
		}
		sort.Slice(result.Candidates, func(i, j int) bool {
			return semver.Compare(result.Candidates[i].Version, result.Candidates[j].Version) > 0
		})
		return result
	}

	// Newest matching version wins; local store beats a repository at equal version.
	sort.SliceStable(matches, func(i, j int) bool {
		if c := semver.Compare(matches[i].id.version, matches[j].id.version); c != 0 {
			return c > 0
		}
		return matches[i].source == "local-store" && matches[j].source != "local-store"
	})
	best := matches[0]
	result.Exists = true
	result.Version = best.id.version
	result.Source = best.source
	result.CommitSHA = best.id.commit
	result.GitRef = best.id.ref
	result.WheelPlatform = best.id.platform
	result.WheelPythonVersion = best.id.python
	result.Candidates = nil
	return result
}

func init() {
	existsCmd.Flags().StringVar(&existsCommit, "commit", "", "Require the package to have been built from this git commit SHA (a prefix of at least 7 characters is accepted)")
	existsCmd.Flags().StringVar(&existsPlatform, "platform", "", "Require vendored wheels for this platform tag (e.g. manylinux2014_x86_64)")
	existsCmd.Flags().StringVar(&existsPythonVersion, "python-version", "", "Require vendored wheels for this Python version (e.g. 3.11)")
	existsCmd.Flags().StringVar(&existsRepo, "repo", "", "Only query this configured repository (implies --remote)")
	existsCmd.Flags().BoolVar(&existsRemote, "remote", false, "Also query configured repositories' metadata (no artifact is downloaded)")
	existsCmd.Flags().BoolVar(&existsJSON, "json", false, "Print the answer as JSON")
	existsCmd.PreRun = func(cmd *cobra.Command, args []string) {
		if existsRepo != "" {
			existsRemote = true
		}
	}
	rootCmd.AddCommand(existsCmd)
}
