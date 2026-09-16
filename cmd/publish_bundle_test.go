package cmd

import (
	"testing"

	"fpm/internal/bundle"
)

func TestParseBundleCoordinateDefaultsToRoot(t *testing.T) {
	m := &bundle.Manifest{Root: bundle.Entry{Org: "vyogotech", App: "saas_platform", Version: "1.0.0"}}
	org, name, err := parseBundleCoordinate("", m)
	if err != nil {
		t.Fatal(err)
	}
	if org != "vyogotech" || name != "saas_platform" {
		t.Fatalf("got %s/%s", org, name)
	}
}

// A stack is usually published under a name of its own rather than its root
// app's, so --as overrides the coordinate.
func TestParseBundleCoordinateOverride(t *testing.T) {
	m := &bundle.Manifest{Root: bundle.Entry{Org: "vyogotech", App: "saas_platform"}}
	org, name, err := parseBundleCoordinate("vyogotech/fcloud-stack", m)
	if err != nil {
		t.Fatal(err)
	}
	if org != "vyogotech" || name != "fcloud-stack" {
		t.Fatalf("got %s/%s", org, name)
	}
}

func TestParseBundleCoordinateRejectsMalformed(t *testing.T) {
	m := &bundle.Manifest{Root: bundle.Entry{Org: "o", App: "a"}}
	for _, as := range []string{"noslash", "a/b/c", "/b", "a/", " / "} {
		if _, _, err := parseBundleCoordinate(as, m); err == nil {
			t.Fatalf("expected rejection of --as %q", as)
		}
	}
}

// A bundle whose root carries no coordinate cannot be named implicitly; the
// error must say so rather than publishing under an empty path.
func TestParseBundleCoordinateRequiresNameWhenRootHasNone(t *testing.T) {
	if _, _, err := parseBundleCoordinate("", &bundle.Manifest{}); err == nil {
		t.Fatal("expected an error when the root has no org/app and --as is absent")
	}
}

func TestParseRemoteIdentifier(t *testing.T) {
	cases := []struct {
		in                string
		org, app, version string
		ok                bool
	}{
		{"vyogotech/fcloud-stack==1.0.0", "vyogotech", "fcloud-stack", "1.0.0", true},
		{"vyogotech/fcloud-stack", "vyogotech", "fcloud-stack", "", true},
		{"noslash", "", "", "", false},
		{"a/b/c", "", "", "", false},
		{"/b", "", "", "", false},
	}
	for _, c := range cases {
		org, app, version, ok := parseRemoteIdentifier(c.in)
		if ok != c.ok || org != c.org || app != c.app || version != c.version {
			t.Fatalf("%q: got (%q,%q,%q,%v) want (%q,%q,%q,%v)", c.in, org, app, version, ok, c.org, c.app, c.version, c.ok)
		}
	}
}

// A local path must never be probed as a remote coordinate, or `fpm install
// ./some/dir` would go to the network before looking on disk.
func TestParseRemoteIdentifierRejectsPaths(t *testing.T) {
	for _, p := range []string{"./bundle-dir", "../x/y", "/abs/path/pkg.fpm", "dist/app-1.0.0.fpm"} {
		if _, _, _, ok := parseRemoteIdentifier(p); ok {
			t.Fatalf("path %q must not parse as a remote identifier", p)
		}
	}
}
