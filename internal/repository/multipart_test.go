package repository

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"fpm/internal/config"
)

// fakeRegistry implements the split-upload protocol the way an R2-backed worker does:
// parts are held aside under an upload id and the object appears under its key only
// when the upload is completed.
type fakeRegistry struct {
	mu        sync.Mutex
	objects   map[string][]byte
	parts     map[string]map[int][]byte
	completed []string
	aborted   []string
	supported bool
	failPart  int // when non-zero, the part number to reject
}

func newFakeRegistry(supported bool) *fakeRegistry {
	return &fakeRegistry{objects: map[string][]byte{}, parts: map[string]map[int][]byte{}, supported: supported}
}

func (f *fakeRegistry) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := r.URL.Query().Get("action")
		key := r.URL.Path
		f.mu.Lock()
		defer f.mu.Unlock()

		if action != "" && !f.supported {
			http.Error(w, "unknown action", http.StatusNotFound)
			return
		}
		switch action {
		case "mpu-create":
			id := "upload-" + strconv.Itoa(len(f.parts)+1)
			f.parts[id] = map[int][]byte{}
			json.NewEncoder(w).Encode(map[string]string{"uploadId": id})
		case "mpu-uploadpart":
			id := r.URL.Query().Get("uploadId")
			n, _ := strconv.Atoi(r.URL.Query().Get("partNumber"))
			if f.failPart == n {
				http.Error(w, "part rejected", http.StatusInternalServerError)
				return
			}
			body, _ := io.ReadAll(r.Body)
			if f.parts[id] == nil {
				http.Error(w, "no such upload", http.StatusNotFound)
				return
			}
			f.parts[id][n] = body
			json.NewEncoder(w).Encode(UploadedPart{PartNumber: n, ETag: fmt.Sprintf("etag-%s-%d", id, n)})
		case "mpu-complete":
			id := r.URL.Query().Get("uploadId")
			var parts []UploadedPart
			json.NewDecoder(r.Body).Decode(&parts)
			var assembled []byte
			for _, p := range parts {
				assembled = append(assembled, f.parts[id][p.PartNumber]...)
			}
			// The object becomes visible only here.
			f.objects[key] = assembled
			f.completed = append(f.completed, id)
			delete(f.parts, id)
			w.WriteHeader(http.StatusOK)
		case "mpu-abort":
			id := r.URL.Query().Get("uploadId")
			delete(f.parts, id)
			f.aborted = append(f.aborted, id)
			w.WriteHeader(http.StatusNoContent)
		default: // a plain single-request PUT
			body, _ := io.ReadAll(r.Body)
			f.objects[key] = body
			w.WriteHeader(http.StatusOK)
		}
	})
}

func bigFile(t *testing.T, size int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "big.fpm")
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i % 251) // a pattern, so a misordered assembly is visible
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestMultipartUploadAssemblesTheWholeArtifact is the point of splitting: an artifact
// larger than a CDN's per-request limit arrives whole and byte-identical.
func TestMultipartUploadAssemblesTheWholeArtifact(t *testing.T) {
	reg := newFakeRegistry(true)
	srv := httptest.NewServer(reg.handler())
	defer srv.Close()

	size := MultipartPartSize*2 + 1234 // three parts, the last a different size
	path := bigFile(t, size)
	if err := UploadHTTPFileMultipart(srv.URL+"/frappe/builder/1.0.0/builder.fpm", path, srv.Client(), nil); err != nil {
		t.Fatal(err)
	}

	want, _ := os.ReadFile(path)
	got := reg.objects["/frappe/builder/1.0.0/builder.fpm"]
	if len(got) != len(want) {
		t.Fatalf("assembled %d bytes, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("assembled object differs at byte %d", i)
		}
	}
	if len(reg.completed) != 1 {
		t.Fatalf("expected one completed upload, got %v", reg.completed)
	}
}

// TestMultipartLeavesNothingVisibleUntilComplete is the atomicity the registry relies
// on: nothing can be pulled while parts are still arriving.
func TestMultipartLeavesNothingVisibleUntilComplete(t *testing.T) {
	reg := newFakeRegistry(true)
	seenDuringUpload := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("action") == "mpu-uploadpart" {
			reg.mu.Lock()
			_, visible := reg.objects[r.URL.Path]
			reg.mu.Unlock()
			seenDuringUpload[r.URL.Path] = seenDuringUpload[r.URL.Path] || visible
		}
		reg.handler().ServeHTTP(w, r)
	}))
	defer srv.Close()

	key := "/frappe/builder/1.0.0/builder.fpm"
	if err := UploadHTTPFileMultipart(srv.URL+key, bigFile(t, MultipartPartSize*2), srv.Client(), nil); err != nil {
		t.Fatal(err)
	}
	if seenDuringUpload[key] {
		t.Fatal("the object was readable while its parts were still being uploaded")
	}
	if _, ok := reg.objects[key]; !ok {
		t.Fatal("the object must exist once the upload completes")
	}
}

// TestMultipartAbortsOnFailure: a part that fails must not leave the registry holding
// the rest of an artifact that will never be completed.
func TestMultipartAbortsOnFailure(t *testing.T) {
	reg := newFakeRegistry(true)
	reg.failPart = 2
	srv := httptest.NewServer(reg.handler())
	defer srv.Close()

	key := "/frappe/builder/1.0.0/builder.fpm"
	err := UploadHTTPFileMultipart(srv.URL+key, bigFile(t, MultipartPartSize*2), srv.Client(), nil)
	if err == nil {
		t.Fatal("a rejected part must fail the upload")
	}
	if len(reg.aborted) != 1 {
		t.Fatalf("the upload must be aborted, got %v", reg.aborted)
	}
	if _, ok := reg.objects[key]; ok {
		t.Fatal("nothing may appear under the key when the upload failed")
	}
	if len(reg.parts) != 0 {
		t.Fatalf("no parts may be left behind: %v", reg.parts)
	}
}

// TestUploadFallsBackWhenTheRegistryIsOlder keeps this client working against a
// registry that has never heard of split uploads.
func TestUploadFallsBackWhenTheRegistryIsOlder(t *testing.T) {
	reg := newFakeRegistry(false)
	srv := httptest.NewServer(reg.handler())
	defer srv.Close()

	key := "/frappe/builder/1.0.0/builder.fpm"
	path := bigFile(t, MultipartThreshold+1)
	if err := UploadHTTPFile(srv.URL+key, path, http.MethodPut, "application/octet-stream", srv.Client(), "", nil); err != nil {
		t.Fatalf("the upload should fall back to a single request: %v", err)
	}
	want, _ := os.ReadFile(path)
	if len(reg.objects[key]) != len(want) {
		t.Fatalf("the artifact did not arrive whole: %d of %d bytes", len(reg.objects[key]), len(want))
	}
	if len(reg.completed) != 0 {
		t.Fatal("no split upload should have been attempted against a registry that refuses it")
	}
}

// TestSmallUploadStaysASingleRequest: splitting is for artifacts that need it. A
// metadata document must not turn into a three-call protocol.
func TestSmallUploadStaysASingleRequest(t *testing.T) {
	reg := newFakeRegistry(true)
	srv := httptest.NewServer(reg.handler())
	defer srv.Close()

	path := bigFile(t, 4096)
	if err := UploadHTTPFile(srv.URL+"/metadata/x.json", path, http.MethodPut, "application/json", srv.Client(), "", nil); err != nil {
		t.Fatal(err)
	}
	if len(reg.completed) != 0 || len(reg.parts) != 0 {
		t.Fatal("a small file must not be split")
	}
}

// conditionalRegistry serves package metadata with an ETag and refuses a write built
// on a stale one, the way R2's conditional put does.
type conditionalRegistry struct {
	mu       sync.Mutex
	doc      []byte
	etag     string
	rejected int
	// raceOnce writes a competing update just before the first conditional write
	// lands, which is the interleaving that used to lose a version.
	raceOnce bool
}

func (c *conditionalRegistry) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			if c.doc == nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("ETag", c.etag)
			w.Write(c.doc)
		case http.MethodPut:
			if c.raceOnce {
				// Someone else publishes between this client's read and its write.
				c.doc = []byte(`{"org":"frappe","appName":"lms","versions":{"9.9.9":{}}}`)
				c.etag = "etag-race"
				c.raceOnce = false
			}
			if want := r.Header.Get("If-Match"); want != "" && want != c.etag {
				c.rejected++
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			body, _ := io.ReadAll(r.Body)
			c.doc = body
			c.etag = fmt.Sprintf("etag-%d", len(body))
			w.WriteHeader(http.StatusOK)
		}
	})
}

// TestMetadataWriteIsConditional: the publish carries the entity tag it read, so a
// registry can refuse a write built on a document that has since changed.
func TestMetadataWriteIsConditional(t *testing.T) {
	reg := &conditionalRegistry{doc: []byte(`{"org":"frappe","appName":"lms","versions":{}}`), etag: "etag-original"}
	srv := httptest.NewServer(reg.handler())
	defer srv.Close()

	meta, found, err := FetchRemotePackageMetadataForRepo(
		configRepo(srv.URL), "frappe", "lms", srv.Client())
	if err != nil || !found {
		t.Fatalf("fetch: %v found=%v", err, found)
	}
	if meta.SourceETag() != "etag-original" {
		t.Fatalf("the entity tag must be carried from the read, got %q", meta.SourceETag())
	}

	if err := UploadPackageMetadata(srv.URL, "frappe", "lms", meta, srv.Client()); err != nil {
		t.Fatalf("an unchanged document must accept the write: %v", err)
	}

	// Now write back a copy whose tag is stale.
	meta.SetSourceETag("etag-original")
	err = UploadPackageMetadata(srv.URL, "frappe", "lms", meta, srv.Client())
	if !errors.Is(err, ErrMetadataChanged) {
		t.Fatalf("a stale write must be refused with ErrMetadataChanged, got %v", err)
	}
	if reg.rejected != 1 {
		t.Fatalf("the registry should have rejected exactly one write, got %d", reg.rejected)
	}
}

// TestMetadataCreateIsConditionalToo: a document that did not exist at read time is
// created, not blindly overwritten — otherwise the first writer's version is lost.
func TestMetadataCreateIsConditionalToo(t *testing.T) {
	reg := &conditionalRegistry{}
	var sawIfNoneMatch string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			sawIfNoneMatch = r.Header.Get("If-None-Match")
		}
		reg.handler().ServeHTTP(w, r)
	}))
	defer srv.Close()

	meta := &PackageMetadata{Org: "frappe", AppName: "lms"}
	if err := UploadPackageMetadata(srv.URL, "frappe", "lms", meta, srv.Client()); err != nil {
		t.Fatal(err)
	}
	if sawIfNoneMatch != "*" {
		t.Fatalf("a first write must be conditional on absence, got %q", sawIfNoneMatch)
	}
}

func configRepo(u string) config.RepositoryConfig {
	return config.RepositoryConfig{Name: "test", URL: u}
}
