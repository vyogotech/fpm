package gitutils

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// GetGitRemoteOriginInfo parses .git/config to find the remote "origin" URL
// and extracts organization and repository name.
// It returns org, repoName, or an error if parsing fails or info is not found.
func GetGitRemoteOriginInfo(repoPath string) (org string, repoName string, err error) {
	gitConfigPath := filepath.Join(repoPath, ".git", "config")

	file, err := os.Open(gitConfigPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", fmt.Errorf("git config not found at %s: %w", gitConfigPath, err)
		}
		return "", "", fmt.Errorf("failed to open git config %s: %w", gitConfigPath, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	inOriginSection := false
	originURL := ""

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[remote \"origin\"]") { // Note: Fixed quote matching
			inOriginSection = true
			continue
		}
		if inOriginSection {
			// If we enter another section (e.g., [branch "main"]) or a new remote,
			// stop parsing for the current origin's URL.
			if strings.HasPrefix(line, "[") {
				break
			}
			if strings.HasPrefix(line, "url = ") {
				originURL = strings.TrimPrefix(line, "url = ")
				break // Found the URL for origin
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return "", "", fmt.Errorf("error scanning git config %s: %w", gitConfigPath, err)
	}

	if originURL == "" {
		return "", "", fmt.Errorf("remote 'origin' URL not found or 'url' field missing in %s", gitConfigPath)
	}

	// Regex to parse common Git URL formats (SSH and HTTPS)
	// Catches:
	// - git@host:org/repo.git
	// - https://host/org/repo.git
	// - https://user@host/org/repo.git
	// - git://host/org/repo.git (though less common now)
	// Groups:
	// 1: host (e.g., "github.com", "gitserver.example.com:8080")
	// 2: org (e.g., "testorg", "my-org", "org.with.dots")
	// 3: repoName (e.g., "testrepo", "my-app", "repo.with.dots")
	// This regex attempts to handle SSH, GIT, and HTTPS protocols, optional userinfo, optional port numbers,
	// and dots/hyphens in host, org, and repo names.
	re := regexp.MustCompile(`(?i)(?:git@|git://|https://)(?:[^@]+@)?([\w\.-]+(?:[:\d]+)?)[/:]([\w\.-]+)/([\w\.-]+?)(?:\.git)?$`)
	matches := re.FindStringSubmatch(originURL)

	if len(matches) >= 4 { // Should be exactly 4 if it matches fully
		org = strings.TrimSpace(matches[2])
		repoName = strings.TrimSpace(matches[3])

		if org == "" || repoName == "" {
			return "", "", fmt.Errorf("regex matched but org ('%s') or repoName ('%s') is empty for URL: '%s'", org, repoName, originURL)
		}
		return org, repoName, nil
	}

	// Fallback for URLs that might not fit the complex regex but are simple like SCP-style SSH short syntax
	// e.g. git@github.com:org/repo (without .git and no protocol scheme)
	// The above regex should handle `git@github.com:org/repo.git` already.
	// This is more of a specific fallback if the primary one fails for some simpler valid SSH URIs.
	// However, the primary regex `(?:git@|...` part should cover the SCP-like git@host:path syntax.
	// Let's test if the current regex handles `git@github.com:org.with.dots/repo.with.dots` (no .git)
	// The `(?:\.git)?$` makes `.git` optional, and `([\w\.-]+?)` for repo should be fine.

	return "", "", fmt.Errorf("failed to parse org and repo from origin URL: '%s'", originURL)
}

// GetFullGitRemoteOriginURL parses .git/config to find the remote "origin" URL.
// It returns the full URL string or an error if parsing fails or info is not found.
func GetFullGitRemoteOriginURL(repoPath string) (string, error) {
	gitConfigPath := filepath.Join(repoPath, ".git", "config")

	file, err := os.Open(gitConfigPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("git config not found at %s: %w", gitConfigPath, err)
		}
		return "", fmt.Errorf("failed to open git config %s: %w", gitConfigPath, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	inOriginSection := false
	originURL := ""

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[remote \"origin\"]") {
			inOriginSection = true
			continue
		}
		if inOriginSection {
			if strings.HasPrefix(line, "[") { // Entered a new section
				break
			}
			if strings.HasPrefix(line, "url = ") {
				originURL = strings.TrimPrefix(line, "url = ")
				break // Found the URL for origin
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("error scanning git config %s: %w", gitConfigPath, err)
	}

	if originURL == "" {
		return "", fmt.Errorf("remote 'origin' URL not found or 'url' field missing in %s", gitConfigPath)
	}

	return originURL, nil
}
