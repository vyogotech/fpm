package ociregistry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"fpm/internal/config"
	"fpm/internal/metadata"
	"fpm/internal/repository"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"
)

func TestResolveRepoPath(t *testing.T) {
	tests := []struct {
		name     string
		baseURL  string
		org      string
		app      string
		expected string
	}{
		{
			name:     "GHCR standard path",
			baseURL:  "ghcr.io/vyogotech/fpm",
			org:      "frappe",
			app:      "erpnext",
			expected: "ghcr.io/vyogotech/fpm/frappe/erpnext",
		},
		{
			name:     "Localhost bare host adds fpm prefix",
			baseURL:  "localhost:5000",
			org:      "myorg",
			app:      "custom_app",
			expected: "localhost:5000/fpm/myorg/custom_app",
		},
		{
			name:     "HTTP prefix stripped",
			baseURL:  "http://127.0.0.1:5000/repo",
			org:      "testorg",
			app:      "testapp",
			expected: "127.0.0.1:5000/repo/testorg/testapp",
		},
		{
			name:     "HTTPS prefix stripped with trailing slash",
			baseURL:  "https://registry.example.com/v2/packages/",
			org:      "earthians",
			app:      "marley",
			expected: "registry.example.com/v2/packages/earthians/marley",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual, err := ResolveRepoPath(tt.baseURL, tt.org, tt.app)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, actual)
		})
	}
}

func TestAnnotationPromotionRoundtrip(t *testing.T) {
	appMeta := &metadata.AppMetadata{
		Org:                 "frappe",
		AppName:             "hrms",
		PackageVersion:      "15.2.0",
		Title:               "Frappe HRMS",
		Description:         "Human Resource Management System",
		Author:              "Frappe Technologies",
		Publisher:           "Frappe Technologies",
		Email:               "hr@frappe.io",
		License:             "AGPL-3.0",
		Icon:                "octicon octicon-person",
		PackageType:         "standalone",
		CommitSHA:           "abcdef1234567890",
		GitRef:              "refs/tags/v15.2.0",
		WheelPlatform:       "manylinux_2_17_x86_64",
		WheelPythonVersion:  "cp311",
		FrappeCompatibility: []string{"15"},
		RequiredApps: []metadata.RequiredApp{
			{Name: "erpnext", Org: "frappe", Version: "15.2.0"},
		},
		Dependencies: map[string]string{
			"requests": ">=2.28.0",
		},
	}

	dummyChecksum := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	ann := buildAnnotations(appMeta, "hrms-15.2.0.fpm", dummyChecksum)

	assert.Equal(t, "hrms-15.2.0.fpm", ann[AnnotationTitle])
	assert.Equal(t, "15.2.0", ann[AnnotationVersion])
	assert.Equal(t, "frappe", ann[AnnotationOrg])
	assert.Equal(t, "hrms", ann[AnnotationAppName])
	assert.Equal(t, "Frappe HRMS", ann[AnnotationAppTitle])
	assert.Equal(t, "Human Resource Management System", ann[AnnotationDescription])
	assert.Equal(t, "Frappe Technologies", ann[AnnotationAuthor])
	assert.Equal(t, "Frappe Technologies", ann[AnnotationPublisher])
	assert.Equal(t, "hr@frappe.io", ann[AnnotationEmail])
	assert.Equal(t, "AGPL-3.0", ann[AnnotationLicense])
	assert.Equal(t, "octicon octicon-person", ann[AnnotationIcon])
	assert.Equal(t, "standalone", ann[AnnotationPackageType])
	assert.Equal(t, "abcdef1234567890", ann[AnnotationRevision])
	assert.Equal(t, "refs/tags/v15.2.0", ann[AnnotationRefName])
	assert.Equal(t, "manylinux_2_17_x86_64", ann[AnnotationWheelPlatform])
	assert.Equal(t, "cp311", ann[AnnotationWheelPythonVersion])
	assert.Equal(t, dummyChecksum, ann[AnnotationChecksumSHA256])

	// Convert back
	vm := annotationsToPackageVersionMetadata(ann)
	require.NotNil(t, vm)
	assert.Equal(t, dummyChecksum, vm.ChecksumSHA256)
	assert.Equal(t, "abcdef1234567890", vm.CommitSHA)
	assert.Equal(t, "refs/tags/v15.2.0", vm.GitRef)
	assert.Equal(t, "Frappe HRMS", vm.Title)
	assert.Equal(t, "Human Resource Management System", vm.Notes)
	assert.Equal(t, "Frappe Technologies", vm.Author)
	assert.Equal(t, "Frappe Technologies", vm.Publisher)
	assert.Equal(t, "hr@frappe.io", vm.Email)
	assert.Equal(t, "AGPL-3.0", vm.License)
	assert.Equal(t, "octicon octicon-person", vm.Icon)
	assert.Equal(t, "manylinux_2_17_x86_64", vm.WheelPlatform)
	assert.Equal(t, "cp311", vm.WheelPythonVersion)
	assert.Equal(t, []string{"15"}, vm.FrappeCompatibility)
	require.Len(t, vm.RequiredApps, 1)
	assert.Equal(t, "erpnext", vm.RequiredApps[0].AppName)
	assert.Equal(t, "frappe", vm.RequiredApps[0].Org)
	assert.Equal(t, "15.2.0", vm.RequiredApps[0].Version)
}

func TestOrasMemoryStorePackAndInspect(t *testing.T) {
	ctx := context.Background()
	store := memory.New()

	// 1. Create a dummy package layer
	pkgContent := []byte("fpm-package-test-data-contents")
	hasher := sha256.New()
	hasher.Write(pkgContent)
	pkgChecksum := hex.EncodeToString(hasher.Sum(nil))

	layerDesc, err := oras.PushBytes(ctx, store, MediaTypePackage, pkgContent)
	require.NoError(t, err)

	// 2. Config blob
	cfgBytes := []byte(`{"org":"myorg","app":"testapp"}`)
	cfgDesc, err := oras.PushBytes(ctx, store, MediaTypeConfig, cfgBytes)
	require.NoError(t, err)

	// 3. Annotations
	appMeta := &metadata.AppMetadata{
		Org:            "myorg",
		AppName:        "testapp",
		PackageVersion: "1.0.0",
		PackageType:    "standalone",
	}
	ann := buildAnnotations(appMeta, "testapp-1.0.0.fpm", pkgChecksum)

	// 4. Pack Manifest
	packOpts := oras.PackManifestOptions{
		Layers:              []ocispec.Descriptor{layerDesc},
		ConfigDescriptor:    &cfgDesc,
		ManifestAnnotations: ann,
	}

	manifestDesc, err := oras.PackManifest(ctx, store, oras.PackManifestVersion1_1, ArtifactType, packOpts)
	require.NoError(t, err)

	err = store.Tag(ctx, manifestDesc, "1.0.0")
	require.NoError(t, err)

	// 5. Fetch and verify
	rc, err := store.Fetch(ctx, manifestDesc)
	require.NoError(t, err)
	defer rc.Close()

	var manifest ocispec.Manifest
	err = json.NewDecoder(rc).Decode(&manifest)
	require.NoError(t, err)

	assert.Equal(t, ArtifactType, manifest.ArtifactType)
	assert.Equal(t, MediaTypeConfig, manifest.Config.MediaType)
	require.Len(t, manifest.Layers, 1)
	assert.Equal(t, MediaTypePackage, manifest.Layers[0].MediaType)
	assert.Equal(t, "1.0.0", manifest.Annotations[AnnotationVersion])
	assert.Equal(t, "myorg", manifest.Annotations[AnnotationOrg])
	assert.Equal(t, "testapp", manifest.Annotations[AnnotationAppName])
	assert.Equal(t, pkgChecksum, manifest.Annotations[AnnotationChecksumSHA256])
}

func TestGenericAuthResolutionOrder(t *testing.T) {
	// Clean environment
	t.Setenv(EnvRegistryPassword, "")
	t.Setenv(EnvFPMRegistryPassword, "")
	t.Setenv(EnvFPMRegistryToken, "")
	t.Setenv(EnvRegistryUsername, "")
	t.Setenv(EnvFPMRegistryUsername, "")

	// 1. Password from REGISTRY_PASSWORD takes top priority
	t.Setenv(EnvRegistryPassword, "generic-pass")
	t.Setenv(EnvFPMRegistryPassword, "fpm-pass")
	assert.Equal(t, "generic-pass", resolvePassword("testrepo"))

	// 2. FPM_REGISTRY_PASSWORD takes next priority
	t.Setenv(EnvRegistryPassword, "")
	assert.Equal(t, "fpm-pass", resolvePassword("testrepo"))

	// 3. FPM_REGISTRY_TOKEN takes next priority
	t.Setenv(EnvFPMRegistryPassword, "")
	t.Setenv(EnvFPMRegistryToken, "fpm-token")
	assert.Equal(t, "fpm-token", resolvePassword("testrepo"))

	// 4. Repo specific fallback
	t.Setenv(EnvFPMRegistryToken, "")
	t.Setenv(repository.PasswordEnvVar("myrepo"), "repo-specific-pass")
	assert.Equal(t, "repo-specific-pass", resolvePassword("myrepo"))

	// Username resolution
	t.Setenv(EnvRegistryUsername, "generic-user")
	assert.Equal(t, "generic-user", resolveUsername(config.RepositoryConfig{}))
	assert.Equal(t, "config-user", resolveUsername(config.RepositoryConfig{Username: "config-user"}))
}

func TestDriverRegistration(t *testing.T) {
	driver := repository.GetOCIDriver()
	require.NotNil(t, driver, "OCIDriver must be registered by internal/ociregistry package init()")
}

// TestReferrersSubjectLinking demonstrates oras' subject mechanics with both manifests
// in ONE store — that is, one repository. It passed while publishing was broken in
// production, because fpm was setting a subject that lived in a *different* repository;
// see TestSubjectFromAnotherRepositoryCannotBePushed for what that does.
func TestReferrersSubjectLinking(t *testing.T) {
	ctx := context.Background()
	store := memory.New()

	// Base app manifest (e.g. erpnext)
	baseContent := []byte("erpnext-content")
	baseLayer, err := oras.PushBytes(ctx, store, MediaTypePackage, baseContent)
	require.NoError(t, err)

	baseManifest, err := oras.PackManifest(ctx, store, oras.PackManifestVersion1_1, ArtifactType, oras.PackManifestOptions{
		Layers: []ocispec.Descriptor{baseLayer},
	})
	require.NoError(t, err)
	err = store.Tag(ctx, baseManifest, "15.0.0")
	require.NoError(t, err)

	// Referrer app manifest (e.g. hrms subject -> erpnext)
	hrmsContent := []byte("hrms-content")
	hrmsLayer, err := oras.PushBytes(ctx, store, MediaTypePackage, hrmsContent)
	require.NoError(t, err)

	hrmsManifest, err := oras.PackManifest(ctx, store, oras.PackManifestVersion1_1, ArtifactType, oras.PackManifestOptions{
		Layers:  []ocispec.Descriptor{hrmsLayer},
		Subject: &baseManifest,
	})
	require.NoError(t, err)
	err = store.Tag(ctx, hrmsManifest, "15.0.0")
	require.NoError(t, err)

	// Verify Subject is present
	rc, err := store.Fetch(ctx, hrmsManifest)
	require.NoError(t, err)
	defer rc.Close()

	var manifest ocispec.Manifest
	err = json.NewDecoder(rc).Decode(&manifest)
	require.NoError(t, err)
	require.NotNil(t, manifest.Subject)
	assert.Equal(t, baseManifest.Digest, manifest.Subject.Digest)
}

// TestSubjectFromAnotherRepositoryCannotBePushed reproduces the failure that stopped
// hrms and lms publishing at all:
//
//	failed to perform "FindSuccessors" on source: sha256:...:
//	application/vnd.oci.image.manifest.v1+json: not found
//
// The OCI referrers graph is per-repository: `subject` must name a manifest in the same
// repository. fpm gives every app its own — frappe/hrms and frappe/erpnext are separate
// repositories — so a subject resolved from the dependency's repository is not in the
// source being copied, and the push cannot walk it. Satisfying it would mean duplicating
// the dependency's manifest into this app's repository, which is not what a reference is.
func TestSubjectFromAnotherRepositoryCannotBePushed(t *testing.T) {
	ctx := context.Background()

	// The dependency lives elsewhere: a descriptor for a manifest this store never has.
	elsewhere := memory.New()
	depLayer, err := oras.PushBytes(ctx, elsewhere, MediaTypePackage, []byte("erpnext-content"))
	require.NoError(t, err)
	depManifest, err := oras.PackManifest(ctx, elsewhere, oras.PackManifestVersion1_1, ArtifactType,
		oras.PackManifestOptions{Layers: []ocispec.Descriptor{depLayer}})
	require.NoError(t, err)

	// The app being published, in its own repository, pointing at that descriptor.
	source := memory.New()
	appLayer, err := oras.PushBytes(ctx, source, MediaTypePackage, []byte("hrms-content"))
	require.NoError(t, err)
	appManifest, err := oras.PackManifest(ctx, source, oras.PackManifestVersion1_1, ArtifactType,
		oras.PackManifestOptions{Layers: []ocispec.Descriptor{appLayer}, Subject: &depManifest})
	require.NoError(t, err)
	require.NoError(t, source.Tag(ctx, appManifest, "15.63.3"))

	_, err = oras.Copy(ctx, source, "15.63.3", memory.New(), "15.63.3", oras.DefaultCopyOptions)
	require.Error(t, err, "a cross-repository subject must not be pushable; that is why fpm sets none")
	assert.Contains(t, err.Error(), "not found")
}
