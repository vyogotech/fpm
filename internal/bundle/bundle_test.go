package bundle

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseRejectsEmptyInstallOrder(t *testing.T) {
	if _, err := Parse([]byte(`{"install_order":[]}`)); err == nil {
		t.Fatal("expected an empty install_order to be rejected: an installer with nothing to do is a malformed bundle, not a no-op")
	}
}

func TestParseRejectsEntryWithoutAppOrVersion(t *testing.T) {
	for _, body := range []string{
		`{"install_order":[{"org":"o","version":"1.0.0","file":"a.fpm"}]}`,
		`{"install_order":[{"org":"o","app":"a","file":"a.fpm"}]}`,
	} {
		if _, err := Parse([]byte(body)); err == nil {
			t.Fatalf("expected rejection for %s", body)
		}
	}
}

// A bundle is unpacked into a directory the caller chose. An entry whose file
// escapes that directory would write anywhere the process can reach, so the
// manifest is the right place to stop it — before any blob is fetched.
func TestParseRejectsNonLocalFile(t *testing.T) {
	for _, file := range []string{"../evil.fpm", "/etc/evil.fpm", "nested/evil.fpm"} {
		body := `{"install_order":[{"org":"o","app":"a","version":"1.0.0","file":"` + file + `"}]}`
		if _, err := Parse([]byte(body)); err == nil {
			t.Fatalf("expected rejection of non-local file %q", file)
		}
	}
}

func TestShippedSkipsBenchProvided(t *testing.T) {
	m, err := Parse([]byte(`{"install_order":[
		{"org":"frappe","app":"erpnext","version":"16.0.0","provided_by":"bench"},
		{"org":"vyogotech","app":"frappe_cloud_manager","version":"1.0.0","file":"fcm-1.0.0.fpm"},
		{"org":"vyogotech","app":"saas_platform","version":"1.0.0","file":"sp-1.0.0.fpm"}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.InstallOrder) != 3 {
		t.Fatalf("install_order should keep every entry, got %d", len(m.InstallOrder))
	}
	shipped := m.Shipped()
	if len(shipped) != 2 {
		t.Fatalf("expected 2 shipped packages, got %d", len(shipped))
	}
	// erpnext comes from the base image: recorded so the installer can check the
	// bench has it, never carried and never fetched.
	for _, e := range shipped {
		if e.App == "erpnext" {
			t.Fatal("a bench-provided app must not be shipped")
		}
	}
	if shipped[0].App != "frappe_cloud_manager" {
		t.Fatalf("shipped order must follow install order, got %s first", shipped[0].App)
	}
}

func TestReadWriteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := &Manifest{
		Root:         Entry{Org: "vyogotech", App: "saas_platform", Version: "1.0.0", File: "sp-1.0.0.fpm"},
		InstallOrder: []Entry{{Org: "vyogotech", App: "saas_platform", Version: "1.0.0", File: "sp-1.0.0.fpm"}},
		CreatedBy:    "test",
	}
	if err := Write(dir, in); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ManifestName)); err != nil {
		t.Fatalf("expected %s on disk: %v", ManifestName, err)
	}
	out, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if out.Root.Identifier() != in.Root.Identifier() || out.CreatedBy != "test" {
		t.Fatalf("round trip lost data: %+v", out)
	}
}

func TestReadNonBundleDirectory(t *testing.T) {
	if _, err := Read(t.TempDir()); err == nil {
		t.Fatal("expected a directory without a manifest to be rejected")
	}
}
