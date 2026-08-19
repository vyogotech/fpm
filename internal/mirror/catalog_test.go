package mirror

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const header = "slug,repo,app_name,track,branch,branch_major,majors,bundle_deps,enabled,notes\n"

func writeCatalog(t *testing.T, rows string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "apps.csv")
	if err := os.WriteFile(path, []byte(header+rows), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadCatalogDefaults(t *testing.T) {
	path := writeCatalog(t, "hrms,https://github.com/frappe/hrms,,,,,,,,\n")
	cat, err := LoadCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	app := cat.Apps[0]
	if app.Track != TrackTags || !app.BundleDeps || !app.Enabled || len(app.Majors) != 0 {
		t.Errorf("defaults not applied: %+v", app)
	}
	if app.MetadataName() != "hrms" {
		t.Errorf("MetadataName() = %q, want hrms", app.MetadataName())
	}
}

func TestLoadCatalogParsesFields(t *testing.T) {
	path := writeCatalog(t,
		"frappe,https://github.com/frappe/frappe,,,,,14;15;16,,,\n"+
			"health,https://github.com/frappe/health,healthcare,,,,,false,,differs\n"+
			"drive,https://github.com/frappe/drive,,branch,main,2,,,false,no tags\n")
	cat, err := LoadCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cat.Apps[0].Majors; len(got) != 3 || got[0] != 14 || got[2] != 16 {
		t.Errorf("majors = %v, want [14 15 16]", got)
	}
	if app := cat.Apps[1]; app.MetadataName() != "healthcare" || app.BundleDeps {
		t.Errorf("overrides not applied: %+v", app)
	}
	if app := cat.Apps[2]; app.Track != TrackBranch || app.Branch != "main" || app.BranchMajor != 2 || app.Enabled {
		t.Errorf("branch row misparsed: %+v", app)
	}
}

func TestLoadCatalogRejectsThirdPartyRepo(t *testing.T) {
	path := writeCatalog(t, "evil,https://github.com/notfrappe/evil,,,,,,,,\n")
	if _, err := LoadCatalog(path); err == nil || !strings.Contains(err.Error(), "frappe-org") {
		t.Fatalf("expected frappe-org rejection, got %v", err)
	}
}

func TestLoadCatalogRejectsBadRows(t *testing.T) {
	cases := map[string]string{
		"duplicate slug":  "wiki,https://github.com/frappe/wiki,,,,,,,,\nwiki,https://github.com/frappe/wiki,,,,,,,,\n",
		"bad track":       "wiki,https://github.com/frappe/wiki,,nightly,,,,,,\n",
		"branch required": "wiki,https://github.com/frappe/wiki,,branch,,,,,,\n",
		"stray branch":    "wiki,https://github.com/frappe/wiki,,,develop,,,,,\n",
		"bad majors":      "wiki,https://github.com/frappe/wiki,,,,,x;y,,,\n",
		"bad bool":        "wiki,https://github.com/frappe/wiki,,,,,,yes,,\n",
		"bad slug":        "Wiki!,https://github.com/frappe/wiki,,,,,,,,\n",
	}
	for name, rows := range cases {
		if _, err := LoadCatalog(writeCatalog(t, rows)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestLoadCatalogRejectsUnknownColumn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "apps.csv")
	bad := "slug,repo,app_name,track,branch,branch_major,majours,bundle_deps,enabled,notes\n"
	if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCatalog(path); err == nil || !strings.Contains(err.Error(), "majours") {
		t.Fatalf("expected unknown-column error, got %v", err)
	}
}

func TestLoadCatalogFindsBuildScript(t *testing.T) {
	path := writeCatalog(t, "crm,https://github.com/frappe/crm,,,,,,,,\n")
	buildDir := filepath.Join(filepath.Dir(path), "build")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(buildDir, "crm.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cat, err := LoadCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	if cat.Apps[0].BuildScript != script {
		t.Errorf("BuildScript = %q, want %q", cat.Apps[0].BuildScript, script)
	}
}

func TestEnabledFilter(t *testing.T) {
	path := writeCatalog(t,
		"wiki,https://github.com/frappe/wiki,,,,,,,,\n"+
			"lms,https://github.com/frappe/lms,,,,,,,false,paused\n")
	cat, err := LoadCatalog(path)
	if err != nil {
		t.Fatal(err)
	}

	all, err := cat.Enabled(nil)
	if err != nil || len(all) != 1 || all[0].Slug != "wiki" {
		t.Fatalf("Enabled(nil) = %v, %v; want just wiki", all, err)
	}
	if _, err := cat.Enabled([]string{"nope"}); err == nil {
		t.Error("unknown slug in filter must error")
	}
	if _, err := cat.Enabled([]string{"lms"}); err == nil {
		t.Error("disabled slug in filter must error")
	}
}

func TestShippedCatalogLoads(t *testing.T) {
	cat, err := LoadCatalog(filepath.Join("..", "..", "catalog", "apps.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Apps) < 10 {
		t.Errorf("shipped catalog has %d apps, expected the seeded set", len(cat.Apps))
	}
}
