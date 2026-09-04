package mirror

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fpm/internal/config"
	"fpm/internal/repository"
)

// fakePodman puts a `podman` on PATH that records every invocation, so what the check
// asks the container to do can be asserted without a container.
func fakePodman(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "podman.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + logPath + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "podman"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// TestInstallCheckConfiguresRepositories: a bare container has no repositories, and
// `fpm install` only fetches an app's required_apps when one is configured. Without this
// the check could verify only apps whose dependencies were already baked into the image
// — which is why lms, requiring payments, failed its check and was never published.
func TestInstallCheckConfiguresRepositories(t *testing.T) {
	logPath := fakePodman(t)
	c := InstallCheck{
		Image:  "example/bench:latest",
		FPMBin: filepath.Join(t.TempDir(), "fpm"),
		Log:    func(string, ...any) {},
		Repos: []config.RepositoryConfig{
			{Name: "ghcr", URL: "ghcr.io/acme/fpm", Type: "oci", Username: "ci"},
			{Name: "fpm-http", URL: "https://fpm.example.com", Type: "http"},
		},
	}
	if err := c.configureRepos("container"); err != nil {
		t.Fatalf("configureRepos: %v", err)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal("podman was never invoked")
	}
	for _, want := range []string{
		"fpm repo add 'ghcr' 'ghcr.io/acme/fpm' --type 'oci' --username 'ci'",
		"fpm repo add 'fpm-http' 'https://fpm.example.com' --type 'http'",
	} {
		if !strings.Contains(string(logged), want) {
			t.Fatalf("missing %q in:\n%s", want, logged)
		}
	}
}

// TestInstallCheckPassesRepositoryPasswords: a private registry is no use to the check
// if the credential stays on the host.
func TestInstallCheckPassesRepositoryPasswords(t *testing.T) {
	t.Setenv(repository.PasswordEnvVar("ghcr"), "a-token")
	c := InstallCheck{Repos: []config.RepositoryConfig{{Name: "ghcr", URL: "ghcr.io/acme/fpm"}}}

	env := strings.Join(c.repoEnv(), " ")
	if !strings.Contains(env, repository.PasswordEnvVar("ghcr")+"=a-token") {
		t.Fatalf("the repository's password must reach the container, got %q", env)
	}
}

// TestInstallCheckOmitsPasswordsItDoesNotHave: reads are frequently anonymous, and
// refusing to check a package over a credential that may not be needed would withhold
// one that installs perfectly well.
func TestInstallCheckOmitsPasswordsItDoesNotHave(t *testing.T) {
	t.Setenv(repository.PasswordEnvVar("ghcr"), "")
	t.Setenv(repository.PasswordEnvFallback, "")
	c := InstallCheck{Repos: []config.RepositoryConfig{{Name: "ghcr", URL: "ghcr.io/acme/fpm"}}}
	if got := c.repoEnv(); len(got) != 0 {
		t.Fatalf("no password configured, so nothing should be passed; got %v", got)
	}
}

// TestInstallCheckQuotesConfiguredValues keeps a repository name or URL from being
// read as shell syntax by the bash -c the container runs.
func TestInstallCheckQuotesConfiguredValues(t *testing.T) {
	if got := shellQuote("it's"); got != `'it'"'"'s'` {
		t.Fatalf("shellQuote = %s", got)
	}
	logPath := fakePodman(t)
	c := InstallCheck{Log: func(string, ...any) {},
		Repos: []config.RepositoryConfig{{Name: "odd", URL: "https://x/;touch /tmp/pwned"}}}
	if err := c.configureRepos("container"); err != nil {
		t.Fatalf("configureRepos: %v", err)
	}
	logged, _ := os.ReadFile(logPath)
	if !strings.Contains(string(logged), "'https://x/;touch /tmp/pwned'") {
		t.Fatalf("the URL must be quoted whole, got:\n%s", logged)
	}
}
