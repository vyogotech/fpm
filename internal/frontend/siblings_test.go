package frontend

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestSiblingAppsFindsBenchAppsABuildReadsOffDisk: helpdesk's desk build runs
// `cd ../../frappe/ui && yarn install`, which needs frappe checked out beside it. That
// is a build-time dependency, not a `required_apps` entry, and nothing resolved it —
// helpdesk 1.29 failed with "can't cd to ../../frappe/ui".
func TestSiblingAppsFindsBenchAppsABuildReadsOffDisk(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "package.json"), `{"scripts":{"build":"cd desk && yarn build"}}`)
	write(t, filepath.Join(root, "desk", "package.json"), `{"scripts":{
	  "build":"yarn install-framework-ui && vite build",
	  "install-framework-ui":"[ -f ../../frappe/ui/node_modules/.yarn-integrity ] || (cd ../../frappe/ui && yarn install)"
	}}`)
	write(t, filepath.Join(root, "helpdesk", "hooks.py"), "app_name = \"helpdesk\"\n")

	got, err := SiblingApps(root, "helpdesk")
	if err != nil {
		t.Fatalf("SiblingApps: %v", err)
	}
	if strings.Join(got, ",") != "frappe" {
		t.Fatalf("got %v, want [frappe]", got)
	}
}

// The app's own name is not a sibling, and an app that reaches for nothing reports none.
func TestSiblingAppsIgnoresSelfAndFindsNoneWhenAbsent(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "frontend", "package.json"), `{"scripts":{
	  "build":"vite build",
	  "copy":"cp ../../crm/x ../../crm/y"
	}}`)
	write(t, filepath.Join(root, "crm", "hooks.py"), "app_name = \"crm\"\n")

	got, err := SiblingApps(root, "crm")
	if err != nil {
		t.Fatalf("SiblingApps: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %v, want none (crm is not its own sibling)", got)
	}
}
