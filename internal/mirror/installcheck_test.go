package mirror

import (
	"fmt"
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

// TestInstallCheckSkipsWhenTheContainerWillNotStart: rootless podman on a shared runner
// intermittently fails to map the user namespace (crun, exit 126). That says nothing
// about the package, and treating it as a failure withheld four of twelve apps in one
// run — the same reasoning the bench-never-came-up path already applies.
func TestInstallCheckSkipsWhenTheContainerWillNotStart(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"case \"$1\" in run) echo 'crun: writing file `/proc/9/gid_map`: Invalid argument' >&2; exit 126 ;; esac\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "podman"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	artifact := filepath.Join(t.TempDir(), "wiki-3.1.0.fpm")
	if err := os.WriteFile(artifact, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var logged []string
	c := InstallCheck{
		Image:  "example/bench:latest",
		FPMBin: filepath.Join(t.TempDir(), "fpm"),
		Log:    func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) },
	}
	if err := c.Verify(artifact, "wiki", "3.1.0"); err != nil {
		t.Fatalf("a host that cannot start containers must not fail the package: %v", err)
	}
	joined := strings.Join(logged, "\n")
	if !strings.Contains(joined, "install check skipped") {
		t.Fatalf("the skip has to be reported, got: %s", joined)
	}
	if !strings.Contains(joined, "gid_map") {
		t.Fatalf("the reason has to survive into the log, got: %s", joined)
	}
}

// TestInstallCheckFallsBackWhenKeepIdIsRefused: keep-id needs subuid/subgid ranges the
// host may not have, and on a GitHub runner it fails outright — eight of twelve checks
// in one run were skipped for it. Nothing is written through either mount and the image
// runs as frappe either way, so the check retries in the default namespace rather than
// giving up on a gate that exists to stop broken packages reaching the registry.
func TestInstallCheckFallsBackWhenKeepIdIsRefused(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "podman.log")
	// keep-id is refused the way crun refuses it; the same run without it succeeds.
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + logPath + "\n" +
		"case \"$*\" in\n" +
		"  *keep-id*) echo 'crun: writing file `/proc/9/gid_map`: Invalid argument' >&2; exit 126 ;;\n" +
		"  run*) echo container-id; exit 0 ;;\n" +
		"  exec*) echo wiki; exit 0 ;;\n" + // the bench pings, and list-apps reports the app
		"esac\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "podman"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	artifact := filepath.Join(t.TempDir(), "wiki-3.1.0.fpm")
	if err := os.WriteFile(artifact, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var logged []string
	c := InstallCheck{
		Image: "example/bench:latest", Site: "dev.localhost",
		FPMBin: filepath.Join(t.TempDir(), "fpm"),
		Log:    func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) },
	}
	// The stub answers every later exec successfully, so a clean return means the
	// container was started by the fallback and the check ran against it.
	if err := c.Verify(artifact, "wiki", "3.1.0"); err != nil {
		t.Fatalf("the check must fall back rather than fail: %v", err)
	}
	invocations, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	runs := 0
	for _, line := range strings.Split(string(invocations), "\n") {
		if strings.HasPrefix(line, "run ") {
			runs++
		}
	}
	if runs < 2 {
		t.Fatalf("keep-id was refused, so a second start without it is required; saw %d run(s):\n%s", runs, invocations)
	}
	if strings.Contains(strings.Join(logged, "\n"), "install check skipped") {
		t.Fatalf("the fallback succeeded, so nothing should have been skipped: %v", logged)
	}
}
