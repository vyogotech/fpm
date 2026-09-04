package mirror

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"fpm/internal/config"
	"fpm/internal/repository"
)

// Publishing proves an artifact exists. It does not prove it works: the catalogue
// shipped front-end packages for months that installed and then rendered nothing,
// because nothing ever installed one (issue #9), and a site install could leave an app
// registered with none of its DocTypes (issue #13). Neither is visible in the artifact.
//
// So a package is installed before it is published — into a throwaway Single Node
// Frappista container, the same way a user would — and a failure keeps it out of the
// registry rather than putting the discovery off until someone complains.

// installCheckTimeout bounds one package's verification. A bench boot plus an install
// is a couple of minutes; well past that means something is wedged.
const installCheckTimeout = 12 * time.Minute

// InstallCheck describes how to verify a built package installs.
type InstallCheck struct {
	// Image is the bench container to install into. Empty disables the check.
	Image string
	// Site is the site in that image to install onto.
	Site string
	// FPMBin is the binary to drive the install with, mounted into the container.
	FPMBin string
	// Repos are the repositories to configure inside the container, so that an app's
	// required_apps can be fetched the way they would be on a real bench.
	//
	// Without them the check can only verify an app whose dependencies are already
	// baked into the image. `fpm install` resolves transitively and will fetch what is
	// missing, but only when a repository is configured — a bare container has none, so
	// an app like lms, which requires payments, failed its check and was never
	// published. Dependencies build before their dependents, so by the time an app is
	// checked its requirements are in the registries this run publishes to.
	Repos []config.RepositoryConfig
	// Log receives progress.
	Log func(format string, args ...any)
}

// Enabled reports whether a check is configured.
func (c InstallCheck) Enabled() bool { return c.Image != "" }

// Verify installs artifact into a fresh container and reports what a user would notice:
// the install failing, or succeeding while leaving the app without its DocTypes.
//
// It returns nil when there is nothing it can check — no podman on the host — rather
// than failing a build for a missing tool, since the alternative is a mirror that
// cannot run anywhere the check is unavailable.
func (c InstallCheck) Verify(artifact, appName, version string) error {
	if !c.Enabled() {
		return nil
	}
	if _, err := exec.LookPath("podman"); err != nil {
		c.log("  install check skipped: podman is not on PATH")
		return nil
	}
	if c.Site == "" {
		c.Site = "dev.localhost"
	}

	name := fmt.Sprintf("fpm-installcheck-%s-%d", appName, time.Now().UnixNano())
	bench := "/home/frappe/frappe-bench"
	artifactDir, artifactFile := filepath.Split(artifact)

	c.log("  install check: %s==%s into %s", appName, version, c.Image)
	run := exec.Command("podman", "run", "-d", "--name", name,
		"--userns=keep-id:uid=1001,gid=0",
		"-v", artifactDir+":/artifact:ro,Z",
		"-v", filepath.Dir(c.FPMBin)+":/opt/fpm:ro,Z",
		c.Image, "bash", "-c",
		"sed -i '/^watch:/d' "+bench+"/Procfile; exec /usr/libexec/s2i/run")
	if out, err := run.CombinedOutput(); err != nil {
		return fmt.Errorf("could not start %s: %v\n%s", c.Image, err, tail(string(out), 400))
	}
	defer func() {
		_ = exec.Command("podman", "rm", "-f", name).Run()
	}()

	if err := c.configureRepos(name); err != nil {
		return err
	}

	if err := c.waitForBench(name); err != nil {
		// The bench not starting says nothing about the package. Refusing to publish
		// over it would block the catalogue on a flaky runner, so this is reported and
		// the package goes on — the check is a gate on the artifact, not on the host.
		c.log("  install check skipped: %v", err)
		return nil
	}

	// The install a user runs, onto a real site. fpm's own site install verifies the
	// app's DocTypes reached the database and repairs the state issue #13 left, so a
	// clean exit here already means more than "the files were copied".
	install := fmt.Sprintf("fpm install /artifact/%s --bench-path %s --site %s", artifactFile, bench, c.Site)
	out, err := c.exec(name, install, installCheckTimeout)
	if err != nil {
		return fmt.Errorf("the package does not install: %v\n%s", err, tail(out, 1500))
	}

	// It has to be on the site afterwards, not merely unpacked into the bench.
	listed, err := c.exec(name,
		fmt.Sprintf("cd sites && ../env/bin/python -m frappe.utils.bench_helper frappe --site %s list-apps", c.Site),
		2*time.Minute)
	if err != nil {
		return fmt.Errorf("could not list the site's apps after installing: %v\n%s", err, tail(listed, 600))
	}
	if !strings.Contains(listed, appName) {
		return fmt.Errorf("%s installed without error but is not on %s:\n%s", appName, c.Site, tail(listed, 400))
	}

	c.log("  install check: %s==%s installs and is live on %s", appName, version, c.Site)
	return nil
}

// configureRepos points the container's fpm at the same repositories the run publishes
// to, so an install can fetch an app's required_apps instead of refusing.
//
// A repository whose password is not in this process's environment is still added: reads
// are frequently anonymous, and failing the check over a credential that may not be
// needed would withhold a package that installs perfectly well.
func (c InstallCheck) configureRepos(container string) error {
	for _, r := range c.Repos {
		add := fmt.Sprintf("fpm repo add %s %s", shellQuote(r.Name), shellQuote(r.URL))
		if r.Type != "" {
			add += " --type " + shellQuote(r.Type)
		}
		if r.Username != "" {
			add += " --username " + shellQuote(r.Username)
		}
		if r.PlainHTTP {
			add += " --plain-http"
		}
		if r.Insecure {
			add += " --insecure"
		}
		if out, err := c.exec(container, add, 2*time.Minute); err != nil {
			return fmt.Errorf("could not configure repository %s in the check container: %v\n%s",
				r.Name, err, tail(out, 400))
		}
	}
	if len(c.Repos) > 0 {
		c.log("  install check: configured %d repository(ies) so required_apps can be fetched", len(c.Repos))
	}
	return nil
}

// repoEnv passes each configured repository's password into the container, under the
// name that repository reads it from.
func (c InstallCheck) repoEnv() []string {
	var env []string
	for _, r := range c.Repos {
		name := repository.PasswordEnvVar(r.Name)
		if v := os.Getenv(name); v != "" {
			env = append(env, "-e", name+"="+v)
		}
	}
	if v := os.Getenv(repository.PasswordEnvFallback); v != "" {
		env = append(env, "-e", repository.PasswordEnvFallback+"="+v)
	}
	return env
}

// shellQuote makes a configured value safe to interpolate into the bash -c script the
// container runs. Repository names and URLs come from configuration, not from a package.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func (c InstallCheck) waitForBench(container string) error {
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := c.exec(container,
			"(mariadb-admin ping || mysqladmin ping) >/dev/null 2>&1 && redis-cli ping >/dev/null 2>&1",
			30*time.Second); err == nil {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	logs, _ := exec.Command("podman", "logs", "--tail", "30", container).CombinedOutput()
	return fmt.Errorf("the bench in %s never came up:\n%s", c.Image, tail(string(logs), 800))
}

func (c InstallCheck) exec(container, script string, timeout time.Duration) (string, error) {
	args := append([]string{"exec", "-i", "-e", "HOME=/home/frappe"}, c.repoEnv()...)
	args = append(args, container, "bash", "-c",
		"export PATH=/opt/fpm:/home/frappe/frappe-bench/env/bin:/home/frappe/.local/bin:$PATH; cd /home/frappe/frappe-bench; "+script)
	cmd := exec.Command("podman", args...)
	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = cmd.CombinedOutput()
		close(done)
	}()
	select {
	case <-done:
		return string(out), err
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		return string(out), fmt.Errorf("timed out after %s", timeout)
	}
}

func (c InstallCheck) log(format string, args ...any) {
	if c.Log != nil {
		c.Log(format, args...)
	}
}
