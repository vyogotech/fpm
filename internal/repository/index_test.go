package repository

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestIndexUpsertReplacesExistingEntry(t *testing.T) {
	idx := &RepositoryIndex{}
	idx.Upsert(IndexEntry{Org: "acme", AppName: "my_app", LatestVersion: "1.0.0"})
	idx.Upsert(IndexEntry{Org: "acme", AppName: "my_app", LatestVersion: "2.0.0"})

	if len(idx.Packages) != 1 {
		t.Fatalf("republishing a package should update its entry, not add one: %+v", idx.Packages)
	}
	if idx.Packages[0].LatestVersion != "2.0.0" {
		t.Fatalf("expected latest version 2.0.0, got %q", idx.Packages[0].LatestVersion)
	}
}

func TestIndexUpsertKeepsEntriesSorted(t *testing.T) {
	idx := &RepositoryIndex{}
	idx.Upsert(IndexEntry{Org: "zeta", AppName: "app"})
	idx.Upsert(IndexEntry{Org: "acme", AppName: "zebra"})
	idx.Upsert(IndexEntry{Org: "acme", AppName: "alpha"})

	want := []string{"acme/alpha", "acme/zebra", "zeta/app"}
	for i, w := range want {
		got := idx.Packages[i].Org + "/" + idx.Packages[i].AppName
		if got != w {
			t.Fatalf("entry %d: expected %q, got %q", i, w, got)
		}
	}
}

func TestIndexEntryMatch(t *testing.T) {
	entry := IndexEntry{Org: "acme", AppName: "inventory", Description: "Stock management"}

	for _, q := range []string{"", "acme", "invent", "acme/inv", "stock"} {
		if !entry.Match(q) {
			t.Fatalf("query %q should match %+v", q, entry)
		}
	}
	for _, q := range []string{"payroll", "zeta"} {
		if entry.Match(q) {
			t.Fatalf("query %q should not match %+v", q, entry)
		}
	}
}

// TestFetchRepositoryIndexAbsent covers a repository that has never published an index.
// That must not be an error: callers fall back to a targeted lookup.
func TestFetchRepositoryIndexAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	idx, found, err := FetchRepositoryIndex(srv.URL, nil)
	if err != nil {
		t.Fatalf("a missing index should not be an error, got: %v", err)
	}
	if found {
		t.Fatal("expected found=false for a repository with no index")
	}
	if idx != nil {
		t.Fatalf("expected no index, got %+v", idx)
	}
}

func TestFetchAndUploadRepositoryIndexRoundTrip(t *testing.T) {
	var mu sync.Mutex
	var stored []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, IndexPath) {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			buf := make([]byte, r.ContentLength)
			r.Body.Read(buf)
			stored = buf
			w.WriteHeader(http.StatusCreated)
		case http.MethodGet:
			if stored == nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(stored)
		default:
			http.Error(w, "unsupported", http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()

	idx := &RepositoryIndex{}
	idx.Upsert(IndexEntry{Org: "acme", AppName: "my_app", LatestVersion: "1.0.0", Description: "Test"})
	if err := UploadRepositoryIndex(srv.URL, idx, nil); err != nil {
		t.Fatalf("UploadRepositoryIndex failed: %v", err)
	}

	fetched, found, err := FetchRepositoryIndex(srv.URL, nil)
	if err != nil {
		t.Fatalf("FetchRepositoryIndex failed: %v", err)
	}
	if !found || fetched == nil {
		t.Fatal("expected the uploaded index to be found")
	}
	if len(fetched.Packages) != 1 || fetched.Packages[0].AppName != "my_app" {
		t.Fatalf("round-tripped index mismatch: %+v", fetched.Packages)
	}
}

func TestFetchRepositoryIndexMalformed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("{not json"))
	}))
	defer srv.Close()

	if _, _, err := FetchRepositoryIndex(srv.URL, nil); err == nil {
		t.Fatal("a malformed index should be reported, not silently ignored")
	}
}

// TestIndexPathIsUnderMetadata keeps the catalogue beside per-package metadata, which is
// what the nginx repository config serves and protects.
func TestIndexPathIsUnderMetadata(t *testing.T) {
	if !strings.HasPrefix(IndexPath, "metadata/") {
		t.Fatalf("index should live under metadata/, got %q", IndexPath)
	}
	var idx RepositoryIndex
	if err := json.Unmarshal([]byte(`{"packages":[]}`), &idx); err != nil {
		t.Fatalf("index should decode an empty package list: %v", err)
	}
}
