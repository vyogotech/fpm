package cmd

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fpm/internal/metadata"

	"github.com/stretchr/testify/require"
)

// buildDepsFixture writes an .fpm containing the given manifests and bundled wheels.
func buildDepsFixture(t *testing.T, dir, org, appName, version string,
	meta metadata.AppMetadata, files map[string]string,
) string {
	t.Helper()

	fpmPath := filepath.Join(dir, fmt.Sprintf("%s-%s.fpm", appName, version))
	f, err := os.Create(fpmPath)
	require.NoError(t, err)
	defer f.Close()

	zw := zip.NewWriter(f)

	meta.Org = org
	meta.AppName = appName
	meta.PackageName = appName
	meta.PackageVersion = version
	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	require.NoError(t, err)
	w, err := zw.Create("app_metadata.json")
	require.NoError(t, err)
	_, err = w.Write(metaBytes)
	require.NoError(t, err)

	_, err = zw.Create(appName + "/")
	require.NoError(t, err)
	w, err = zw.Create(appName + "/hooks.py")
	require.NoError(t, err)
	_, err = io.WriteString(w, "app_name = \""+appName+"\"\n")
	require.NoError(t, err)

	for name, content := range files {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = io.WriteString(w, content)
		require.NoError(t, err)
	}

	require.NoError(t, zw.Close())
	return fpmPath
}

func TestDepsCommandReadsBothManifests(t *testing.T) {
	dir := t.TempDir()
	fpmPath := buildDepsFixture(t, dir, "acme", "dep_app", "1.0.0",
		metadata.AppMetadata{PackageType: "prod"},
		map[string]string{
			"requirements.txt": "requests==2.31.0\n# a comment\n",
			"pyproject.toml": `
[project]
name = "dep_app"
dependencies = ["frappe>=15.0.0"]
`,
		})

	output, err := SharedExecuteCommand(rootCmd, "deps", fpmPath)
	require.NoError(t, err)

	require.Contains(t, output, "acme/dep_app")
	require.Contains(t, output, "1.0.0")
	require.Contains(t, output, "requests==2.31.0")
	require.Contains(t, output, "frappe>=15.0.0")
	require.Contains(t, output, "requirements.txt")
	require.Contains(t, output, "pyproject.toml")
	// Comments are not dependency specifiers.
	require.NotContains(t, output, "a comment")
}

// TestDepsCommandReportsBundledWheels covers the offline-install case: the point of
// `deps` is telling you whether an install will reach the network.
func TestDepsCommandReportsBundledWheels(t *testing.T) {
	dir := t.TempDir()
	fpmPath := buildDepsFixture(t, dir, "acme", "bundled_app", "2.0.0",
		metadata.AppMetadata{PackageType: "prod", WheelPlatform: "manylinux2014_x86_64"},
		map[string]string{
			"requirements.txt":                         "six==1.16.0\n",
			"wheels/six-1.16.0-py2.py3-none-any.whl":   "wheel",
			"wheels/flit_core-3.12.0-py3-none-any.whl": "wheel",
		})

	output, err := SharedExecuteCommand(rootCmd, "deps", fpmPath)
	require.NoError(t, err)

	require.Contains(t, output, "2 wheel(s)")
	require.Contains(t, output, "manylinux2014_x86_64")
	require.Contains(t, output, "six-1.16.0-py2.py3-none-any.whl")
	require.Contains(t, output, "flit_core-3.12.0-py3-none-any.whl")
}

// TestDepsCommandReportsNoBundle makes the network dependency explicit rather than
// leaving the reader to infer it from an absent section.
func TestDepsCommandReportsNoBundle(t *testing.T) {
	dir := t.TempDir()
	fpmPath := buildDepsFixture(t, dir, "acme", "online_app", "1.0.0",
		metadata.AppMetadata{PackageType: "dev"},
		map[string]string{"requirements.txt": "requests\n"})

	output, err := SharedExecuteCommand(rootCmd, "deps", fpmPath)
	require.NoError(t, err)

	require.Contains(t, output, "resolves dependencies from the network")
}

func TestDepsCommandNoDependencies(t *testing.T) {
	dir := t.TempDir()
	fpmPath := buildDepsFixture(t, dir, "acme", "bare_app", "1.0.0",
		metadata.AppMetadata{PackageType: "prod"}, nil)

	output, err := SharedExecuteCommand(rootCmd, "deps", fpmPath)
	require.NoError(t, err)

	require.Contains(t, output, "none declared")
}

func TestDepsCommandMissingFile(t *testing.T) {
	_, err := SharedExecuteCommand(rootCmd, "deps", filepath.Join(t.TempDir(), "absent.fpm"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestParseDepsIdentifier(t *testing.T) {
	cases := []struct {
		in                    string
		org, appName, version string
		wantErr               bool
	}{
		{in: "acme/my_app", org: "acme", appName: "my_app"},
		{in: "acme/my_app==1.2.3", org: "acme", appName: "my_app", version: "1.2.3"},
		{in: "acme/my_app==latest", org: "acme", appName: "my_app", version: "latest"},
		{in: "my_app", wantErr: true},
		{in: "/my_app", wantErr: true},
		{in: "acme/", wantErr: true},
		{in: "a/b/c", wantErr: true},
	}

	for _, c := range cases {
		org, appName, version, err := parseDepsIdentifier(c.in)
		if c.wantErr {
			if err == nil {
				t.Fatalf("%q should be rejected", c.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q: unexpected error %v", c.in, err)
		}
		if org != c.org || appName != c.appName || version != c.version {
			t.Fatalf("%q: got %s/%s@%s, want %s/%s@%s",
				c.in, org, appName, version, c.org, c.appName, c.version)
		}
	}
}

func TestParsePackageIdentifierForSearch(t *testing.T) {
	if org, app, ok := parsePackageIdentifier("acme/my_app"); !ok || org != "acme" || app != "my_app" {
		t.Fatalf("expected acme/my_app to parse, got %s/%s ok=%v", org, app, ok)
	}
	// A bare keyword is not an exact identifier and cannot be looked up directly.
	for _, q := range []string{"", "inventory", "acme/my_app==1.0.0", "acme/*", "a/b/c"} {
		if _, _, ok := parsePackageIdentifier(q); ok {
			t.Fatalf("%q should not parse as an exact package identifier", q)
		}
	}
}

// TestDepsOutputDoesNotClaimFPMDependencyResolution guards the honesty of the output:
// FPM-level package dependencies are recorded but never resolved during install.
func TestDepsOutputDoesNotClaimFPMDependencyResolution(t *testing.T) {
	dir := t.TempDir()
	fpmPath := buildDepsFixture(t, dir, "acme", "fpmdeps_app", "1.0.0",
		metadata.AppMetadata{
			PackageType:  "prod",
			Dependencies: map[string]string{"erpnext": "15.0.0"},
		}, nil)

	output, err := SharedExecuteCommand(rootCmd, "deps", fpmPath)
	require.NoError(t, err)

	require.Contains(t, output, "erpnext")
	require.True(t, strings.Contains(output, "not resolved during install"),
		"output must not imply FPM dependency resolution exists:\n%s", output)
}

func TestDepsInstallationPlanAndBenchDetection(t *testing.T) {
	dir := t.TempDir()
	benchDir := filepath.Join(dir, "frappe-bench")
	require.NoError(t, os.MkdirAll(filepath.Join(benchDir, "apps", "payments", "payments"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(benchDir, "sites"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(benchDir, "sites", "apps.txt"), []byte("frappe\npayments\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(benchDir, "apps", "payments", "payments", "hooks.py"), []byte("app_name = 'payments'\napp_version = '15.0.0'\n"), 0o644))

	fpmPath := buildDepsFixture(t, dir, "frappe", "hrms", "15.2.0",
		metadata.AppMetadata{
			PackageType: "prod",
			RequiredApps: []metadata.RequiredApp{
				{Org: "frappe", Name: "payments", Version: "15.0.0"},
			},
		}, nil)

	output, err := SharedExecuteCommand(rootCmd, "deps", fpmPath, "--bench-path", benchDir)
	require.NoError(t, err)

	require.Contains(t, output, "payments==15.0.0 -> SKIP (already present in bench)")
	require.Contains(t, output, "frappe/hrms==15.2.0 (target) -> INSTALL into bench")
	require.Contains(t, output, "Total apps to be installed / upgraded in bench: 1")
}

func TestDepsJSONOutputWithInstallQueue(t *testing.T) {
	dir := t.TempDir()
	fpmPath := buildDepsFixture(t, dir, "frappe", "hrms", "15.2.0",
		metadata.AppMetadata{
			PackageType: "prod",
			RequiredApps: []metadata.RequiredApp{
				{Org: "frappe", Name: "erpnext", Version: "15.2.0"},
			},
		}, nil)

	output, err := SharedExecuteCommand(rootCmd, "deps", fpmPath, "--json", "--no-remote")
	require.NoError(t, err)

	var report DepsReport
	require.NoError(t, json.Unmarshal([]byte(output), &report))

	require.Equal(t, "frappe", report.Org)
	require.Equal(t, "hrms", report.App)
	require.Equal(t, "15.2.0", report.Version)
	require.Contains(t, report.InstallQueue, "frappe/hrms==15.2.0")
	require.NotEmpty(t, report.InstallPlan)
}
