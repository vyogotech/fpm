package repository

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"fpm/internal/config"
	"sort"
	"strings"
	"time"
)

// IndexPath is the repository-wide catalogue of published packages, relative to the
// repository root. Per-package metadata lives at metadata/<org>/<app>/package-metadata.json,
// which can only be fetched by a client that already knows both names; the index is what
// makes discovery by keyword possible at all.
const IndexPath = "metadata/index.json"

// IndexEntry summarises one package in a repository, carrying just enough to answer a
// search without fetching every package's full metadata.
type IndexEntry struct {
	Org           string `json:"org"`
	AppName       string `json:"appName"`
	Title         string `json:"title,omitempty"`
	Description   string `json:"description,omitempty"`
	Icon          string `json:"icon,omitempty"`
	IconFile      string `json:"icon_file,omitempty"`
	LatestVersion string `json:"latest_version,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
}

// RepositoryIndex is the catalogue of every package published to a repository.
type RepositoryIndex struct {
	Packages []IndexEntry `json:"packages"`
}

// Upsert records a package in the index, replacing any existing entry for the same
// org and app name. Entries are kept sorted so the published file is stable across runs.
func (idx *RepositoryIndex) Upsert(entry IndexEntry) {
	for i, existing := range idx.Packages {
		if existing.Org == entry.Org && existing.AppName == entry.AppName {
			idx.Packages[i] = entry
			return
		}
	}
	idx.Packages = append(idx.Packages, entry)
	sort.Slice(idx.Packages, func(i, j int) bool {
		if idx.Packages[i].Org != idx.Packages[j].Org {
			return idx.Packages[i].Org < idx.Packages[j].Org
		}
		return idx.Packages[i].AppName < idx.Packages[j].AppName
	})
}

// FetchRepositoryIndexForRepo retrieves a repository's package catalogue, dispatching
// on the backend type.
//
// An OCI registry has no index: a registry is addressed by repository name, and there
// is nowhere to publish a catalogue that `docker pull` semantics would find. Asking one
// for metadata/index.json builds a URL with no scheme and fails with "unsupported
// protocol scheme", which reads like a network fault rather than "this backend cannot
// answer that question". Reported as simply absent instead, which every caller already
// handles: a repository without an index is usable, just not searchable by keyword.
func FetchRepositoryIndexForRepo(repo config.RepositoryConfig, client *http.Client) (*RepositoryIndex, bool, error) {
	if strings.EqualFold(repo.Type, "oci") {
		return nil, false, nil
	}
	return FetchRepositoryIndex(repo.URL, client)
}

// FetchRepositoryIndex retrieves a repository's package catalogue.
//
// The boolean reports whether an index exists. A repository that has never had one
// published is not an error: callers fall back to a targeted lookup, so a repository
// without an index stays usable, just not searchable by keyword.
func FetchRepositoryIndex(repoBaseURL string, client *http.Client) (*RepositoryIndex, bool, error) {
	if client == nil {
		client = &http.Client{Timeout: time.Second * 30}
	}

	fullURL, err := url.JoinPath(repoBaseURL, IndexPath)
	if err != nil {
		return nil, false, fmt.Errorf("error constructing index URL for repo %s: %w", repoBaseURL, err)
	}

	req, err := http.NewRequest(http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, false, fmt.Errorf("error creating request for %s: %w", fullURL, err)
	}
	req.Header.Set("User-Agent", "fpm-client/0.1.0")

	resp, err := client.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("failed to fetch index %s: %w", fullURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, false, fmt.Errorf("failed to fetch index %s (status: %s). Response: %s",
			fullURL, resp.Status, string(bodyBytes))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, fmt.Errorf("failed to read index from %s: %w", fullURL, err)
	}

	var idx RepositoryIndex
	if err := json.Unmarshal(body, &idx); err != nil {
		return nil, false, fmt.Errorf("failed to parse index from %s: %w", fullURL, err)
	}
	return &idx, true, nil
}

// UploadRepositoryIndex publishes the package catalogue back to the repository.
func UploadRepositoryIndex(repoBaseURL string, idx *RepositoryIndex, client *http.Client) error {
	if client == nil {
		client = &http.Client{Timeout: time.Second * 60}
	}

	jsonData, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal repository index: %w", err)
	}

	fullURL, err := url.JoinPath(repoBaseURL, IndexPath)
	if err != nil {
		return fmt.Errorf("error constructing index upload URL for repo %s: %w", repoBaseURL, err)
	}

	req, err := http.NewRequest(http.MethodPut, fullURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create PUT request for index %s: %w", fullURL, err)
	}
	req.Header.Set("User-Agent", "fpm-client/0.1.0")
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to upload index to %s: %w", fullURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated &&
		resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusNoContent {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("failed to upload index to %s (status: %s). Response: %s",
			fullURL, resp.Status, string(bodyBytes))
	}
	return nil
}

// Match reports whether an entry satisfies a lowercased search query. An empty query
// matches everything, mirroring how the local store and cache are searched.
func (e IndexEntry) Match(query string) bool {
	if query == "" {
		return true
	}
	fields := []string{
		strings.ToLower(e.Org),
		strings.ToLower(e.AppName),
		strings.ToLower(e.Description),
		strings.ToLower(e.Org) + "/" + strings.ToLower(e.AppName),
	}
	for _, f := range fields {
		if strings.Contains(f, query) {
			return true
		}
	}
	return false
}
