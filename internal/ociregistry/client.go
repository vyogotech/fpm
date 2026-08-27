package ociregistry

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"fpm/internal/config"
	"fpm/internal/repository"

	"golang.org/x/term"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
)

// Generic registry auth environment variable names (no provider-specific names).
const (
	EnvRegistryPassword    = "REGISTRY_PASSWORD"
	EnvFPMRegistryPassword = "FPM_REGISTRY_PASSWORD"
	EnvFPMRegistryToken    = "FPM_REGISTRY_TOKEN"
	EnvRegistryUsername    = "REGISTRY_USERNAME"
	EnvFPMRegistryUsername = "FPM_REGISTRY_USERNAME"
)

// ResolveRepoPath constructs the full OCI repository path for an org/app.
// For example:
//
//	repoURL: "ghcr.io/vyogotech/fpm" -> "ghcr.io/vyogotech/fpm/myorg/myapp"
//	repoURL: "localhost:5000"        -> "localhost:5000/fpm/myorg/myapp" (defaults to /fpm/ prefix if root)
func ResolveRepoPath(baseURL, org, appName string) (string, error) {
	raw := strings.TrimSpace(baseURL)
	// Strip scheme if provided
	if strings.HasPrefix(raw, "http://") {
		raw = strings.TrimPrefix(raw, "http://")
	} else if strings.HasPrefix(raw, "https://") {
		raw = strings.TrimPrefix(raw, "https://")
	}
	raw = strings.TrimSuffix(raw, "/")

	parts := strings.Split(raw, "/")
	regHost := parts[0]
	subPath := ""
	if len(parts) > 1 {
		subPath = strings.Join(parts[1:], "/")
	}

	if subPath == "" {
		subPath = "fpm"
	}

	fullRepo := fmt.Sprintf("%s/%s/%s/%s", regHost, subPath, org, appName)
	return fullRepo, nil
}

// NewRepository creates a configured oras remote.Repository instance for a given repo and package.
func NewRepository(repoConfig config.RepositoryConfig, org, appName string) (*remote.Repository, error) {
	fullRef, err := ResolveRepoPath(repoConfig.URL, org, appName)
	if err != nil {
		return nil, fmt.Errorf("invalid repository URL %q: %w", repoConfig.URL, err)
	}

	repo, err := remote.NewRepository(fullRef)
	if err != nil {
		return nil, fmt.Errorf("failed to create OCI repository reference for %q: %w", fullRef, err)
	}

	// Plain HTTP and Insecure settings
	repo.PlainHTTP = repoConfig.PlainHTTP || strings.HasPrefix(repoConfig.URL, "http://") || isLocalhost(repoConfig.URL)

	// Configure HTTP client
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if repoConfig.Insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
	}

	customHTTPClient := &http.Client{
		Transport: tr,
		Timeout:   120 * time.Second,
	}

	// Resolve credentials
	authClient, err := buildAuthClient(customHTTPClient, repoConfig)
	if err != nil {
		return nil, err
	}
	repo.Client = authClient

	return repo, nil
}

func isLocalhost(rawURL string) bool {
	u := strings.TrimPrefix(strings.TrimPrefix(rawURL, "http://"), "https://")
	host := strings.Split(u, "/")[0]
	return strings.HasPrefix(host, "localhost") || strings.HasPrefix(host, "127.0.0.1") || strings.HasPrefix(host, "::1")
}

// buildAuthClient resolves credentials in generic order:
// 1. Explicit username + environment password (REGISTRY_PASSWORD, FPM_REGISTRY_PASSWORD, FPM_REGISTRY_TOKEN, FPM_REPO_<NAME>_PASSWORD, FPM_REPO_PASSWORD)
// 2. Interactive password prompt if terminal
// 3. Docker/Podman credential store fallback
func buildAuthClient(httpClient *http.Client, repoConfig config.RepositoryConfig) (*auth.Client, error) {
	username := resolveUsername(repoConfig)
	password := resolvePassword(repoConfig.Name)

	authClient := &auth.Client{
		Client: httpClient,
		Cache:  auth.NewCache(),
	}

	// If explicit username & password provided
	if username != "" && password != "" {
		authClient.Credential = auth.StaticCredential(parseRegistryHost(repoConfig.URL), auth.Credential{
			Username: username,
			Password: password,
		})
		return authClient, nil
	}

	// If username provided but no password found in environment -> prompt if interactive
	if username != "" && password == "" {
		if term.IsTerminal(int(os.Stdin.Fd())) {
			fmt.Fprintf(os.Stderr, "Password for %s@%s: ", username, repoConfig.Name)
			pwBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			if err != nil {
				return nil, fmt.Errorf("failed to read password for repository %q: %w", repoConfig.Name, err)
			}
			password = string(pwBytes)
			authClient.Credential = auth.StaticCredential(parseRegistryHost(repoConfig.URL), auth.Credential{
				Username: username,
				Password: password,
			})
			return authClient, nil
		}
		return nil, fmt.Errorf("repository %q has username %q but no password was found. Set %s (or %s)",
			repoConfig.Name, username, EnvRegistryPassword, repository.PasswordEnvVar(repoConfig.Name))
	}

	// Fallback to Docker / Podman credential store
	if store, err := credentials.NewStoreFromDocker(credentials.StoreOptions{}); err == nil {
		authClient.Credential = credentials.Credential(store)
	}

	return authClient, nil
}

func resolveUsername(repoConfig config.RepositoryConfig) string {
	if repoConfig.Username != "" {
		return repoConfig.Username
	}
	if u := os.Getenv(EnvRegistryUsername); u != "" {
		return u
	}
	if u := os.Getenv(EnvFPMRegistryUsername); u != "" {
		return u
	}
	return ""
}

func resolvePassword(repoName string) string {
	// Generic registry password/token
	if p := os.Getenv(EnvRegistryPassword); p != "" {
		return p
	}
	if p := os.Getenv(EnvFPMRegistryPassword); p != "" {
		return p
	}
	if p := os.Getenv(EnvFPMRegistryToken); p != "" {
		return p
	}
	// Repo-specific fallback
	if repoName != "" {
		if p := os.Getenv(repository.PasswordEnvVar(repoName)); p != "" {
			return p
		}
	}
	if p := os.Getenv(repository.PasswordEnvFallback); p != "" {
		return p
	}
	return ""
}

func parseRegistryHost(rawURL string) string {
	raw := strings.TrimSpace(rawURL)
	if strings.Contains(raw, "://") {
		if u, err := url.Parse(raw); err == nil {
			return u.Host
		}
	}
	parts := strings.Split(raw, "/")
	return parts[0]
}
