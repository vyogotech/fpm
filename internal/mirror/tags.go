package mirror

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"fpm/internal/semver"
)

// Tag is one remote release tag.
type Tag struct {
	Name string // as published, e.g. "v15.93.1"
	SHA  string
}

// ListRemoteTags lists a repository's tags without cloning it.
//
// --refs drops the peeled "^{}" entries ls-remote emits for annotated tags,
// so every line is exactly "<sha>\trefs/tags/<name>".
func ListRemoteTags(repoURL string) ([]Tag, error) {
	out, err := exec.Command("git", "ls-remote", "--tags", "--refs", repoURL).Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-remote --tags %s: %w (%s)", repoURL, err, exitDetail(err))
	}

	var tags []Tag
	for _, line := range strings.Split(string(out), "\n") {
		sha, ref, found := strings.Cut(strings.TrimSpace(line), "\t")
		if !found {
			continue
		}
		name := strings.TrimPrefix(ref, "refs/tags/")
		if name == ref || name == "" {
			continue
		}
		tags = append(tags, Tag{Name: name, SHA: sha})
	}
	return tags, nil
}

// ResolveRemoteBranch returns the commit a remote branch points at.
func ResolveRemoteBranch(repoURL, branch string) (string, error) {
	out, err := exec.Command("git", "ls-remote", repoURL, "refs/heads/"+branch).Output()
	if err != nil {
		return "", fmt.Errorf("git ls-remote %s %s: %w (%s)", repoURL, branch, err, exitDetail(err))
	}
	sha, _, found := strings.Cut(strings.TrimSpace(string(out)), "\t")
	if !found || sha == "" {
		return "", fmt.Errorf("branch %q not found on %s", branch, repoURL)
	}
	return sha, nil
}

// LatestPerMajor picks the newest stable release of each major line.
//
// Tags that do not parse as versions (frappe repos carry the odd oddball) and
// prereleases are ignored; when majors is non-empty, only those lines are
// considered. The result is ordered by major so plans are deterministic.
func LatestPerMajor(tags []Tag, majors []int) []Tag {
	allowed := map[int]bool{}
	for _, major := range majors {
		allowed[major] = true
	}

	best := map[int]Tag{}
	for _, tag := range tags {
		major, ok := semver.Major(tag.Name)
		if !ok || semver.IsPrerelease(tag.Name) {
			continue
		}
		if len(allowed) > 0 && !allowed[major] {
			continue
		}
		current, exists := best[major]
		if !exists || semver.Compare(tag.Name, current.Name) > 0 {
			best[major] = tag
		}
	}

	keys := make([]int, 0, len(best))
	for major := range best {
		keys = append(keys, major)
	}
	sort.Ints(keys)

	out := make([]Tag, 0, len(best))
	for _, major := range keys {
		out = append(out, best[major])
	}
	return out
}

// NormalizeVersion maps a tag name to the version string used everywhere else:
// passed to `fpm package --version` and compared against registry metadata.
// One normalization point, so the two can never disagree.
func NormalizeVersion(tagName string) string {
	return strings.TrimPrefix(strings.TrimSpace(tagName), "v")
}

// BranchPseudoVersion is the version a branch-tracked build publishes as.
// It carries a prerelease identifier by construction, so semver.Latest never
// surfaces it as a package's latest version.
func BranchPseudoVersion(major int, date, sha string) string {
	return fmt.Sprintf("%d.0.0-git.%s.%s", major, date, ShortSHA(sha))
}

// ShortSHA is the fixed-width commit abbreviation used in pseudo-versions.
// Fixed here rather than delegated to git, so skip-if-published comparisons
// do not depend on a local git's abbreviation heuristics.
func ShortSHA(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}

func exitDetail(err error) string {
	if exit, ok := err.(*exec.ExitError); ok && len(exit.Stderr) > 0 {
		return strings.TrimSpace(string(exit.Stderr))
	}
	return "no stderr"
}
