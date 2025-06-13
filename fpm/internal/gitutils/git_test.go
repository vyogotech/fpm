package gitutils

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetGitRemoteOriginInfo(t *testing.T) {
	createMockGitConfig := func(t *testing.T, dir string, content string) {
		t.Helper()
		gitDir := filepath.Join(dir, ".git")
		require.NoError(t, os.MkdirAll(gitDir, 0755))
		configFilePath := filepath.Join(gitDir, "config")
		require.NoError(t, os.WriteFile(configFilePath, []byte(content), 0644))
	}

	t.Run("valid https url", func(t *testing.T) {
		tmpDir := t.TempDir()
		configContent := `
[core]
	repositoryformatversion = 0
[remote "origin"]
	url = https://github.com/testorg/testrepo.git
	fetch = +refs/heads/*:refs/remotes/origin/*
[branch "main"]
	remote = origin
	merge = refs/heads/main
`
		createMockGitConfig(t, tmpDir, configContent)
		org, repo, err := GetGitRemoteOriginInfo(tmpDir)
		assert.NoError(t, err)
		assert.Equal(t, "testorg", org)
		assert.Equal(t, "testrepo", repo)
	})

	t.Run("valid ssh url", func(t *testing.T) {
		tmpDir := t.TempDir()
		configContent := `[remote "origin"]
			url = git@github.com:myorg/myrepo.git`
		createMockGitConfig(t, tmpDir, configContent)
		org, repo, err := GetGitRemoteOriginInfo(tmpDir)
		assert.NoError(t, err)
		assert.Equal(t, "myorg", org)
		assert.Equal(t, "myrepo", repo)
	})

	t.Run("url without .git suffix", func(t *testing.T) {
		tmpDir := t.TempDir()
		configContent := `[remote "origin"]
			url = https://gitlab.com/another-org/another-repo` // common case
		createMockGitConfig(t, tmpDir, configContent)
		org, repo, err := GetGitRemoteOriginInfo(tmpDir)
		assert.NoError(t, err)
		assert.Equal(t, "another-org", org)
		assert.Equal(t, "another-repo", repo)
	})

	t.Run("url with . in name", func(t *testing.T) {
		tmpDir := t.TempDir()
		configContent := `[remote "origin"]
			url = git@github.com:org.with.dots/repo.with.dots.git`
		createMockGitConfig(t, tmpDir, configContent)
		org, repo, err := GetGitRemoteOriginInfo(tmpDir)
		assert.NoError(t, err)
		assert.Equal(t, "org.with.dots", org)
		assert.Equal(t, "repo.with.dots", repo)
	})


	t.Run("no .git/config file", func(t *testing.T) {
		tmpDir := t.TempDir() // No .git/config created
		_, _, err := GetGitRemoteOriginInfo(tmpDir)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "git config not found")
	})

	t.Run("no [remote \"origin\"] section", func(t *testing.T) {
		tmpDir := t.TempDir()
		configContent := `[core]
			repositoryformatversion = 0`
		createMockGitConfig(t, tmpDir, configContent)
		_, _, err := GetGitRemoteOriginInfo(tmpDir)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "remote 'origin' URL not found")
	})

	t.Run("no url in origin section", func(t *testing.T) {
		tmpDir := t.TempDir()
		configContent := `[remote "origin"]
			fetch = +refs/heads/*:refs/remotes/origin/*`
		createMockGitConfig(t, tmpDir, configContent)
		_, _, err := GetGitRemoteOriginInfo(tmpDir)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "remote 'origin' URL not found or 'url' field missing")
	})

	t.Run("unparseable url (file path)", func(t *testing.T) {
		tmpDir := t.TempDir()
		configContent := `[remote "origin"]
			url = /some/local/path/to/repo.git`
		createMockGitConfig(t, tmpDir, configContent)
		_, _, err := GetGitRemoteOriginInfo(tmpDir)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to parse org and repo from origin URL")
	})

	t.Run("unparseable url (http but not git)", func(t *testing.T) {
		tmpDir := t.TempDir()
		configContent := `[remote "origin"]
			url = http://example.com/just/a/path`
		createMockGitConfig(t, tmpDir, configContent)
		_, _, err := GetGitRemoteOriginInfo(tmpDir)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to parse org and repo from origin URL")
	})

	t.Run("origin section after other remotes", func(t *testing.T) {
		tmpDir := t.TempDir()
		configContent := `
[remote "upstream"]
	url = https://github.com/other/other.git
[remote "origin"]
	url = https://github.com/correct-org/correct-repo.git
`
		createMockGitConfig(t, tmpDir, configContent)
		org, repo, err := GetGitRemoteOriginInfo(tmpDir)
		assert.NoError(t, err)
		assert.Equal(t, "correct-org", org)
		assert.Equal(t, "correct-repo", repo)
	})

	t.Run("origin url is last line in section", func(t *testing.T) {
		tmpDir := t.TempDir()
		configContent := `
[remote "origin"]
	fetch = +refs/heads/*:refs/remotes/origin/*
	url = https://github.com/lastline/test.git`
		createMockGitConfig(t, tmpDir, configContent)
		org, repo, err := GetGitRemoteOriginInfo(tmpDir)
		assert.NoError(t, err)
		assert.Equal(t, "lastline", org)
		assert.Equal(t, "test", repo)
	})

	t.Run("complex url with user info and port", func(t *testing.T) {
		tmpDir := t.TempDir()
		configContent := `[remote "origin"]
			url = https://user:pass@gitserver.example.com:8080/my-org/my-app.git`
		createMockGitConfig(t, tmpDir, configContent)
		org, repo, err := GetGitRemoteOriginInfo(tmpDir)
		assert.NoError(t, err)
		assert.Equal(t, "my-org", org)
		assert.Equal(t, "my-app", repo)
	})
}
