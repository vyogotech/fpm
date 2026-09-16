package ociregistry

// Bundle support: publish a dependency-closure bundle as ONE OCI artifact.
//
// A bundle is the "fat" package — a root app plus every Frappe app it
// transitively requires — and until now it existed only as a local directory
// produced by `fpm bundle` / `fpm package --with-deps`. This packs that
// directory into a single manifest so it can be pushed to, and installed from,
// a registry like any other artifact.
//
// Shape, deliberately the same as a single-package artifact so registries and
// tooling need no special handling:
//   config  the fpm-bundle.json bytes, which already carry install order
//   layers  one .fpm blob per SHIPPED entry, titled with its filename
//   subject none, for the same per-repository reason documented on Push
//
// An entry the target bench provides (provided_by: "bench" — how a base-image
// app such as erpnext is recorded) carries no file and therefore no layer. It
// stays in the config so the installer can still check the bench has it.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"fpm/internal/bundle"
	"fpm/internal/config"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/registry/remote"
)

const (
	// MediaTypeBundleConfig marks the config blob as a bundle manifest.
	MediaTypeBundleConfig = "application/vnd.vyogo.fpm.bundle.config.v1+json"
	// BundleArtifactType distinguishes a bundle from a single package at the
	// manifest level, so a client can tell them apart without fetching a blob.
	BundleArtifactType = "application/vnd.vyogo.fpm.bundle.v1"
)

const (
	// AnnotationBundle is "true" on a bundle manifest. Belt and braces with
	// artifactType: some registries and older clients drop artifactType on
	// conversion, and annotations survive.
	AnnotationBundle = "vnd.vyogo.fpm.bundle"
	// AnnotationBundleJSON carries the whole fpm-bundle.json, so `fpm deps` and
	// a registry browser can read the closure without pulling any layer.
	AnnotationBundleJSON = "vnd.vyogo.fpm.bundle.v1+json"
	// AnnotationBundleCount is the number of packages the bundle carries.
	AnnotationBundleCount = "vnd.vyogo.fpm.bundle.package_count"
)

// ErrNotBundle is returned by PullBundle when the reference resolves to a
// single package. Callers probing "is this a bundle?" match on it rather than
// on message text, and fall back to the single-package path.
var ErrNotBundle = errors.New("not a bundle")

// IsBundle reports whether a fetched manifest is a bundle rather than a single
// package. artifactType is authoritative; the annotation is the fallback.
func IsBundle(m *ocispec.Manifest) bool {
	if m == nil {
		return false
	}
	if m.ArtifactType == BundleArtifactType {
		return true
	}
	if m.Config.MediaType == MediaTypeBundleConfig {
		return true
	}
	return m.Annotations[AnnotationBundle] == "true"
}

// PushBundle publishes a bundle directory as one OCI artifact under org/appName,
// tagged version. org/appName need not be the root package's own coordinate: a
// stack can be published under a name of its own.
func PushBundle(ctx context.Context, repoConfig config.RepositoryConfig, bundleDir string, m *bundle.Manifest, org, appName, version string, extraTags []string) (ocispec.Descriptor, error) {
	if m == nil {
		return ocispec.Descriptor{}, fmt.Errorf("bundle manifest is required for push")
	}
	if org == "" || appName == "" || version == "" {
		return ocispec.Descriptor{}, fmt.Errorf("org, app name and version are required for a bundle push")
	}

	repo, err := NewRepository(repoConfig, org, appName)
	if err != nil {
		return ocispec.Descriptor{}, err
	}

	memStore := memory.New()

	// One layer per shipped package, in install order so the manifest reads the
	// way the bundle installs. The installer does not rely on layer order — it
	// reads install_order from the config — but a human inspecting the manifest
	// should not have to cross-reference.
	var layers []ocispec.Descriptor
	for _, e := range m.Shipped() {
		path := filepath.Join(bundleDir, e.File)
		data, err := os.ReadFile(path)
		if err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("bundle entry %s: %w", e.Identifier(), err)
		}
		desc, err := oras.PushBytes(ctx, memStore, MediaTypePackage, data)
		if err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("failed to stage layer for %s: %w", e.Identifier(), err)
		}
		sum := sha256.Sum256(data)
		desc.Annotations = map[string]string{
			AnnotationTitle:          e.File,
			AnnotationOrg:            e.Org,
			AnnotationAppName:        e.App,
			AnnotationChecksumSHA256: hex.EncodeToString(sum[:]),
		}
		layers = append(layers, desc)
	}
	if len(layers) == 0 {
		return ocispec.Descriptor{}, fmt.Errorf("bundle carries no packages: every entry is provided by the bench")
	}

	// The config IS the bundle manifest: one source of truth for install order,
	// rather than a re-encoding that could drift from the file on disk.
	configPayload, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to encode bundle manifest: %w", err)
	}
	configDesc, err := oras.PushBytes(ctx, memStore, MediaTypeBundleConfig, configPayload)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to stage bundle config: %w", err)
	}

	annotations := map[string]string{
		AnnotationBundle:      "true",
		AnnotationBundleJSON:  string(configPayload),
		AnnotationBundleCount: strconv.Itoa(len(layers)),
		AnnotationOrg:         org,
		AnnotationAppName:     appName,
		AnnotationVersion:     version,
		AnnotationCreated:     time.Now().UTC().Format(time.RFC3339),
	}
	if m.Root.App != "" {
		annotations[AnnotationRequiredApps] = requiredAppsOfBundle(m)
	}

	manifestDesc, err := oras.PackManifest(ctx, memStore, oras.PackManifestVersion1_1, BundleArtifactType, oras.PackManifestOptions{
		Layers:              layers,
		ConfigDescriptor:    &configDesc,
		ManifestAnnotations: annotations,
	})
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to pack bundle manifest: %w", err)
	}

	if err := memStore.Tag(ctx, manifestDesc, version); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to tag bundle locally with %s: %w", version, err)
	}
	for _, tag := range extraTags {
		if tag != "" && tag != version {
			if err := memStore.Tag(ctx, manifestDesc, tag); err != nil {
				return ocispec.Descriptor{}, fmt.Errorf("failed to tag bundle locally with %s: %w", tag, err)
			}
		}
	}

	if _, err := oras.Copy(ctx, memStore, version, repo, version, oras.DefaultCopyOptions); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to push bundle to %s: %w", repo.Reference.String(), err)
	}
	for _, tag := range extraTags {
		if tag != "" && tag != version {
			if _, err := oras.Copy(ctx, memStore, tag, repo, tag, oras.DefaultCopyOptions); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to push extra tag %s: %v\n", tag, err)
			}
		}
	}
	return manifestDesc, nil
}

// requiredAppsOfBundle renders the closure for the manifest annotation, so the
// dependency set is queryable without fetching the config blob.
func requiredAppsOfBundle(m *bundle.Manifest) string {
	type ra struct {
		Org        string `json:"org,omitempty"`
		Name       string `json:"name"`
		Version    string `json:"version,omitempty"`
		ProvidedBy string `json:"provided_by,omitempty"`
	}
	out := make([]ra, 0, len(m.InstallOrder))
	for _, e := range m.InstallOrder {
		if e.App == m.Root.App && e.Org == m.Root.Org {
			continue
		}
		out = append(out, ra{Org: e.Org, Name: e.App, Version: e.Version, ProvidedBy: e.ProvidedBy})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(b)
}

// PullBundle downloads a bundle artifact into targetDir, writing each package
// under the filename its manifest entry names plus fpm-bundle.json, so the
// result is byte-for-byte a bundle directory `fpm install <dir>` accepts.
func PullBundle(ctx context.Context, repoConfig config.RepositoryConfig, org, appName, versionOrDigest, targetDir string) (*bundle.Manifest, error) {
	repo, err := NewRepository(repoConfig, org, appName)
	if err != nil {
		return nil, err
	}

	_, rc, err := repo.FetchReference(ctx, versionOrDigest)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch OCI manifest for %s/%s:%s from %s: %w", org, appName, versionOrDigest, repoConfig.Name, err)
	}
	defer rc.Close()

	manifestBytes, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest content: %w", err)
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return nil, fmt.Errorf("failed to unmarshal OCI manifest: %w", err)
	}
	if !IsBundle(&manifest) {
		return nil, fmt.Errorf("%s/%s:%s is a single package: %w", org, appName, versionOrDigest, ErrNotBundle)
	}

	// Prefer the config blob over the annotation: annotations have a size limit
	// that a large closure can exceed, and some registries truncate them.
	var m *bundle.Manifest
	cfgRC, cfgErr := repo.Blobs().Fetch(ctx, manifest.Config)
	if cfgErr == nil {
		defer cfgRC.Close()
		cfgBytes, readErr := io.ReadAll(cfgRC)
		if readErr == nil {
			m, err = bundle.Parse(cfgBytes)
			if err != nil {
				return nil, err
			}
		}
	}
	if m == nil {
		raw := manifest.Annotations[AnnotationBundleJSON]
		if raw == "" {
			return nil, fmt.Errorf("bundle %s/%s:%s has neither a readable config blob nor a bundle annotation", org, appName, versionOrDigest)
		}
		if m, err = bundle.Parse([]byte(raw)); err != nil {
			return nil, err
		}
	}

	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create bundle directory %s: %w", targetDir, err)
	}

	// Index layers by the filename they were titled with, so an entry is matched
	// by name rather than by position.
	byName := map[string]ocispec.Descriptor{}
	for _, l := range manifest.Layers {
		if t := l.Annotations[AnnotationTitle]; t != "" {
			byName[t] = l
		}
	}

	for _, e := range m.Shipped() {
		desc, ok := byName[e.File]
		if !ok {
			return nil, fmt.Errorf("bundle is missing the layer for %s (%s)", e.Identifier(), e.File)
		}
		if err := fetchLayerTo(ctx, repo, desc, filepath.Join(targetDir, e.File)); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Identifier(), err)
		}
	}

	if err := bundle.Write(targetDir, m); err != nil {
		return nil, fmt.Errorf("failed to write %s: %w", bundle.ManifestName, err)
	}
	return m, nil
}

// fetchLayerTo downloads one blob and verifies it against its descriptor digest
// before the file is considered written.
func fetchLayerTo(ctx context.Context, repo *remote.Repository, desc ocispec.Descriptor, dest string) error {
	blobRC, err := repo.Blobs().Fetch(ctx, desc)
	if err != nil {
		return fmt.Errorf("failed to fetch layer %s: %w", desc.Digest.String(), err)
	}
	defer blobRC.Close()

	out, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("failed to create %s: %w", dest, err)
	}
	defer out.Close()

	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, hasher), blobRC); err != nil {
		_ = os.Remove(dest)
		return fmt.Errorf("failed to write %s: %w", dest, err)
	}
	if got := "sha256:" + hex.EncodeToString(hasher.Sum(nil)); got != string(desc.Digest) {
		_ = os.Remove(dest)
		return fmt.Errorf("content integrity check failed: expected %s, got %s", desc.Digest, got)
	}
	return nil
}
