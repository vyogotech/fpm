// Credential handling for repositories that require HTTP Basic Auth.
//
// The repository server FPM ships requires authentication for every write, so publishing
// to a secured repository is impossible without credentials on the request.
//
// A username is recorded in the repository configuration, but a password never is:
// ~/.fpm/config.json is a plain file that ends up in backups and dotfile repositories.
// Passwords come from the environment, or from an interactive prompt, and are held only
// for the lifetime of the command.
package repository

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

// PasswordEnvPrefix names the per-repository password variable, suffixed with the
// repository name uppercased and non-alphanumerics replaced by underscores. A repository
// named "company-repo" reads FPM_REPO_COMPANY_REPO_PASSWORD.
const PasswordEnvPrefix = "FPM_REPO_"

// PasswordEnvFallback applies to every repository, for single-repository setups and CI
// jobs that would otherwise have to know the repository's configured name.
const PasswordEnvFallback = "FPM_REPO_PASSWORD"

// Credentials are the HTTP Basic Auth credentials for one repository.
type Credentials struct {
	Username string
	Password string
}

// Configured reports whether there is anything to send.
func (c Credentials) Configured() bool { return c.Username != "" }

// PasswordEnvVar returns the environment variable consulted for a repository's password.
func PasswordEnvVar(repoName string) string {
	var b strings.Builder
	b.WriteString(PasswordEnvPrefix)
	for _, r := range strings.ToUpper(repoName) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	b.WriteString("_PASSWORD")
	return b.String()
}

// ResolveCredentials determines the credentials to use for a repository.
//
// The username comes from configuration. The password is read from the repository's own
// environment variable, then the generic fallback, and only then by prompting - so
// non-interactive use (CI, cron) works without a terminal, and an interactive user is
// never forced to put a password in their shell history.
//
// promptAllowed lets callers suppress the prompt for operations that should never block.
func ResolveCredentials(repoName, username string, promptAllowed bool) (Credentials, error) {
	if username == "" {
		return Credentials{}, nil
	}

	if pw := os.Getenv(PasswordEnvVar(repoName)); pw != "" {
		return Credentials{Username: username, Password: pw}, nil
	}
	if pw := os.Getenv(PasswordEnvFallback); pw != "" {
		return Credentials{Username: username, Password: pw}, nil
	}

	if !promptAllowed || !term.IsTerminal(int(os.Stdin.Fd())) {
		return Credentials{}, fmt.Errorf(
			"repository %q is configured with username %q but no password was found. "+
				"Set %s (or %s), or run the command interactively to be prompted",
			repoName, username, PasswordEnvVar(repoName), PasswordEnvFallback)
	}

	fmt.Fprintf(os.Stderr, "Password for %s@%s: ", username, repoName)
	pwBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return Credentials{}, fmt.Errorf("failed to read password for repository %q: %w", repoName, err)
	}
	return Credentials{Username: username, Password: string(pwBytes)}, nil
}

// authTransport attaches Basic Auth to requests bound for the repository host.
//
// Scoping by host matters: a repository may redirect to a CDN or object store on another
// origin, and credentials must not follow the request off-site.
type authTransport struct {
	base  http.RoundTripper
	host  string
	creds Credentials
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL != nil && strings.EqualFold(req.URL.Host, t.host) {
		// Clone before mutating: RoundTrip must not modify the caller's request.
		req = req.Clone(req.Context())
		req.SetBasicAuth(t.creds.Username, t.creds.Password)
	}
	return t.base.RoundTrip(req)
}

// NewClient returns an HTTP client for a repository, attaching credentials when they are
// configured. With none configured it behaves exactly as an unauthenticated client, so
// public repositories are unaffected.
func NewClient(repoURL string, creds Credentials, timeout time.Duration) (*http.Client, error) {
	client := &http.Client{Timeout: timeout}
	if !creds.Configured() {
		return client, nil
	}

	parsed, err := url.Parse(repoURL)
	if err != nil {
		return nil, fmt.Errorf("invalid repository URL %q: %w", repoURL, err)
	}

	client.Transport = &authTransport{
		base:  http.DefaultTransport,
		host:  parsed.Host,
		creds: creds,
	}
	return client, nil
}

// HTTPStatusError reports a request that completed with an unexpected status. It is a
// distinct type so callers can recognise an authentication failure and explain it,
// rather than surfacing a bare 401 that gives no hint what to do.
type HTTPStatusError struct {
	URL        string
	StatusCode int
	Status     string
	Body       string
}

func (e *HTTPStatusError) Error() string {
	msg := fmt.Sprintf("request to %s failed (status: %s)", e.URL, e.Status)
	if e.Body != "" {
		msg += ". Response: " + e.Body
	}
	return msg
}

// IsAuthFailure reports whether an error was an authentication or authorisation refusal.
func IsAuthFailure(err error) bool {
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) {
		return false
	}
	return statusErr.StatusCode == http.StatusUnauthorized ||
		statusErr.StatusCode == http.StatusForbidden
}

// DescribeTooLarge turns an HTTP 413 into guidance. The status usually comes from a
// proxy in front of the registry rather than from the registry itself — fpm's own
// registry accepts artifacts up to 1 GiB, and its nginx up to 500 MB, while a CDN in
// front commonly caps request bodies at 100 MB — so the size to raise is not on the
// machine the publisher is looking at.
func DescribeTooLarge(repoName string, sizeBytes int64) string {
	size := ""
	if sizeBytes > 0 {
		size = fmt.Sprintf(" (%.1f MB)", float64(sizeBytes)/(1<<20))
	}
	return fmt.Sprintf(
		"The package%s was refused as too large by %q, or by a proxy or CDN in front of it — "+
			"an upload cap of 100 MB is a common default, and it applies before the registry sees the request. "+
			"Raise that limit for the registry's hostname, publish this package through a route that does not pass "+
			"through it, or reduce the artifact (its vendored wheels and compiled frontend are usually most of the size).",
		size, repoName)
}

// IsTooLarge reports whether an upload was refused for its size.
func IsTooLarge(err error) bool {
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) {
		return false
	}
	return statusErr.StatusCode == http.StatusRequestEntityTooLarge
}

// DescribeAuthFailure turns an HTTP 401/403 into guidance, since the underlying status
// alone gives no hint that credentials are the missing piece.
func DescribeAuthFailure(repoName, username string, statusCode int) string {
	if statusCode != http.StatusUnauthorized && statusCode != http.StatusForbidden {
		return ""
	}
	if username == "" {
		return fmt.Sprintf(
			"repository %q requires authentication but no username is configured. "+
				"Run 'fpm repo add %s <url> --username <user>' to set one, then supply the password via %s",
			repoName, repoName, PasswordEnvVar(repoName))
	}
	return fmt.Sprintf(
		"authentication failed for repository %q as user %q. "+
			"Check the password supplied via %s (or %s)",
		repoName, username, PasswordEnvVar(repoName), PasswordEnvFallback)
}
