package gitutils

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Commit identifies the exact revision a source tree was packaged from.
type Commit struct {
	// SHA is the full 40-character commit hash of HEAD.
	SHA string
	// Ref is the symbolic name HEAD points at (a branch or tag), or "" when detached.
	Ref string
	// Dirty reports uncommitted changes in the working tree. A package built from a
	// dirty tree cannot be reproduced from SHA alone, so consumers keying a cache on
	// the SHA need to know.
	Dirty bool
}

var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ResolveHeadCommit resolves the commit HEAD points at in repoPath. It prefers the
// git CLI, which handles worktrees, submodules and packed refs correctly, and falls
// back to reading .git/HEAD and refs directly when git is not installed.
//
// A tree that is not a git repository is reported with os.ErrNotExist wrapped, so a
// caller can downgrade it to informational output the same way the remote-URL
// helpers are handled.
func ResolveHeadCommit(repoPath string) (Commit, error) {
	if _, err := os.Stat(filepath.Join(repoPath, ".git")); err != nil {
		if os.IsNotExist(err) {
			return Commit{}, fmt.Errorf("git repository not found at %s: %w", repoPath, err)
		}
		return Commit{}, fmt.Errorf("failed to check %s: %w", filepath.Join(repoPath, ".git"), err)
	}

	if gitPath, err := exec.LookPath("git"); err == nil {
		return resolveWithGit(gitPath, repoPath)
	}
	return resolveFromDotGit(repoPath)
}

func resolveWithGit(gitPath, repoPath string) (Commit, error) {
	run := func(args ...string) (string, error) {
		cmd := exec.Command(gitPath, append([]string{"-C", repoPath}, args...)...)
		out, err := cmd.Output()
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(exitErr.Stderr)))
			}
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return strings.TrimSpace(string(out)), nil
	}

	sha, err := run("rev-parse", "HEAD")
	if err != nil {
		return Commit{}, fmt.Errorf("failed to resolve HEAD in %s: %w", repoPath, err)
	}
	if !fullSHA.MatchString(sha) {
		return Commit{}, fmt.Errorf("git rev-parse HEAD in %s returned %q, not a commit hash", repoPath, sha)
	}

	c := Commit{SHA: sha}
	// symbolic-ref fails on a detached HEAD; that is not an error, just no ref.
	if ref, err := run("symbolic-ref", "--short", "-q", "HEAD"); err == nil && ref != "" {
		c.Ref = ref
	} else if tag, err := run("describe", "--tags", "--exact-match", "HEAD"); err == nil && tag != "" {
		c.Ref = tag
	}

	// Untracked files do not change what a commit reproduces; modified tracked files do.
	if status, err := run("status", "--porcelain", "--untracked-files=no"); err == nil && status != "" {
		c.Dirty = true
	}
	return c, nil
}

// resolveFromDotGit reads HEAD without git. It understands a symbolic HEAD pointing
// at a loose or packed ref, and a detached HEAD holding a raw hash.
func resolveFromDotGit(repoPath string) (Commit, error) {
	gitDir := filepath.Join(repoPath, ".git")
	// A worktree or submodule has a .git *file* naming the real git dir.
	if info, err := os.Stat(gitDir); err == nil && !info.IsDir() {
		data, err := os.ReadFile(gitDir)
		if err != nil {
			return Commit{}, fmt.Errorf("failed to read %s: %w", gitDir, err)
		}
		line := strings.TrimSpace(string(data))
		if !strings.HasPrefix(line, "gitdir:") {
			return Commit{}, fmt.Errorf("unrecognised .git file at %s", gitDir)
		}
		gitDir = strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(repoPath, gitDir)
		}
	}

	headData, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return Commit{}, fmt.Errorf("failed to read HEAD in %s: %w", gitDir, err)
	}
	head := strings.TrimSpace(string(headData))

	if fullSHA.MatchString(head) {
		return Commit{SHA: head}, nil
	}
	if !strings.HasPrefix(head, "ref: ") {
		return Commit{}, fmt.Errorf("unrecognised HEAD content %q in %s", head, gitDir)
	}
	ref := strings.TrimSpace(strings.TrimPrefix(head, "ref: "))
	shortRef := strings.TrimPrefix(strings.TrimPrefix(ref, "refs/heads/"), "refs/tags/")

	// Loose ref first. In a worktree, refs live in the common dir.
	for _, dir := range []string{gitDir, commonDir(gitDir)} {
		if data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(ref))); err == nil {
			sha := strings.TrimSpace(string(data))
			if fullSHA.MatchString(sha) {
				return Commit{SHA: sha, Ref: shortRef}, nil
			}
		}
		if sha := lookupPackedRef(filepath.Join(dir, "packed-refs"), ref); sha != "" {
			return Commit{SHA: sha, Ref: shortRef}, nil
		}
	}
	return Commit{}, fmt.Errorf("could not resolve %s to a commit in %s", ref, gitDir)
}

// commonDir returns the shared git dir for a worktree, or gitDir itself.
func commonDir(gitDir string) string {
	data, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return gitDir
	}
	common := strings.TrimSpace(string(data))
	if !filepath.IsAbs(common) {
		common = filepath.Join(gitDir, common)
	}
	return common
}

func lookupPackedRef(packedRefsPath, ref string) string {
	f, err := os.Open(packedRefsPath)
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || line[0] == '#' || line[0] == '^' {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == ref && fullSHA.MatchString(fields[0]) {
			return fields[0]
		}
	}
	return ""
}
