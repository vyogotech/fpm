package cmd

import (
	"errors"
	"path/filepath"
	"testing"

	"fpm/internal/benchbuild"
	"fpm/internal/metadata"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPackageRefusesUnbuiltDeskAssets is issue #9's systemic half: every front-end
// package in the published catalogue installed and then rendered nothing, because
// nothing compiled its bundles and a bench installing from a package never builds
// them. The state was only visible as a line in the install log.
func TestPackageRefusesUnbuiltDeskAssets(t *testing.T) {
	src := SharedCreateMinimalAppForPackage(t, t.TempDir(), "wiki", map[string]string{
		"wiki/public/js/wiki.bundle.js":   "// esbuild entry point",
		"wiki/public/css/wiki.bundle.css": "/* esbuild entry point */",
	})

	args, outDir := packageArgs(t, src, "3.0.0", "--org", "frappe")
	out, err := SharedExecuteCommand(rootCmd, args...)

	require.Error(t, err, out)
	assert.True(t, errors.Is(err, benchbuild.ErrBuildFailed), "got %v", err)
	assert.Equal(t, ExitAssetBuildFailed, ExitCodeFor(err))
	assert.Contains(t, err.Error(), "wiki/public/dist")
	assert.Contains(t, err.Error(), "--bench-path")
	assert.NoFileExists(t, filepath.Join(outDir, "wiki-3.0.0.fpm"), "nothing is published for an app whose UI would not render")
}

// TestPackageAllowsUnbuiltAssetsOnRequest keeps the escape hatch, and says out loud
// what the package will not do.
func TestPackageAllowsUnbuiltAssetsOnRequest(t *testing.T) {
	src := SharedCreateMinimalAppForPackage(t, t.TempDir(), "wiki", map[string]string{
		"wiki/public/js/wiki.bundle.js": "// esbuild entry point",
	})

	args, outDir := packageArgs(t, src, "3.0.0", "--org", "frappe", "--allow-unbuilt-assets")
	out, err := SharedExecuteCommand(rootCmd, args...)
	require.NoError(t, err, out)
	assert.Contains(t, out, "will not render the app's desk UI")

	meta, err := metadata.ReadMetadataFromFPMArchive(filepath.Join(outDir, "wiki-3.0.0.fpm"))
	require.NoError(t, err)
	assert.False(t, meta.AssetsBuilt)
}

// TestPackageDevOnlyWarnsAboutUnbuiltAssets: a development package is iterated on
// inside a bench that can build, so it is a warning rather than a refusal.
func TestPackageDevOnlyWarnsAboutUnbuiltAssets(t *testing.T) {
	src := SharedCreateMinimalAppForPackage(t, t.TempDir(), "wiki", map[string]string{
		"wiki/public/js/wiki.bundle.js": "// esbuild entry point",
	})

	args, outDir := packageArgs(t, src, "3.0.0", "--org", "frappe", "--package-type", "dev")
	out, err := SharedExecuteCommand(rootCmd, args...)
	require.NoError(t, err, out)
	assert.Contains(t, out, "will not render the app's desk UI")
	assert.FileExists(t, filepath.Join(outDir, "wiki-3.0.0.fpm"))
}

// TestPackageAcceptsAnAppWithNoBundleSources: an SPA-only app, or one with no assets
// at all, has nothing to compile and must package unhindered.
func TestPackageAcceptsAnAppWithNoBundleSources(t *testing.T) {
	src := SharedCreateMinimalAppForPackage(t, t.TempDir(), "plain", map[string]string{
		"plain/public/images/logo.svg": "<svg/>",
	})

	args, outDir := packageArgs(t, src, "1.0.0", "--org", "acme")
	out, err := SharedExecuteCommand(rootCmd, args...)
	require.NoError(t, err, out)
	assert.FileExists(t, filepath.Join(outDir, "plain-1.0.0.fpm"))
}

// TestPackageAcceptsPrebuiltBundles: a checkout that already holds compiled output —
// built by hand, or by a previous fpm run inside a bench — is exactly what the guard
// is looking for.
func TestPackageAcceptsPrebuiltBundles(t *testing.T) {
	src := SharedCreateMinimalAppForPackage(t, t.TempDir(), "wiki", map[string]string{
		"wiki/public/js/wiki.bundle.js":               "// esbuild entry point",
		"wiki/public/dist/js/wiki.bundle.ABCDEFGH.js": "console.log(1)",
	})

	args, outDir := packageArgs(t, src, "3.0.0", "--org", "frappe")
	out, err := SharedExecuteCommand(rootCmd, args...)
	require.NoError(t, err, out)

	meta, err := metadata.ReadMetadataFromFPMArchive(filepath.Join(outDir, "wiki-3.0.0.fpm"))
	require.NoError(t, err)
	assert.True(t, meta.AssetsBuilt)
	assert.Contains(t, meta.AssetBundles, "wiki.bundle.js")
}
