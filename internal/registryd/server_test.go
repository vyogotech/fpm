package registryd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"fpm/internal/archive"
	"fpm/internal/metadata"
	"fpm/internal/repository"
)

// These exercise the acceptance criteria in
// test/registry/features/registry_service.feature against real artifacts built
// by the same code the CLI uses, so the server and the client cannot disagree
// about what a package is.

const publisherToken = "tok_acme_publisher"

func newTestServer(t *testing.T) (*httptest.Server, *Store) {
	t.Helper()

	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("creating the store: %v", err)
	}

	auth := NewTokenAuthenticator([]Publisher{{
		Name:        "acme",
		Orgs:        []string{"acme"},
		TokenSHA256: HashToken(publisherToken),
	}})

	server := httptest.NewServer(NewServer(store, auth).Handler())
	t.Cleanup(server.Close)
	return server, store
}

// buildArtifact produces a genuine .fpm archive, including the content
// checksum the server re-verifies.
func buildArtifact(t *testing.T, org, appName, version string, compat []string) []byte {
	t.Helper()

	source := t.TempDir()
	moduleDir := filepath.Join(source, appName)
	if err := os.MkdirAll(moduleDir, 0o755); err != nil {
		t.Fatalf("preparing the app source: %v", err)
	}
	for name, body := range map[string]string{
		"__init__.py": fmt.Sprintf("__version__ = %q\n", version),
		"hooks.py":    fmt.Sprintf("app_name = %q\n", appName),
		"modules.txt": appName + "\n",
	} {
		if err := os.WriteFile(filepath.Join(moduleDir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}

	meta := &metadata.AppMetadata{
		PackageName:         appName,
		PackageVersion:      version,
		Description:         "Test package",
		Author:              "Acme Ltd",
		Org:                 org,
		AppName:             appName,
		FrappeCompatibility: compat,
		PackageType:         "dev",
	}

	// CreateFPMArchive takes an output *directory* and names the file itself.
	outDir := t.TempDir()
	// BundleDeps off: vendoring wheels would need pip and a network, and the
	// behaviour under test is the registry, not the packager.
	if err := archive.CreateFPMArchive(source, outDir, meta, version, archive.Options{BundleDeps: false}); err != nil {
		t.Fatalf("building the artifact: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(outDir, fmt.Sprintf("%s-%s.fpm", appName, version)))
	if err != nil {
		t.Fatalf("reading the artifact: %v", err)
	}
	return data
}

func put(t *testing.T, server *httptest.Server, path, token string, body []byte) *http.Response {
	t.Helper()

	req, err := http.NewRequest(http.MethodPut, server.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("issuing the request: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func get(t *testing.T, server *httptest.Server, path string) *http.Response {
	t.Helper()
	resp, err := server.Client().Get(server.URL + path)
	if err != nil {
		t.Fatalf("issuing the request: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func decode[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	var out T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding the response: %v", err)
	}
	return out
}

func publishWidget(t *testing.T, server *httptest.Server, version string) []byte {
	t.Helper()
	artifact := buildArtifact(t, "acme", "widget", version, []string{"15.x.x"})
	resp := put(t, server, "/"+ArtifactPath("acme", "widget", version), publisherToken, artifact)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("publishing %s: got status %d, want 201", version, resp.StatusCode)
	}
	return artifact
}

// ── the CLI contract ────────────────────────────────────────────────────────

func TestPublishedArtifactIsDownloadableByteForByte(t *testing.T) {
	server, _ := newTestServer(t)
	artifact := publishWidget(t, server, "1.0.0")

	resp := get(t, server, "/"+ArtifactPath("acme", "widget", "1.0.0"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got status %d, want 200", resp.StatusCode)
	}

	var downloaded bytes.Buffer
	if _, err := downloaded.ReadFrom(resp.Body); err != nil {
		t.Fatalf("reading the artifact: %v", err)
	}
	if !bytes.Equal(downloaded.Bytes(), artifact) {
		t.Error("the downloaded artifact does not match what was uploaded")
	}
}

func TestMetadataIsGeneratedFromTheArtifact(t *testing.T) {
	server, _ := newTestServer(t)
	publishWidget(t, server, "1.0.0")

	resp := get(t, server, "/metadata/acme/widget/package-metadata.json")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got status %d, want 200", resp.StatusCode)
	}

	meta := decode[repository.PackageMetadata](t, resp)
	entry, ok := meta.Versions["1.0.0"]
	if !ok {
		t.Fatal("the published version is missing from the metadata")
	}
	if entry.ChecksumSHA256 == "" {
		t.Error("the server did not record a checksum")
	}
	// Read straight from the manifest inside the artifact, so a consumer never
	// has to download and unpack a package to learn what it supports.
	if len(entry.FrappeCompatibility) == 0 {
		t.Error("declared Frappe compatibility was not carried into the metadata")
	}
}

func TestCatalogueIndexIsGenerated(t *testing.T) {
	server, _ := newTestServer(t)
	publishWidget(t, server, "1.0.0")

	index := decode[repository.RepositoryIndex](t, get(t, server, "/metadata/index.json"))
	if len(index.Packages) != 1 || index.Packages[0].AppName != "widget" {
		t.Fatalf("index = %+v, want one entry for widget", index.Packages)
	}
}

func TestClientSuppliedMetadataCannotForgeAChecksum(t *testing.T) {
	// fpm publish PUTs metadata after the artifact, and that document carries
	// fpm_path and checksum_sha256. Believing it would reopen exactly the hole
	// this service closes; rejecting it would break the client. So it is
	// accepted and the artifact-derived fields are kept.
	server, _ := newTestServer(t)
	publishWidget(t, server, "1.0.0")

	before := decode[repository.PackageMetadata](t,
		get(t, server, "/metadata/acme/widget/package-metadata.json"))
	realChecksum := before.Versions["1.0.0"].ChecksumSHA256

	forged, _ := json.Marshal(repository.PackageMetadata{
		Org: "acme", AppName: "widget",
		Versions: map[string]repository.PackageVersionMetadata{
			"1.0.0": {
				FPMPath:        "somewhere/else/evil.fpm",
				ChecksumSHA256: "0000000000000000000000000000000000000000000000000000000000000000",
			},
		},
	})

	resp := put(t, server, "/metadata/acme/widget/package-metadata.json", publisherToken, forged)
	if resp.StatusCode >= 400 {
		t.Fatalf("the client's metadata write should be accepted, got %d", resp.StatusCode)
	}

	after := decode[repository.PackageMetadata](t,
		get(t, server, "/metadata/acme/widget/package-metadata.json"))
	entry := after.Versions["1.0.0"]
	if entry.ChecksumSHA256 != realChecksum {
		t.Error("a client was able to overwrite the recorded checksum")
	}
	if entry.FPMPath != ArtifactPath("acme", "widget", "1.0.0") {
		t.Errorf("a client was able to repoint the artifact path to %q", entry.FPMPath)
	}
}

func TestIndexWriteFromTheClientIsAcceptedAndIgnored(t *testing.T) {
	// The client always rewrites the index after publishing. Refusing it would
	// make every successful publish print a warning.
	server, _ := newTestServer(t)
	publishWidget(t, server, "1.0.0")

	resp := put(t, server, "/metadata/index.json", publisherToken, []byte(`{"packages":[]}`))
	if resp.StatusCode >= 400 {
		t.Fatalf("got status %d, want the write to be accepted", resp.StatusCode)
	}

	index := decode[repository.RepositoryIndex](t, get(t, server, "/metadata/index.json"))
	if len(index.Packages) != 1 {
		t.Error("the client emptied the catalogue by writing the index")
	}
}

// ── authentication and ownership ────────────────────────────────────────────

func TestAnonymousPublishingIsRefused(t *testing.T) {
	server, _ := newTestServer(t)
	artifact := buildArtifact(t, "acme", "widget", "1.0.0", nil)

	resp := put(t, server, "/"+ArtifactPath("acme", "widget", "1.0.0"), "", artifact)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("got status %d, want 401", resp.StatusCode)
	}
}

func TestUnknownTokenIsRefused(t *testing.T) {
	server, _ := newTestServer(t)
	artifact := buildArtifact(t, "acme", "widget", "1.0.0", nil)

	resp := put(t, server, "/"+ArtifactPath("acme", "widget", "1.0.0"), "not-a-real-token", artifact)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("got status %d, want 401", resp.StatusCode)
	}
}

func TestPublisherCannotWriteToAnotherOrg(t *testing.T) {
	// The property the shared htpasswd could never provide: with one password
	// for the whole registry, any publisher could overwrite frappe/erpnext.
	server, _ := newTestServer(t)
	artifact := buildArtifact(t, "otherorg", "widget", "1.0.0", nil)

	resp := put(t, server, "/"+ArtifactPath("otherorg", "widget", "1.0.0"), publisherToken, artifact)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("got status %d, want 403", resp.StatusCode)
	}
}

func TestPublisherCannotWriteAnotherOrgsMetadata(t *testing.T) {
	server, _ := newTestServer(t)
	resp := put(t, server, "/metadata/otherorg/widget/package-metadata.json",
		publisherToken, []byte(`{"org":"otherorg","appName":"widget","versions":{}}`))
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("got status %d, want 403", resp.StatusCode)
	}
}

func TestReadsNeverRequireCredentials(t *testing.T) {
	server, _ := newTestServer(t)
	publishWidget(t, server, "1.0.0")

	for _, path := range []string{
		"/metadata/index.json",
		"/metadata/acme/widget/package-metadata.json",
		"/" + ArtifactPath("acme", "widget", "1.0.0"),
		"/health",
	} {
		if resp := get(t, server, path); resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: got status %d, want 200", path, resp.StatusCode)
		}
	}
}

// ── integrity ───────────────────────────────────────────────────────────────

func TestNonPackageBytesAreRefused(t *testing.T) {
	server, store := newTestServer(t)

	resp := put(t, server, "/"+ArtifactPath("acme", "widget", "1.0.0"),
		publisherToken, []byte("this is not a zip archive"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400", resp.StatusCode)
	}

	if _, found, _ := store.PackageMetadata("acme", "widget"); found {
		t.Error("a rejected upload left metadata behind")
	}
}

func TestTamperedArtifactIsRefused(t *testing.T) {
	// The archive records a checksum over its own contents. Re-running that
	// check server-side is what makes the recorded checksum an integrity
	// statement rather than a claim.
	server, store := newTestServer(t)
	artifact := buildArtifact(t, "acme", "widget", "1.0.0", nil)

	tampered := append([]byte(nil), artifact...)
	// Corrupt a byte in the middle of the payload, past the zip header.
	tampered[len(tampered)/2] ^= 0xFF

	resp := put(t, server, "/"+ArtifactPath("acme", "widget", "1.0.0"), publisherToken, tampered)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", resp.StatusCode)
	}
	if _, found, _ := store.PackageMetadata("acme", "widget"); found {
		t.Error("a tampered upload was recorded")
	}
}

func TestPathAndManifestCoordinatesMustAgree(t *testing.T) {
	server, _ := newTestServer(t)
	// A widget artifact uploaded to the gadget path: without this check a
	// publisher could park someone else's package under a name they control.
	artifact := buildArtifact(t, "acme", "widget", "1.0.0", nil)

	resp := put(t, server, "/"+ArtifactPath("acme", "gadget", "1.0.0"), publisherToken, artifact)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", resp.StatusCode)
	}
}

func TestVersionInPathMustMatchTheManifest(t *testing.T) {
	server, _ := newTestServer(t)
	artifact := buildArtifact(t, "acme", "widget", "1.0.0", nil)

	resp := put(t, server, "/"+ArtifactPath("acme", "widget", "2.0.0"), publisherToken, artifact)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", resp.StatusCode)
	}
}

func TestMalformedArtifactPathIsRefused(t *testing.T) {
	server, _ := newTestServer(t)
	artifact := buildArtifact(t, "acme", "widget", "1.0.0", nil)

	// Shallow paths were anonymously writable under the nginx configuration
	// because the protective rule only matched three-deep paths.
	for _, path := range []string{"/evil.fpm", "/acme/evil.fpm", "/acme/widget/evil.fpm"} {
		resp := put(t, server, path, publisherToken, artifact)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("PUT %s: got status %d, want 400", path, resp.StatusCode)
		}
	}
}

// ── correctness the static registry could not provide ───────────────────────

func TestLatestVersionUsesSemanticPrecedence(t *testing.T) {
	// The published bug: a string comparison ranked 1.9.0 above 1.10.0, so
	// `fpm install widget` installed the older package.
	server, _ := newTestServer(t)
	publishWidget(t, server, "1.9.0")
	publishWidget(t, server, "1.10.0")

	meta := decode[repository.PackageMetadata](t,
		get(t, server, "/metadata/acme/widget/package-metadata.json"))
	if meta.LatestVersion != "1.10.0" {
		t.Errorf("latest_version = %q, want 1.10.0", meta.LatestVersion)
	}
}

func TestRepublishingAVersionIsRefused(t *testing.T) {
	// Installs are expected to be reproducible; a version whose contents change
	// underneath its consumers is worse than one that is missing.
	server, _ := newTestServer(t)
	publishWidget(t, server, "1.0.0")

	again := buildArtifact(t, "acme", "widget", "1.0.0", nil)
	resp := put(t, server, "/"+ArtifactPath("acme", "widget", "1.0.0"), publisherToken, again)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("got status %d, want 409", resp.StatusCode)
	}
}

func TestConcurrentPublishesBothSurvive(t *testing.T) {
	// The static registry lost one of these: publishing was a read-modify-write
	// of a single shared index.json with no locking.
	server, _ := newTestServer(t)

	artifacts := map[string][]byte{
		"widget": buildArtifact(t, "acme", "widget", "1.0.0", nil),
		"gadget": buildArtifact(t, "acme", "gadget", "1.0.0", nil),
	}

	var wg sync.WaitGroup
	for name, artifact := range artifacts {
		wg.Add(1)
		go func(name string, artifact []byte) {
			defer wg.Done()
			put(t, server, "/"+ArtifactPath("acme", name, "1.0.0"), publisherToken, artifact)
		}(name, artifact)
	}
	wg.Wait()

	index := decode[repository.RepositoryIndex](t, get(t, server, "/metadata/index.json"))
	if len(index.Packages) != 2 {
		t.Errorf("index has %d packages, want 2 — a concurrent publish was lost", len(index.Packages))
	}
}

func TestDownloadsAreCounted(t *testing.T) {
	// Artifacts are served from the registry and never through the control
	// plane, so this is the only place a download can be observed.
	server, store := newTestServer(t)
	publishWidget(t, server, "1.0.0")

	artifactPath := ArtifactPath("acme", "widget", "1.0.0")
	get(t, server, "/"+artifactPath)
	get(t, server, "/"+artifactPath)

	if count := store.Downloads(artifactPath); count != 2 {
		t.Errorf("download count = %d, want 2", count)
	}
}
