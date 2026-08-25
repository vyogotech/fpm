//go:build integration

// Package registry_test runs the acceptance suite described in
// features/registry_auth.feature against a real nginx.
//
// The suite runs twice: once against nginx/nginx.conf as shipped for
// docker/podman, and once against the config rendered by the Helm chart. Both
// copies previously carried the same authentication hole independently, so
// running one suite against both is what stops them drifting apart again.
//
// These are integration tests: they need a container runtime (podman or
// docker) and the nginx:alpine image. The Helm variant additionally needs the
// helm binary and is skipped without it. Run them with:
//
//	go test -tags integration ./test/registry/...
package registry_test

import (
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	publisherUser = "publisher"
	publisherPass = "s3cr3t-publisher-pw"
	nginxImage    = "nginx:alpine"
)

// seedFiles are written into the repository root before nginx starts, so that
// read scenarios have something to return.
var seedFiles = map[string]string{
	"metadata/index.json":                        `{"packages":[{"org":"acme","appName":"widget","latest_version":"1.0.0"}]}`,
	"metadata/acme/widget/package-metadata.json": `{"org":"acme","appName":"widget","latest_version":"1.0.0","versions":{}}`,
	"acme/widget/1.0.0/widget-1.0.0.fpm":         "PK\x03\x04 not-a-real-zip",
}

// fixture describes one deployment flavour of the registry configuration.
type fixture struct {
	name string
	// build writes nginx.conf and fpm-location.conf into dir and returns the
	// path at which the htpasswd file must be mounted, which differs between
	// the compose and Helm layouts.
	build func(t *testing.T, dir string) (confPath, policyPath, htpasswdMount string)
}

func fixtures() []fixture {
	return []fixture{
		{name: "compose", build: buildRepoConfig},
		{name: "helm", build: buildHelmConfig},
	}
}

// buildRepoConfig uses nginx/nginx.conf as committed.
func buildRepoConfig(t *testing.T, _ string) (string, string, string) {
	t.Helper()
	root := repoRoot(t)
	confPath := filepath.Join(root, "nginx", "nginx.conf")
	policyPath := filepath.Join(root, "nginx", "fpm-location.conf")
	for _, path := range []string{confPath, policyPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
	}
	return confPath, policyPath, "/etc/nginx/.htpasswd"
}

// buildHelmConfig renders the chart and extracts the two ConfigMap keys.
func buildHelmConfig(t *testing.T, dir string) (string, string, string) {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not on PATH")
	}

	chartDir := filepath.Join(repoRoot(t), "charts", "fpm-registry")
	rendered, err := exec.Command("helm", "template", "acceptance", chartDir,
		"--show-only", "templates/configmap.yaml",
		"--set", "auth.username="+publisherUser,
		"--set", "auth.password="+publisherPass,
		// backend.enabled=true (the default) intentionally omits dav_methods
		// from nginx because the write-path service handles writes. The
		// acceptance test validates the standalone mode where nginx itself
		// handles WebDAV, so we override the default here.
		"--set", "backend.enabled=false",
	).CombinedOutput()
	if err != nil {
		t.Fatalf("rendering the chart: %v\n%s", err, rendered)
	}

	confPath := filepath.Join(dir, "nginx.conf")
	policyPath := filepath.Join(dir, "fpm-location.conf")
	for key, dest := range map[string]string{
		"nginx.conf":        confPath,
		"fpm-location.conf": policyPath,
	} {
		body, ok := extractBlockScalar(string(rendered), key)
		if !ok {
			t.Fatalf("the rendered ConfigMap has no %q key:\n%s", key, rendered)
		}
		if err := os.WriteFile(dest, []byte(body), 0o644); err != nil {
			t.Fatalf("writing %s: %v", dest, err)
		}
	}
	// The chart mounts the credential under a directory, unlike compose.
	return confPath, policyPath, "/etc/nginx/auth/.htpasswd"
}

// extractBlockScalar pulls a `key: |` literal block out of rendered YAML and
// dedents it. This avoids taking a YAML dependency for one test.
func extractBlockScalar(doc, key string) (string, bool) {
	lines := strings.Split(doc, "\n")
	start := -1
	var indent string
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == key+": |" || trimmed == key+": |-" {
			start = i + 1
			indent = strings.Repeat(" ", len(line)-len(strings.TrimLeft(line, " "))+2)
			break
		}
	}
	if start < 0 {
		return "", false
	}

	var body []string
	for _, line := range lines[start:] {
		if strings.TrimSpace(line) == "" {
			body = append(body, "")
			continue
		}
		if !strings.HasPrefix(line, indent) {
			break
		}
		body = append(body, strings.TrimPrefix(line, indent))
	}
	return strings.Join(body, "\n") + "\n", true
}

// registry is a running nginx container serving one fixture's configuration.
type registry struct {
	baseURL string
	client  *http.Client
}

// containerRuntime returns podman or docker, whichever is on PATH.
func containerRuntime(t *testing.T) string {
	t.Helper()
	for _, candidate := range []string{"podman", "docker"} {
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}
	t.Skip("no container runtime (podman or docker) on PATH")
	return ""
}

// repoRoot walks up from this source file to the fpm repository root.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine the path of this test file")
	}
	// this file lives at <root>/test/registry/registry_auth_test.go
	return filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
}

// htpasswdLine builds an nginx-compatible entry using the {SHA} scheme, which
// nginx supports natively and which needs no external htpasswd binary.
func htpasswdLine(user, password string) string {
	sum := sha1.Sum([]byte(password))
	return fmt.Sprintf("%s:{SHA}%s\n", user, base64.StdEncoding.EncodeToString(sum[:]))
}

// freePort asks the kernel for an unused localhost port.
func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

// startRegistry boots nginx for one fixture. The container is removed when the
// test finishes.
func startRegistry(t *testing.T, f fixture) *registry {
	t.Helper()

	runtimeBin := containerRuntime(t)
	workDir := t.TempDir()
	confPath, policyPath, htpasswdMount := f.build(t, workDir)

	dataDir := filepath.Join(workDir, "data")
	for relPath, contents := range seedFiles {
		full := filepath.Join(dataDir, relPath)
		if err := os.MkdirAll(filepath.Dir(full), 0o777); err != nil {
			t.Fatalf("seeding %s: %v", relPath, err)
		}
		if err := os.WriteFile(full, []byte(contents), 0o666); err != nil {
			t.Fatalf("seeding %s: %v", relPath, err)
		}
	}
	// nginx workers run as an unprivileged user and must be able to create
	// files here for the WebDAV PUT paths to work at all.
	if err := chmodTree(dataDir, 0o777); err != nil {
		t.Fatalf("relaxing permissions on the repository root: %v", err)
	}

	htpasswdPath := filepath.Join(workDir, "htpasswd")
	if err := os.WriteFile(htpasswdPath, []byte(htpasswdLine(publisherUser, publisherPass)), 0o644); err != nil {
		t.Fatalf("writing htpasswd: %v", err)
	}

	port := freePort(t)
	name := fmt.Sprintf("fpm-registry-test-%s-%d", f.name, port)

	args := []string{
		"run", "--detach", "--name", name,
		"--publish", fmt.Sprintf("127.0.0.1:%d:80", port),
		"--volume", confPath + ":/etc/nginx/nginx.conf:ro",
		"--volume", policyPath + ":/etc/nginx/fpm-location.conf:ro",
		"--volume", htpasswdPath + ":" + htpasswdMount + ":ro",
		"--volume", dataDir + ":/var/fpm-repo",
		nginxImage,
	}
	if output, err := exec.Command(runtimeBin, args...).CombinedOutput(); err != nil {
		t.Fatalf("starting nginx: %v\n%s", err, output)
	}

	t.Cleanup(func() {
		if t.Failed() {
			if logs, err := exec.Command(runtimeBin, "logs", name).CombinedOutput(); err == nil {
				t.Logf("nginx logs:\n%s", logs)
			}
		}
		_ = exec.Command(runtimeBin, "rm", "--force", name).Run()
	})

	reg := &registry{
		baseURL: fmt.Sprintf("http://127.0.0.1:%d", port),
		client: &http.Client{
			Timeout: 10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	reg.waitUntilHealthy(t, runtimeBin, name)
	return reg
}

func chmodTree(root string, mode os.FileMode) error {
	return filepath.Walk(root, func(path string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chmod(path, mode)
	})
}

func (r *registry) waitUntilHealthy(t *testing.T, runtimeBin, name string) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := r.client.Get(r.baseURL + "/health")
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	if logs, err := exec.Command(runtimeBin, "logs", name).CombinedOutput(); err == nil {
		t.Logf("nginx logs:\n%s", logs)
	}
	t.Fatal("nginx did not become healthy within 45s")
}

// do issues a request. When user is empty the request is anonymous.
func (r *registry) do(t *testing.T, method, path, user, password string) *http.Response {
	t.Helper()
	var body io.Reader
	if method == http.MethodPut {
		body = strings.NewReader("payload written by the acceptance suite")
	}
	req, err := http.NewRequest(method, r.baseURL+path, body)
	if err != nil {
		t.Fatalf("building %s %s: %v", method, path, err)
	}
	if user != "" {
		req.SetBasicAuth(user, password)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		t.Fatalf("issuing %s %s: %v", method, path, err)
	}
	t.Cleanup(func() {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	})
	return resp
}

func (r *registry) anonymous(t *testing.T, method, path string) *http.Response {
	t.Helper()
	return r.do(t, method, path, "", "")
}

func (r *registry) asPublisher(t *testing.T, method, path string) *http.Response {
	t.Helper()
	return r.do(t, method, path, publisherUser, publisherPass)
}

func assertStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode != want {
		t.Errorf("got status %d, want %d", resp.StatusCode, want)
	}
}

func assertStatusOneOf(t *testing.T, resp *http.Response, want ...int) {
	t.Helper()
	for _, code := range want {
		if resp.StatusCode == code {
			return
		}
	}
	t.Errorf("got status %d, want one of %v", resp.StatusCode, want)
}

// eachFixture runs body against every deployment flavour of the config.
func eachFixture(t *testing.T, body func(t *testing.T, reg *registry)) {
	t.Helper()
	for _, f := range fixtures() {
		t.Run(f.name, func(t *testing.T) {
			body(t, startRegistry(t, f))
		})
	}
}

// ── Public reads stay public ───────────────────────────────────────────

func TestAnonymousReadsAreAllowed(t *testing.T) {
	eachFixture(t, func(t *testing.T, reg *registry) {
		for name, path := range map[string]string{
			"catalogue index":  "/metadata/index.json",
			"package metadata": "/metadata/acme/widget/package-metadata.json",
			"artifact":         "/acme/widget/1.0.0/widget-1.0.0.fpm",
			"health probe":     "/health",
		} {
			t.Run(name, func(t *testing.T) {
				assertStatus(t, reg.anonymous(t, http.MethodGet, path), http.StatusOK)
			})
		}
	})
}

// ── Writes require credentials ─────────────────────────────────────────

func TestAnonymousWritesAreRejected(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
	}{
		// package-metadata.json carries fpm_path and checksum_sha256, so an
		// anonymous writer here can repoint a package at any artifact and
		// forge its checksum.
		{"overwrite the catalogue index", http.MethodPut, "/metadata/index.json"},
		{"overwrite package metadata", http.MethodPut, "/metadata/acme/widget/package-metadata.json"},
		{"delete package metadata", http.MethodDelete, "/metadata/acme/widget/package-metadata.json"},

		// Artifact paths at every depth. A server-level regex cannot protect
		// these: once a nested location matches, nginx never evaluates
		// server-level regex locations.
		{"write a well-formed artifact path", http.MethodPut, "/acme/widget/1.0.0/widget-1.0.0.fpm"},
		{"write a shallow artifact path", http.MethodPut, "/acme/evil.fpm"},
		{"write a top-level artifact path", http.MethodPut, "/evil.fpm"},
	}

	eachFixture(t, func(t *testing.T, reg *registry) {
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				assertStatus(t, reg.anonymous(t, tc.method, tc.path), http.StatusUnauthorized)
			})
		}
	})
}

func TestCredentialedPublishingStillWorks(t *testing.T) {
	eachFixture(t, func(t *testing.T, reg *registry) {
		for name, path := range map[string]string{
			"upload an artifact":         "/acme/widget/2.0.0/widget-2.0.0.fpm",
			"update package metadata":    "/metadata/acme/widget/package-metadata.json",
			"update the catalogue index": "/metadata/index.json",
		} {
			t.Run(name, func(t *testing.T) {
				assertStatusOneOf(t, reg.asPublisher(t, http.MethodPut, path),
					http.StatusOK, http.StatusCreated, http.StatusAccepted, http.StatusNoContent)
			})
		}
	})
}

// ── CORS must survive the nested locations ─────────────────────────────

// A nested location that declares its own add_header discards every add_header
// inherited from its parent, so CORS vanishes on exactly the paths a browser
// client needs.
func TestReadsCarryCORSHeaders(t *testing.T) {
	eachFixture(t, func(t *testing.T, reg *registry) {
		for name, path := range map[string]string{
			"catalogue index":  "/metadata/index.json",
			"package metadata": "/metadata/acme/widget/package-metadata.json",
			"artifact":         "/acme/widget/1.0.0/widget-1.0.0.fpm",
		} {
			t.Run(name, func(t *testing.T) {
				resp := reg.anonymous(t, http.MethodGet, path)
				if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
					t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, "*")
				}
			})
		}
	})
}
