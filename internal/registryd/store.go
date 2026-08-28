// Package registryd is the registry's write path.
//
// Reads stay exactly what they were: static files under a document root,
// servable by nginx, a CDN or this process. Only writes move behind a service,
// because that is where the static registry could not be made correct:
//
//   - WebDAV had no way to authenticate a *publisher*, only a shared password,
//     so every credentialed publisher could overwrite every other org.
//   - The client supplied fpm_path and checksum_sha256 in the metadata it PUT,
//     so the checksum recorded for an artifact was whatever the uploader said
//     it was — which is not an integrity check.
//   - Publishing was a read-modify-write of one shared index.json with no
//     locking, so simultaneous publishes lost each other.
//
// The service keeps the same URLs, methods and status codes, so an unmodified
// fpm client cannot tell the difference beyond the base URL.
package registryd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"fpm/internal/metadata"
	"fpm/internal/repository"
	"fpm/internal/semver"
)

// ErrVersionExists is returned when a version has already been published.
// Republishing is refused because installs are expected to be reproducible: a
// version that changes underneath its consumers is worse than a missing one.
var ErrVersionExists = errors.New("this version has already been published")

const (
	indexRelPath   = "metadata/index.json"
	metadataDir    = "metadata"
	metadataFile   = "package-metadata.json"
	downloadsState = ".state/downloads.json"
)

// Store owns the document root. Every mutation takes the write lock, which is
// what makes concurrent publishes safe — the property the WebDAV registry
// could not offer at all.
type Store struct {
	root string
	mu   sync.RWMutex

	// downloads is counted here because artifacts are served from this root
	// (or a CDN in front of it), never through the control plane. Nothing else
	// is positioned to see a download happen.
	downloads map[string]int64
}

// NewStore opens (and creates) a document root.
func NewStore(root string) (*Store, error) {
	if root == "" {
		return nil, errors.New("a document root is required")
	}
	if err := os.MkdirAll(filepath.Join(root, metadataDir), 0o755); err != nil {
		return nil, fmt.Errorf("preparing the document root: %w", err)
	}

	store := &Store{root: root, downloads: map[string]int64{}}
	store.loadDownloads()
	return store, nil
}

// Root exposes the document root so reads can be served straight from disk.
func (s *Store) Root() string { return s.root }

// ArtifactPath is the canonical location for a package version, matching the
// layout the fpm client already expects.
func ArtifactPath(org, appName, version string) string {
	return fmt.Sprintf("%s/%s/%s/%s-%s.fpm", org, appName, version, appName, version)
}

func packageMetadataRelPath(org, appName string) string {
	return filepath.ToSlash(filepath.Join(metadataDir, org, appName, metadataFile))
}

// writeAtomic replaces a file via a temp file and rename, so a reader never
// observes a half-written document. The static registry's biggest failure mode
// was a truncated index; this makes that impossible rather than unlikely.
func (s *Store) writeAtomic(relPath string, data []byte) error {
	full := filepath.Join(s.root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}

	temp, err := os.CreateTemp(filepath.Dir(full), ".tmp-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName) // no-op once the rename succeeds

	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	// fsync before rename: a rename that lands before the data does would
	// leave an empty file behind a crash.
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tempName, 0o644); err != nil {
		return err
	}
	return os.Rename(tempName, full)
}

func (s *Store) readJSON(relPath string, target any) (bool, error) {
	data, err := os.ReadFile(filepath.Join(s.root, filepath.FromSlash(relPath)))
	if os.IsNotExist(err) {
		return false, nil // absent is a normal state, not an error
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(data, target)
}

// PackageMetadata returns the published metadata for a package.
func (s *Store) PackageMetadata(org, appName string) (*repository.PackageMetadata, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.packageMetadataLocked(org, appName)
}

func (s *Store) packageMetadataLocked(org, appName string) (*repository.PackageMetadata, bool, error) {
	meta := &repository.PackageMetadata{
		Org:      org,
		AppName:  appName,
		Versions: map[string]repository.PackageVersionMetadata{},
	}
	found, err := s.readJSON(packageMetadataRelPath(org, appName), meta)
	if err != nil {
		return nil, false, err
	}
	if meta.Versions == nil {
		meta.Versions = map[string]repository.PackageVersionMetadata{}
	}
	return meta, found, nil
}

// PublishInput is one verified artifact, ready to record.
type PublishInput struct {
	Org      string
	AppName  string
	Version  string
	Artifact []byte
	Checksum string // sha256 of Artifact, computed by the server
	Manifest *metadata.AppMetadata
	Force    bool
}

// Publish records a verified artifact and regenerates the derived documents.
//
// Everything after validation happens under one lock, so a reader sees either
// the state before this publish or the state after it. That is the whole
// reason this service exists.
func (s *Store) Publish(in PublishInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	meta, _, err := s.packageMetadataLocked(in.Org, in.AppName)
	if err != nil {
		return err
	}
	if _, exists := meta.Versions[in.Version]; exists && !in.Force {
		return ErrVersionExists
	}

	artifactRel := ArtifactPath(in.Org, in.AppName, in.Version)
	if err := s.writeAtomic(artifactRel, in.Artifact); err != nil {
		return fmt.Errorf("storing the artifact: %w", err)
	}

	manifest := in.Manifest
	if manifest == nil {
		manifest = &metadata.AppMetadata{}
	}

	meta.Org = in.Org
	meta.AppName = in.AppName
	if manifest.Description != "" {
		meta.Description = manifest.Description
	}
	meta.Versions[in.Version] = repository.PackageVersionMetadata{
		FPMPath: artifactRel,
		// The server's own hash of the bytes it received. The client's claimed
		// checksum is never copied into this field — that indirection is what
		// made the previous integrity chain forgeable.
		ChecksumSHA256:      in.Checksum,
		ReleaseDate:         time.Now().UTC().Format(time.RFC3339Nano),
		Dependencies:        repository.DependenciesFrom(manifest.Dependencies),
		Notes:               manifest.Description,
		FrappeCompatibility: manifest.FrappeCompatibility,
		SourceControlURL:    manifest.SourceControlURL,
		Author:              manifest.Author,
		PackageType:         manifest.PackageType,
		WheelPlatform:       manifest.WheelPlatform,
		WheelPythonVersion:  manifest.WheelPythonVersion,
		CommitSHA:           manifest.CommitSHA,
		GitRef:              manifest.GitRef,
		RequiredApps:        repository.RequiredAppsFrom(manifest.RequiredApps),
	}
	// Recomputed over every version, so the answer is right even for packages
	// whose metadata was written by the old string-comparing client.
	meta.LatestVersion = semver.LatestOf(meta.Versions)

	if err := s.writeMetadataLocked(meta); err != nil {
		return err
	}
	return s.rebuildIndexLocked()
}

// SavePackageMetadata persists metadata while preserving server-owned fields.
//
// `fpm publish` PUTs metadata after uploading an artifact. Rejecting that would
// break the client; trusting it would reopen the hole this service closes,
// since the client supplies fpm_path and checksum_sha256. So the write is
// accepted, and the fields that describe the artifact are taken from what was
// actually stored.
func (s *Store) SavePackageMetadata(org, appName string, incoming *repository.PackageMetadata) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	stored, _, err := s.packageMetadataLocked(org, appName)
	if err != nil {
		return err
	}

	if incoming == nil {
		return nil
	}
	if incoming.Description != "" {
		stored.Description = incoming.Description
	}
	for version, entry := range incoming.Versions {
		existing, known := stored.Versions[version]
		if !known {
			// A version with no artifact behind it is not a version. Accepting
			// one would let a client advertise a download that does not exist.
			continue
		}
		// Only genuinely advisory fields are taken from the client.
		if entry.Notes != "" {
			existing.Notes = entry.Notes
		}
		stored.Versions[version] = existing
	}

	stored.LatestVersion = semver.LatestOf(stored.Versions)
	if err := s.writeMetadataLocked(stored); err != nil {
		return err
	}
	return s.rebuildIndexLocked()
}

func (s *Store) writeMetadataLocked(meta *repository.PackageMetadata) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return s.writeAtomic(packageMetadataRelPath(meta.Org, meta.AppName), data)
}

// rebuildIndexLocked regenerates the catalogue from the metadata on disk.
//
// Derived rather than incrementally edited: the static registry's index was a
// read-modify-write of shared state, so a lost write silently dropped a package
// from the catalogue forever. Regenerating means the index cannot drift from
// the packages that actually exist, and a corrupted one heals on the next
// publish.
func (s *Store) rebuildIndexLocked() error {
	index := repository.RepositoryIndex{Packages: []repository.IndexEntry{}}

	metadataRoot := filepath.Join(s.root, metadataDir)
	err := filepath.Walk(metadataRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || info.Name() != metadataFile {
			return nil
		}

		var meta repository.PackageMetadata
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, &meta); err != nil {
			// One unreadable package must not stop the catalogue being built.
			return nil
		}

		updated := ""
		if entry, ok := meta.Versions[meta.LatestVersion]; ok {
			updated = entry.ReleaseDate
		}
		index.Packages = append(index.Packages, repository.IndexEntry{
			Org:           meta.Org,
			AppName:       meta.AppName,
			Description:   meta.Description,
			LatestVersion: meta.LatestVersion,
			UpdatedAt:     updated,
		})
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	// Stable order so republishing an unchanged package produces an identical
	// index rather than a diff driven by filesystem walk order.
	sort.Slice(index.Packages, func(i, j int) bool {
		if index.Packages[i].Org != index.Packages[j].Org {
			return index.Packages[i].Org < index.Packages[j].Org
		}
		return index.Packages[i].AppName < index.Packages[j].AppName
	})

	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	return s.writeAtomic(indexRelPath, data)
}

// ArtifactBytes reads a stored artifact and counts the download.
func (s *Store) ArtifactBytes(relPath string) ([]byte, error) {
	clean := filepath.Clean("/" + strings.TrimPrefix(relPath, "/"))
	data, err := os.ReadFile(filepath.Join(s.root, filepath.FromSlash(clean)))
	if err != nil {
		return nil, err
	}
	s.countDownload(strings.TrimPrefix(clean, "/"))
	return data, nil
}

func (s *Store) countDownload(relPath string) {
	s.mu.Lock()
	s.downloads[relPath]++
	snapshot := make(map[string]int64, len(s.downloads))
	for k, v := range s.downloads {
		snapshot[k] = v
	}
	s.mu.Unlock()

	// Best effort: a lost count is not worth failing a download for.
	if data, err := json.Marshal(snapshot); err == nil {
		_ = s.writeAtomic(downloadsState, data)
	}
}

// Downloads reports how many times an artifact has been fetched.
func (s *Store) Downloads(relPath string) int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.downloads[strings.TrimPrefix(relPath, "/")]
}

func (s *Store) loadDownloads() {
	var counts map[string]int64
	if ok, err := s.readJSON(downloadsState, &counts); ok && err == nil && counts != nil {
		s.downloads = counts
	}
}
