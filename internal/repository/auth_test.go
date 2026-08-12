package repository

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPasswordEnvVar(t *testing.T) {
	cases := map[string]string{
		"prod":         "FPM_REPO_PROD_PASSWORD",
		"company-repo": "FPM_REPO_COMPANY_REPO_PASSWORD",
		"local.dev":    "FPM_REPO_LOCAL_DEV_PASSWORD",
		"repo1":        "FPM_REPO_REPO1_PASSWORD",
	}
	for repoName, want := range cases {
		if got := PasswordEnvVar(repoName); got != want {
			t.Fatalf("PasswordEnvVar(%q) = %q, want %q", repoName, got, want)
		}
	}
}

// TestResolveCredentialsNoUsername covers public repositories: with no username there is
// nothing to resolve, and no prompt should ever appear.
func TestResolveCredentialsNoUsername(t *testing.T) {
	creds, err := ResolveCredentials("public", "", true)
	if err != nil {
		t.Fatalf("a repository without a username should not error: %v", err)
	}
	if creds.Configured() {
		t.Fatalf("expected no credentials, got %+v", creds)
	}
}

func TestResolveCredentialsFromRepoEnv(t *testing.T) {
	t.Setenv(PasswordEnvVar("prod"), "s3cret")

	creds, err := ResolveCredentials("prod", "deployer", false)
	if err != nil {
		t.Fatalf("ResolveCredentials failed: %v", err)
	}
	if creds.Username != "deployer" || creds.Password != "s3cret" {
		t.Fatalf("unexpected credentials: %+v", creds)
	}
}

// TestResolveCredentialsPrefersRepoSpecificEnv keeps a multi-repository setup
// unambiguous when the generic fallback is also set.
func TestResolveCredentialsPrefersRepoSpecificEnv(t *testing.T) {
	t.Setenv(PasswordEnvFallback, "generic")
	t.Setenv(PasswordEnvVar("prod"), "specific")

	creds, err := ResolveCredentials("prod", "deployer", false)
	if err != nil {
		t.Fatalf("ResolveCredentials failed: %v", err)
	}
	if creds.Password != "specific" {
		t.Fatalf("repository-specific password should win, got %q", creds.Password)
	}
}

func TestResolveCredentialsFallbackEnv(t *testing.T) {
	t.Setenv(PasswordEnvFallback, "generic")

	creds, err := ResolveCredentials("prod", "deployer", false)
	if err != nil {
		t.Fatalf("ResolveCredentials failed: %v", err)
	}
	if creds.Password != "generic" {
		t.Fatalf("expected the fallback password, got %q", creds.Password)
	}
}

// TestResolveCredentialsNonInteractiveWithoutPassword covers CI: a missing password must
// fail with guidance rather than blocking on a prompt that nothing will answer.
func TestResolveCredentialsNonInteractiveWithoutPassword(t *testing.T) {
	_, err := ResolveCredentials("prod", "deployer", false)
	if err == nil {
		t.Fatal("expected an error when no password is available and prompting is disallowed")
	}
	for _, want := range []string{PasswordEnvVar("prod"), PasswordEnvFallback} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error should name %s, got: %v", want, err)
		}
	}
}

// TestClientSendsBasicAuth is the point of the feature: the repository server FPM ships
// requires authentication for every write.
func TestClientSendsBasicAuth(t *testing.T) {
	var gotUser, gotPass string
	var gotOK bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, gotOK = r.BasicAuth()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, Credentials{Username: "deployer", Password: "s3cret"}, 10*time.Second)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	resp, err := client.Get(srv.URL + "/anything")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if !gotOK {
		t.Fatal("no Basic Auth header was sent")
	}
	if gotUser != "deployer" || gotPass != "s3cret" {
		t.Fatalf("wrong credentials sent: %s/%s", gotUser, gotPass)
	}
}

// TestClientWithoutCredentialsSendsNone keeps public repositories unaffected.
func TestClientWithoutCredentialsSendsNone(t *testing.T) {
	var sawAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _, sawAuth = r.BasicAuth()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, Credentials{}, 10*time.Second)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	resp, err := client.Get(srv.URL + "/anything")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if sawAuth {
		t.Fatal("credentials were sent for a repository configured without any")
	}
}

// TestClientDoesNotLeakCredentialsOffHost guards against handing a repository's password
// to a third party when it redirects to a CDN or object store on another origin.
func TestClientDoesNotLeakCredentialsOffHost(t *testing.T) {
	var elsewhereSawAuth bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _, elsewhereSawAuth = r.BasicAuth()
		w.WriteHeader(http.StatusOK)
	}))
	defer elsewhere.Close()

	repo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/artifact.fpm", http.StatusFound)
	}))
	defer repo.Close()

	client, err := NewClient(repo.URL, Credentials{Username: "deployer", Password: "s3cret"}, 10*time.Second)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	resp, err := client.Get(repo.URL + "/pkg.fpm")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if elsewhereSawAuth {
		t.Fatal("credentials followed a redirect to a different host")
	}
}

func TestDescribeAuthFailure(t *testing.T) {
	// A non-auth status has nothing to explain.
	if msg := DescribeAuthFailure("prod", "deployer", http.StatusNotFound); msg != "" {
		t.Fatalf("expected no guidance for 404, got %q", msg)
	}

	// No username configured: say how to configure one.
	msg := DescribeAuthFailure("prod", "", http.StatusUnauthorized)
	if !strings.Contains(msg, "--username") {
		t.Fatalf("guidance should mention configuring a username, got %q", msg)
	}

	// Username configured: point at the password source.
	msg = DescribeAuthFailure("prod", "deployer", http.StatusForbidden)
	if !strings.Contains(msg, PasswordEnvVar("prod")) {
		t.Fatalf("guidance should name the password variable, got %q", msg)
	}
}
