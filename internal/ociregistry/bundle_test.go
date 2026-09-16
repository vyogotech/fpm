package ociregistry

import (
	"testing"

	"fpm/internal/bundle"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// A client must be able to tell a bundle from a single package by the manifest
// alone — before fetching any blob — or `fpm install <org>/<app>` cannot choose
// a path without downloading the wrong thing first.
func TestIsBundleByArtifactType(t *testing.T) {
	m := &ocispec.Manifest{ArtifactType: BundleArtifactType}
	if !IsBundle(m) {
		t.Fatal("artifactType should identify a bundle")
	}
}

func TestIsBundleByConfigMediaType(t *testing.T) {
	m := &ocispec.Manifest{Config: ocispec.Descriptor{MediaType: MediaTypeBundleConfig}}
	if !IsBundle(m) {
		t.Fatal("bundle config media type should identify a bundle")
	}
}

// Some registries drop artifactType when converting between manifest versions,
// so the annotation is carried as a fallback and must be honoured.
func TestIsBundleByAnnotationFallback(t *testing.T) {
	m := &ocispec.Manifest{Annotations: map[string]string{AnnotationBundle: "true"}}
	if !IsBundle(m) {
		t.Fatal("annotation fallback should identify a bundle")
	}
}

func TestIsBundleRejectsSinglePackage(t *testing.T) {
	m := &ocispec.Manifest{
		ArtifactType: ArtifactType,
		Config:       ocispec.Descriptor{MediaType: MediaTypeConfig},
		Annotations:  map[string]string{AnnotationOrg: "vyogotech"},
	}
	if IsBundle(m) {
		t.Fatal("a single-package manifest must not be taken for a bundle")
	}
	if IsBundle(nil) {
		t.Fatal("nil must not be taken for a bundle")
	}
}

// The required_apps annotation lets `fpm deps` read a bundle's closure without
// pulling the config blob. The root is excluded: it requires itself of nobody.
func TestRequiredAppsOfBundleExcludesRootAndKeepsBenchProvided(t *testing.T) {
	m := &bundle.Manifest{
		Root: bundle.Entry{Org: "vyogotech", App: "saas_platform", Version: "1.0.0"},
		InstallOrder: []bundle.Entry{
			{Org: "frappe", App: "erpnext", Version: "16.0.0", ProvidedBy: "bench"},
			{Org: "vyogotech", App: "frappe_cloud_manager", Version: "1.0.0", File: "fcm.fpm"},
			{Org: "vyogotech", App: "saas_platform", Version: "1.0.0", File: "sp.fpm"},
		},
	}
	got := requiredAppsOfBundle(m)
	for _, want := range []string{"frappe_cloud_manager", "erpnext", `"provided_by":"bench"`} {
		if !contains(got, want) {
			t.Fatalf("expected %q in %s", want, got)
		}
	}
	if contains(got, "saas_platform") {
		t.Fatalf("the root must not appear in its own required_apps: %s", got)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
