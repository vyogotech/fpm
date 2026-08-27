package repository

import (
	"strings"
	"testing"

	"fpm/internal/config"
)

// TestFindPackageInRepoConfigDispatchesOnBackend: `fpm get-app <repo>/<org>/<app>` aimed
// at an OCI registry went down the HTTP path and failed with
// `unsupported protocol scheme ""`, because a registry is not addressed by URL and the
// metadata path it built had no scheme. Searching every configured repository already
// dispatched correctly; only the single-repository path did not.
func TestFindPackageInRepoConfigDispatchesOnBackend(t *testing.T) {
	// No OCI driver registered here, so an OCI repository must report that rather than
	// silently building an HTTP URL out of a registry reference.
	saved := GetOCIDriver()
	RegisterOCIDriver(nil)
	t.Cleanup(func() { RegisterOCIDriver(saved) })

	_, err := FindPackageInRepoConfig(
		config.RepositoryConfig{Name: "ghcr", URL: "ghcr.io/acme/fpm", Type: "oci"},
		"frappe", "crm", "1.0.0", nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "unsupported protocol scheme") {
		t.Errorf("an OCI repository must not be fetched over HTTP: %v", err)
	}
	if !strings.Contains(err.Error(), "OCI") {
		t.Errorf("error should identify the backend: %v", err)
	}
}
