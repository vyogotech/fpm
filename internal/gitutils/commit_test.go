package gitutils

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
		return string(out)
	}
	run("init", "-q", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "f.txt"), []byte("1"), 0o644))
	run("add", "f.txt")
	run("commit", "-q", "-m", "one")
	return dir
}

func TestResolveHeadCommit(t *testing.T) {
	t.Run("not a repo", func(t *testing.T) {
		_, err := ResolveHeadCommit(t.TempDir())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "git repository not found")
	})

	t.Run("branch head", func(t *testing.T) {
		dir := gitRepo(t)
		c, err := ResolveHeadCommit(dir)
		require.NoError(t, err)
		assert.Regexp(t, `^[0-9a-f]{40}$`, c.SHA)
		assert.Equal(t, "main", c.Ref)
		assert.False(t, c.Dirty)

		// The fallback parser must agree with git.
		fb, err := resolveFromDotGit(dir)
		require.NoError(t, err)
		assert.Equal(t, c.SHA, fb.SHA)
		assert.Equal(t, "main", fb.Ref)
	})

	t.Run("dirty tree", func(t *testing.T) {
		dir := gitRepo(t)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "f.txt"), []byte("2"), 0o644))
		c, err := ResolveHeadCommit(dir)
		require.NoError(t, err)
		assert.True(t, c.Dirty)
	})

	t.Run("detached head and tag", func(t *testing.T) {
		dir := gitRepo(t)
		run := func(args ...string) {
			out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
			require.NoError(t, err, string(out))
		}
		run("tag", "v1.0.0")
		run("checkout", "-q", "--detach", "HEAD")
		c, err := ResolveHeadCommit(dir)
		require.NoError(t, err)
		assert.Equal(t, "v1.0.0", c.Ref, "an exact tag names a detached HEAD")

		fb, err := resolveFromDotGit(dir)
		require.NoError(t, err)
		assert.Equal(t, c.SHA, fb.SHA)
		assert.Equal(t, "", fb.Ref)
	})

	t.Run("packed refs fallback", func(t *testing.T) {
		dir := gitRepo(t)
		out, err := exec.Command("git", "-C", dir, "pack-refs", "--all").CombinedOutput()
		require.NoError(t, err, string(out))
		c, err := ResolveHeadCommit(dir)
		require.NoError(t, err)
		fb, err := resolveFromDotGit(dir)
		require.NoError(t, err)
		assert.Equal(t, c.SHA, fb.SHA)
	})
}
