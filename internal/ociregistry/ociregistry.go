package ociregistry

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
	"strings"
	"time"

	"fpm/internal/config"
	"fpm/internal/metadata"
	"fpm/internal/repository"
	"fpm/internal/semver"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/errdef"
)

// OCI Media Types for FPM packages.
const (
	MediaTypeConfig   = "application/vnd.vyogo.fpm.config.v1+json"
	MediaTypePackage  = "application/vnd.vyogo.fpm.package.v1.fpm"
	ArtifactType      = "application/vnd.vyogo.fpm.package.v1"
	MediaTypeManifest = ocispec.MediaTypeImageManifest
)

// Promoted Annotation Keys for OCI Manifests.
const (
	AnnotationTitle               = ocispec.AnnotationTitle
	AnnotationVersion             = ocispec.AnnotationVersion
	AnnotationCreated             = ocispec.AnnotationCreated
	AnnotationRevision            = ocispec.AnnotationRevision
	AnnotationRefName             = ocispec.AnnotationRefName
	AnnotationOrg                 = "vnd.vyogo.fpm.org"
	AnnotationAppName             = "vnd.vyogo.fpm.app_name"
	AnnotationPackageType         = "vnd.vyogo.fpm.package_type"
	AnnotationWheelPlatform       = "vnd.vyogo.fpm.wheel_platform"
	AnnotationWheelPythonVersion  = "vnd.vyogo.fpm.wheel_python_version"
	AnnotationFrappeCompatibility = "vnd.vyogo.fpm.frappe_compatibility"
	AnnotationRequiredApps        = "vnd.vyogo.fpm.required_apps"
	AnnotationDependencies        = "vnd.vyogo.fpm.dependencies"
	AnnotationChecksumSHA256      = "vnd.vyogo.fpm.checksum_sha256"
	AnnotationMetadataJSON        = "vnd.vyogo.fpm.metadata.v1+json"
)

// Push uploads an FPM package file and its metadata to the specified OCI repository.
func Push(ctx context.Context, repoConfig config.RepositoryConfig, fpmFilePath string, appMeta *metadata.AppMetadata, extraTags []string) (ocispec.Descriptor, error) {
	if appMeta == nil {
		return ocispec.Descriptor{}, fmt.Errorf("app metadata is required for push")
	}

	repo, err := NewRepository(repoConfig, appMeta.Org, appMeta.AppName)
	if err != nil {
		return ocispec.Descriptor{}, err
	}

	// 1. Read package file
	fpmBytes, err := os.ReadFile(fpmFilePath)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to read package file %s: %w", fpmFilePath, err)
	}

	hasher := sha256.New()
	hasher.Write(fpmBytes)
	checksumHex := hex.EncodeToString(hasher.Sum(nil))

	// 2. Prepare in-memory store for packing
	memStore := memory.New()

	// Push layer blob
	layerDesc, err := oras.PushBytes(ctx, memStore, MediaTypePackage, fpmBytes)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to stage package layer: %w", err)
	}
	layerDesc.Annotations = map[string]string{
		AnnotationTitle: filepath.Base(fpmFilePath),
	}

	// 3. Prepare Config blob
	configPayload, _ := json.Marshal(map[string]any{
		"created":     time.Now().UTC().Format(time.RFC3339),
		"org":         appMeta.Org,
		"appName":     appMeta.AppName,
		"version":     appMeta.PackageVersion,
		"packageType": appMeta.PackageType,
	})
	configDesc, err := oras.PushBytes(ctx, memStore, MediaTypeConfig, configPayload)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to stage config blob: %w", err)
	}

	// 4. Build manifest annotations
	annotations := buildAnnotations(appMeta, filepath.Base(fpmFilePath), checksumHex)

	// 5. Pack manifest (with OCI 1.1 subject descriptor if required app exists in registry)
	packOpts := oras.PackManifestOptions{
		Layers:              []ocispec.Descriptor{layerDesc},
		ConfigDescriptor:    &configDesc,
		ManifestAnnotations: annotations,
	}

	// No subject descriptor is set for required_apps, though it is tempting: the OCI
	// referrers graph is per-repository. `subject` must name a manifest in the *same*
	// repository, and fpm gives every app its own — hrms lives at
	// <registry>/<repo>/frappe/hrms and erpnext at <registry>/<repo>/frappe/erpnext.
	//
	// Pointing across repositories produced a manifest whose subject the push could not
	// resolve, and every affected app failed to publish at all:
	//
	//   failed to perform "FindSuccessors" on source: sha256:...:
	//   application/vnd.oci.image.manifest.v1+json: not found
	//
	// oras walks a manifest's successors from the source it is copying out of, and the
	// dependency's manifest is not there — it lives in the other repository. Satisfying
	// it would mean duplicating the dependency's manifest into this app's repository,
	// which is not what a reference means.
	//
	// The dependency information is not lost: required_apps are recorded in the
	// AnnotationRequiredApps manifest annotation, which is queryable without pulling
	// the payload, and `fpm deps` reads them from there.

	manifestDesc, err := oras.PackManifest(ctx, memStore, oras.PackManifestVersion1_1, ArtifactType, packOpts)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to pack OCI manifest: %w", err)
	}

	// Tag manifest in local store with primary tag (version) and extra tags
	primaryTag := appMeta.PackageVersion
	if err := memStore.Tag(ctx, manifestDesc, primaryTag); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to tag manifest locally with %s: %w", primaryTag, err)
	}

	for _, tag := range extraTags {
		if tag != "" && tag != primaryTag {
			if err := memStore.Tag(ctx, manifestDesc, tag); err != nil {
				return ocispec.Descriptor{}, fmt.Errorf("failed to tag manifest locally with %s: %w", tag, err)
			}
		}
	}

	// 6. Copy / Push from memory store to remote OCI repository
	copyOpts := oras.DefaultCopyOptions
	_, err = oras.Copy(ctx, memStore, primaryTag, repo, primaryTag, copyOpts)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to push OCI artifact to %s: %w", repo.Reference.String(), err)
	}

	// Push extra tags if any
	for _, tag := range extraTags {
		if tag != "" && tag != primaryTag {
			if _, err := oras.Copy(ctx, memStore, tag, repo, tag, copyOpts); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to push extra tag %s: %v\n", tag, err)
			}
		}
	}

	return manifestDesc, nil
}

// DiscoverReferrers finds all artifacts referencing this app version using the OCI Referrers API.
func DiscoverReferrers(ctx context.Context, repoConfig config.RepositoryConfig, org, appName, version string) ([]ocispec.Descriptor, error) {
	repo, err := NewRepository(repoConfig, org, appName)
	if err != nil {
		return nil, err
	}

	desc, err := repo.Resolve(ctx, version)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve version %s: %w", version, err)
	}

	var results []ocispec.Descriptor
	err = repo.Referrers(ctx, desc, ArtifactType, func(referrers []ocispec.Descriptor) error {
		results = append(results, referrers...)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to discover referrers for %s/%s:%s: %w", org, appName, version, err)
	}
	return results, nil
}

type driver struct{}

func (d *driver) Pull(ctx context.Context, repo config.RepositoryConfig, org, appName, versionOrDigest, targetPath string) (*repository.PackageVersionMetadata, error) {
	return Pull(ctx, repo, org, appName, versionOrDigest, targetPath)
}

func (d *driver) FetchMetadata(ctx context.Context, repo config.RepositoryConfig, org, appName string) (*repository.PackageMetadata, bool, error) {
	return FetchMetadata(ctx, repo, org, appName)
}

func (d *driver) FetchVersionMetadata(ctx context.Context, repo config.RepositoryConfig, org, appName, version string) (*repository.PackageVersionMetadata, bool, error) {
	return FetchVersionMetadata(ctx, repo, org, appName, version)
}

func (d *driver) Exists(ctx context.Context, repo config.RepositoryConfig, org, appName, version string) (bool, *repository.PackageVersionMetadata, error) {
	return Exists(ctx, repo, org, appName, version)
}

func init() {
	repository.RegisterOCIDriver(&driver{})
}

// Pull downloads an FPM package from an OCI repository to targetPath.
func Pull(ctx context.Context, repoConfig config.RepositoryConfig, org, appName, versionOrDigest, targetPath string) (*repository.PackageVersionMetadata, error) {
	repo, err := NewRepository(repoConfig, org, appName)
	if err != nil {
		return nil, err
	}

	// 1. Fetch manifest
	_, rc, err := repo.FetchReference(ctx, versionOrDigest)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch OCI manifest for %s/%s:%s from %s: %w",
			org, appName, versionOrDigest, repoConfig.Name, err)
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

	// 2. Extract metadata from manifest annotations
	versionMeta := annotationsToPackageVersionMetadata(manifest.Annotations)

	// 3. Find the package layer
	var packageLayer *ocispec.Descriptor
	for _, layer := range manifest.Layers {
		if layer.MediaType == MediaTypePackage || strings.HasSuffix(layer.MediaType, ".fpm") || len(manifest.Layers) == 1 {
			l := layer
			packageLayer = &l
			break
		}
	}
	if packageLayer == nil {
		return nil, fmt.Errorf("no package layer found in OCI manifest for %s/%s:%s", org, appName, versionOrDigest)
	}

	// 4. Download layer blob
	blobRC, err := repo.Blobs().Fetch(ctx, *packageLayer)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch package layer blob %s: %w", packageLayer.Digest.String(), err)
	}
	defer blobRC.Close()

	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return nil, fmt.Errorf("failed to create destination directory: %w", err)
	}

	out, err := os.Create(targetPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create local file %s: %w", targetPath, err)
	}
	defer out.Close()

	hasher := sha256.New()
	multiWriter := io.MultiWriter(out, hasher)

	if _, err := io.Copy(multiWriter, blobRC); err != nil {
		_ = os.Remove(targetPath)
		return nil, fmt.Errorf("failed to write package layer: %w", err)
	}

	downloadedDigest := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	if downloadedDigest != string(packageLayer.Digest) {
		_ = os.Remove(targetPath)
		return nil, fmt.Errorf("content integrity check failed: expected digest %s, got %s", packageLayer.Digest, downloadedDigest)
	}

	if versionMeta.ChecksumSHA256 == "" {
		versionMeta.ChecksumSHA256 = strings.TrimPrefix(downloadedDigest, "sha256:")
	}

	return versionMeta, nil
}

// FetchMetadata retrieves metadata for all versions of an app by querying OCI repository tags and manifest annotations.
func FetchMetadata(ctx context.Context, repoConfig config.RepositoryConfig, org, appName string) (*repository.PackageMetadata, bool, error) {
	repo, err := NewRepository(repoConfig, org, appName)
	if err != nil {
		return nil, false, err
	}

	var tags []string
	err = repo.Tags(ctx, "", func(t []string) error {
		tags = append(tags, t...)
		return nil
	})
	if err != nil {
		if errors.Is(err, errdef.ErrNotFound) || isNotFound(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("failed to list OCI tags for %s/%s: %w", org, appName, err)
	}

	if len(tags) == 0 {
		return nil, false, nil
	}

	pkgMeta := &repository.PackageMetadata{
		Org:      org,
		AppName:  appName,
		Versions: make(map[string]repository.PackageVersionMetadata),
	}

	for _, tag := range tags {
		// Skip digest tags or commit SHA tags if they don't match semver
		desc, rc, err := repo.FetchReference(ctx, tag)
		if err != nil {
			continue
		}
		manifestBytes, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			continue
		}

		var manifest ocispec.Manifest
		if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
			continue
		}

		vMeta := annotationsToPackageVersionMetadata(manifest.Annotations)
		if vMeta.ChecksumSHA256 == "" && len(manifest.Layers) > 0 {
			vMeta.ChecksumSHA256 = strings.TrimPrefix(string(manifest.Layers[0].Digest), "sha256:")
		}

		ver := tag
		if v := manifest.Annotations[AnnotationVersion]; v != "" {
			ver = v
		}

		pkgMeta.Versions[ver] = *vMeta
		_ = desc
	}

	if len(pkgMeta.Versions) == 0 {
		return nil, false, nil
	}

	pkgMeta.LatestVersion = semver.LatestOf(pkgMeta.Versions)
	return pkgMeta, true, nil
}

// FetchVersionMetadata retrieves metadata for a single specific version of an app.
func FetchVersionMetadata(ctx context.Context, repoConfig config.RepositoryConfig, org, appName, version string) (*repository.PackageVersionMetadata, bool, error) {
	repo, err := NewRepository(repoConfig, org, appName)
	if err != nil {
		return nil, false, err
	}

	desc, rc, err := repo.FetchReference(ctx, version)
	if err != nil {
		if errors.Is(err, errdef.ErrNotFound) || isNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer rc.Close()
	_ = desc

	manifestBytes, err := io.ReadAll(rc)
	if err != nil {
		return nil, false, err
	}

	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return nil, false, err
	}

	vMeta := annotationsToPackageVersionMetadata(manifest.Annotations)
	if vMeta.ChecksumSHA256 == "" && len(manifest.Layers) > 0 {
		vMeta.ChecksumSHA256 = strings.TrimPrefix(string(manifest.Layers[0].Digest), "sha256:")
	}

	return vMeta, true, nil
}

// Exists checks whether a package version exists in an OCI repository via manifest HEAD.
func Exists(ctx context.Context, repoConfig config.RepositoryConfig, org, appName, version string) (bool, *repository.PackageVersionMetadata, error) {
	repo, err := NewRepository(repoConfig, org, appName)
	if err != nil {
		return false, nil, err
	}

	desc, err := repo.Resolve(ctx, version)
	if err != nil {
		if errors.Is(err, errdef.ErrNotFound) || isNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}

	// Fetch manifest to get annotations
	vMeta, _, _ := FetchVersionMetadata(ctx, repoConfig, org, appName, version)
	_ = desc
	return true, vMeta, nil
}

func buildAnnotations(appMeta *metadata.AppMetadata, filename, checksumHex string) map[string]string {
	ann := map[string]string{
		AnnotationTitle:          filename,
		AnnotationVersion:        appMeta.PackageVersion,
		AnnotationCreated:        time.Now().UTC().Format(time.RFC3339),
		AnnotationOrg:            appMeta.Org,
		AnnotationAppName:        appMeta.AppName,
		AnnotationPackageType:    appMeta.PackageType,
		AnnotationChecksumSHA256: checksumHex,
	}

	if appMeta.CommitSHA != "" {
		ann[AnnotationRevision] = appMeta.CommitSHA
	}
	if appMeta.GitRef != "" {
		ann[AnnotationRefName] = appMeta.GitRef
	}
	if appMeta.WheelPlatform != "" {
		ann[AnnotationWheelPlatform] = appMeta.WheelPlatform
	}
	if appMeta.WheelPythonVersion != "" {
		ann[AnnotationWheelPythonVersion] = appMeta.WheelPythonVersion
	}

	if len(appMeta.FrappeCompatibility) > 0 {
		if b, err := json.Marshal(appMeta.FrappeCompatibility); err == nil {
			ann[AnnotationFrappeCompatibility] = string(b)
		}
	}
	if len(appMeta.RequiredApps) > 0 {
		if b, err := json.Marshal(appMeta.RequiredApps); err == nil {
			ann[AnnotationRequiredApps] = string(b)
		}
	}
	if len(appMeta.Dependencies) > 0 {
		if b, err := json.Marshal(appMeta.Dependencies); err == nil {
			ann[AnnotationDependencies] = string(b)
		}
	}

	// Full metadata backup
	if metaBytes, err := json.Marshal(appMeta); err == nil {
		ann[AnnotationMetadataJSON] = string(metaBytes)
	}

	return ann
}

func annotationsToPackageVersionMetadata(ann map[string]string) *repository.PackageVersionMetadata {
	vm := &repository.PackageVersionMetadata{
		ChecksumSHA256:     ann[AnnotationChecksumSHA256],
		ReleaseDate:        ann[AnnotationCreated],
		CommitSHA:          ann[AnnotationRevision],
		GitRef:             ann[AnnotationRefName],
		PackageType:        ann[AnnotationPackageType],
		WheelPlatform:      ann[AnnotationWheelPlatform],
		WheelPythonVersion: ann[AnnotationWheelPythonVersion],
	}

	if val := ann[AnnotationFrappeCompatibility]; val != "" {
		var compat []string
		if err := json.Unmarshal([]byte(val), &compat); err == nil {
			vm.FrappeCompatibility = compat
		}
	}
	if val := ann[AnnotationRequiredApps]; val != "" {
		var reqs []metadata.RequiredApp
		if err := json.Unmarshal([]byte(val), &reqs); err == nil {
			vm.RequiredApps = repository.RequiredAppsFrom(reqs)
		}
	}
	if val := ann[AnnotationDependencies]; val != "" {
		var deps map[string]string
		if err := json.Unmarshal([]byte(val), &deps); err == nil {
			vm.Dependencies = repository.DependenciesFrom(deps)
		}
	}

	return vm
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "not found") || strings.Contains(s, "404") || strings.Contains(s, "manifest unknown") {
		return true
	}
	// A registry that will not confirm a repository exists reads the same as one that
	// says it does not. GHCR answers a pull scope for a repository it does not have
	// with "denied: requested access to the resource is denied" rather than 404, so
	// treating that as an error makes the first publish of any new app fail on the
	// check that was meant to decide whether to publish it.
	//
	// A genuine credential problem is not masked: it surfaces on the push, which needs
	// write access and reports it plainly.
	return strings.Contains(s, "denied") || strings.Contains(s, "403")
}
