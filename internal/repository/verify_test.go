package repository

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fpm/internal/config"
)

const (
	testOrg     = "testorg"
	testAppName = "testapp"
	testVersion = "1.0.0"
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// newMockRepo serves a package whose bytes are fpmBytes, advertising
// advertisedChecksum in its metadata. Passing a checksum that does not describe
// fpmBytes simulates a corrupted or substituted artifact.
func newMockRepo(t *testing.T, fpmBytes []byte, advertisedChecksum string) *httptest.Server {
	t.Helper()

	fpmPath := fmt.Sprintf("packages/%s/%s/%s/%s-%s.fpm", testOrg, testAppName, testVersion, testAppName, testVersion)
	meta := PackageMetadata{
		Org:           testOrg,
		AppName:       testAppName,
		LatestVersion: testVersion,
		Versions: map[string]PackageVersionMetadata{
			testVersion: {FPMPath: fpmPath, ChecksumSHA256: advertisedChecksum},
		},
	}
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("failed to marshal metadata: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "package-metadata.json"):
			w.Header().Set("Content-Type", "application/json")
			w.Write(metaBytes)
		case strings.HasSuffix(r.URL.Path, ".fpm"):
			w.Write(fpmBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testConfig(t *testing.T, repoURL string) *config.FPMConfig {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return &config.FPMConfig{
		AppsBasePath: filepath.Join(home, ".fpm", "apps"),
		Repositories: map[string]config.RepositoryConfig{
			"mockrepo": {Name: "mockrepo", URL: repoURL, Priority: 0},
		},
	}
}

func cachePathFor(t *testing.T) string {
	t.Helper()
	return filepath.Join(os.Getenv("HOME"), ".fpm", "cache", "mockrepo",
		testOrg, testAppName, testVersion, fmt.Sprintf("%s-%s.fpm", testAppName, testVersion))
}

// TestDownloadVerifiesChecksum is the happy path: metadata matches the served bytes.
func TestDownloadVerifiesChecksum(t *testing.T) {
	fpmBytes := []byte("a genuine fpm payload")
	srv := newMockRepo(t, fpmBytes, sha256Hex(fpmBytes))
	cfg := testConfig(t, srv.URL)

	info, err := FindPackageInRepos(cfg, testOrg, testAppName, testVersion)
	if err != nil {
		t.Fatalf("expected download to succeed, got: %v", err)
	}
	got, err := os.ReadFile(info.LocalPath)
	if err != nil {
		t.Fatalf("failed to read cached package: %v", err)
	}
	if string(got) != string(fpmBytes) {
		t.Fatalf("cached package content mismatch")
	}
}

// TestDownloadRejectsChecksumMismatch is the regression test for downloads never being
// verified: a repository serving bytes that do not match its own advertised checksum
// must not produce an installable package.
func TestDownloadRejectsChecksumMismatch(t *testing.T) {
	servedBytes := []byte("a tampered fpm payload")
	honestChecksum := sha256Hex([]byte("the original fpm payload"))
	srv := newMockRepo(t, servedBytes, honestChecksum)
	cfg := testConfig(t, srv.URL)

	info, err := FindPackageInRepos(cfg, testOrg, testAppName, testVersion)
	if err == nil {
		t.Fatalf("expected a checksum mismatch to be rejected, but got package at %s", info.LocalPath)
	}
	if info != nil {
		t.Fatalf("expected no package info on rejection, got %+v", info)
	}

	// The bad artifact must not be left in the cache for a later run to reuse.
	if _, statErr := os.Stat(cachePathFor(t)); !os.IsNotExist(statErr) {
		t.Fatalf("rejected package was left behind in the cache at %s", cachePathFor(t))
	}
}

// TestPoisonedCacheIsDiscardedAndRedownloaded covers the cache-hit path, which
// previously returned whatever was on disk without checking it.
func TestPoisonedCacheIsDiscardedAndRedownloaded(t *testing.T) {
	fpmBytes := []byte("a genuine fpm payload")
	srv := newMockRepo(t, fpmBytes, sha256Hex(fpmBytes))
	cfg := testConfig(t, srv.URL)

	// Plant a poisoned entry in the cache where a real download would land.
	cachePath := cachePathFor(t)
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o750); err != nil {
		t.Fatalf("failed to create cache dir: %v", err)
	}
	if err := os.WriteFile(cachePath, []byte("poisoned cache entry"), 0o644); err != nil {
		t.Fatalf("failed to plant poisoned cache entry: %v", err)
	}

	info, err := FindPackageInRepos(cfg, testOrg, testAppName, testVersion)
	if err != nil {
		t.Fatalf("expected recovery by re-download, got: %v", err)
	}

	got, err := os.ReadFile(info.LocalPath)
	if err != nil {
		t.Fatalf("failed to read cached package: %v", err)
	}
	if string(got) == "poisoned cache entry" {
		t.Fatal("poisoned cache entry was served instead of being discarded")
	}
	if string(got) != string(fpmBytes) {
		t.Fatalf("expected freshly downloaded content, got %q", string(got))
	}
}

// TestMissingChecksumIsRejected closes the bypass: a repository that omits
// checksum_sha256 would otherwise skip verification entirely while the client reported
// success, which is a weaker guarantee than having no verification at all.
func TestMissingChecksumIsRejected(t *testing.T) {
	fpmBytes := []byte("an unverifiable fpm payload")
	srv := newMockRepo(t, fpmBytes, "")
	cfg := testConfig(t, srv.URL)

	info, err := FindPackageInRepos(cfg, testOrg, testAppName, testVersion)
	if err == nil {
		t.Fatalf("a package with no recorded checksum must be rejected, got %+v", info)
	}
	if info != nil {
		t.Fatalf("expected no package info on rejection, got %+v", info)
	}

	// The unverifiable artifact must not be left behind for a later run to reuse.
	if _, statErr := os.Stat(cachePathFor(t)); !os.IsNotExist(statErr) {
		t.Fatalf("unverifiable package was left in the cache at %s", cachePathFor(t))
	}
}

// TestMissingChecksumCannotBeLaunderedThroughTheCache proves the rejection is not
// bypassable by planting a file where a download would land: the cache-hit path verifies
// too, so an unverifiable entry is discarded rather than served.
func TestMissingChecksumCannotBeLaunderedThroughTheCache(t *testing.T) {
	fpmBytes := []byte("an unverifiable fpm payload")
	srv := newMockRepo(t, fpmBytes, "")
	cfg := testConfig(t, srv.URL)

	cachePath := cachePathFor(t)
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o750); err != nil {
		t.Fatalf("failed to create cache dir: %v", err)
	}
	if err := os.WriteFile(cachePath, fpmBytes, 0o644); err != nil {
		t.Fatalf("failed to plant cache entry: %v", err)
	}

	if _, err := FindPackageInRepos(cfg, testOrg, testAppName, testVersion); err == nil {
		t.Fatal("a cached package with no recorded checksum must still be rejected")
	}
}

// TestVerifyFPMFileOrRemoveDeletesBadFile covers the helper directly.
func TestVerifyFPMFileOrRemoveDeletesBadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pkg.fpm")
	if err := os.WriteFile(path, []byte("content"), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	if err := verifyFPMFileOrRemove(path, "deadbeef", "pkg"); err == nil {
		t.Fatal("expected mismatch to be reported")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("expected the mismatching file to be removed")
	}

	// A matching checksum leaves the file in place.
	if err := os.WriteFile(path, []byte("content"), 0o644); err != nil {
		t.Fatalf("failed to rewrite file: %v", err)
	}
	if err := verifyFPMFileOrRemove(path, sha256Hex([]byte("content")), "pkg"); err != nil {
		t.Fatalf("expected matching checksum to verify, got: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected the verified file to be kept: %v", err)
	}
}
