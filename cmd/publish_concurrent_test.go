package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"fpm/internal/config"
	"fpm/internal/repository"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// racingRegistry serves package metadata with an ETag and refuses a stale write, and
// lets another publisher slip a version in between this one's read and its write.
type racingRegistry struct {
	mu      sync.Mutex
	doc     repository.PackageMetadata
	etag    string
	puts    int
	rejects int
	// interlopeAt is the write attempt just before which a competing publish lands.
	interlopeAt int
}

func (r *racingRegistry) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		switch req.Method {
		case http.MethodGet:
			w.Header().Set("ETag", r.etag)
			json.NewEncoder(w).Encode(r.doc)
		case http.MethodPut:
			r.puts++
			if r.puts == r.interlopeAt {
				// Another publish of the same app completes first.
				r.doc.Versions["7.7.7"] = repository.PackageVersionMetadata{FPMPath: "other"}
				r.etag = "etag-after-interloper"
			}
			if want := req.Header.Get("If-Match"); want != "" && want != r.etag {
				r.rejects++
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			body, _ := io.ReadAll(req.Body)
			var incoming repository.PackageMetadata
			json.Unmarshal(body, &incoming)
			r.doc = incoming
			r.etag = "etag-written"
			w.WriteHeader(http.StatusOK)
		}
	})
}

// TestConcurrentPublishKeepsBothVersions is the lost update this guards against: two
// publishes of one app overlap, and the one that loses the race must re-read and
// re-apply rather than write back a document that never saw the other's version —
// which would leave that artifact uploaded with nothing pointing at it.
func TestConcurrentPublishKeepsBothVersions(t *testing.T) {
	reg := &racingRegistry{
		doc:         repository.PackageMetadata{Org: "frappe", AppName: "lms", Versions: map[string]repository.PackageVersionMetadata{}},
		etag:        "etag-original",
		interlopeAt: 1, // the competing publish lands before our first write
	}
	srv := httptest.NewServer(reg.handler())
	defer srv.Close()

	repo := config.RepositoryConfig{Name: "test", URL: srv.URL}
	mine := repository.PackageVersionMetadata{FPMPath: "frappe/lms/2.62.1/lms-2.62.1.fpm"}

	// What publish holds: the document as it was read, with this version spliced in.
	meta, found, err := repository.FetchRemotePackageMetadataForRepo(repo, "frappe", "lms", srv.Client())
	require.NoError(t, err)
	require.True(t, found)
	meta.Versions["2.62.1"] = mine

	err = publishMetadataWithRetry(repo, "frappe", "lms", mine, "2.62.1", meta, srv.Client())
	require.NoError(t, err, "losing the race once must be recoverable")

	assert.Equal(t, 1, reg.rejects, "the stale write must have been refused exactly once")
	assert.Contains(t, reg.doc.Versions, "2.62.1", "this publish's version must survive")
	assert.Contains(t, reg.doc.Versions, "7.7.7", "the other publish's version must not be lost")
}

// TestConcurrentPublishGivesUpEventually: something publishing this app continuously
// is worth reporting rather than looping over forever.
func TestConcurrentPublishGivesUpEventually(t *testing.T) {
	reg := &racingRegistry{
		doc:         repository.PackageMetadata{Org: "frappe", AppName: "lms", Versions: map[string]repository.PackageVersionMetadata{}},
		etag:        "etag-original",
		interlopeAt: -1,
	}
	// Every write loses: the etag moves on each attempt.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		reg.mu.Lock()
		defer reg.mu.Unlock()
		if req.Method == http.MethodGet {
			w.Header().Set("ETag", reg.etag)
			json.NewEncoder(w).Encode(reg.doc)
			return
		}
		reg.rejects++
		reg.etag = "etag-moved-again"
		w.WriteHeader(http.StatusPreconditionFailed)
	}))
	defer srv.Close()

	repo := config.RepositoryConfig{Name: "test", URL: srv.URL}
	meta, _, err := repository.FetchRemotePackageMetadataForRepo(repo, "frappe", "lms", srv.Client())
	require.NoError(t, err)

	err = publishMetadataWithRetry(repo, "frappe", "lms", repository.PackageVersionMetadata{}, "2.62.1", meta, srv.Client())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "another publish keeps updating")
	assert.Equal(t, metadataPublishAttempts, reg.rejects, "it must stop after a bounded number of attempts")
}
