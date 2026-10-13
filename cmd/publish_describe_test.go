package cmd

import (
	"testing"

	"fpm/internal/repository"
)

func TestTheLatestVersionDescribesTheApp(t *testing.T) {
	meta := &repository.PackageMetadata{
		Title: "Frappe AI", Description: "the old words", LatestVersion: "1.0.1",
		Versions: map[string]repository.PackageVersionMetadata{"1.0.0": {}, "1.0.1": {}},
	}
	renamed := appDescription{Title: "AI Sidebar", Description: "the new words", Icon: "octicon octicon-comment"}

	describeFromLatest(meta, "1.0.0", renamed)
	if meta.Title != "Frappe AI" || meta.Description != "the old words" || meta.Icon != "" {
		t.Fatalf("republishing an older version must not rename the app: %+v", meta)
	}

	describeFromLatest(meta, "1.0.1", renamed)
	if meta.Title != "AI Sidebar" || meta.Description != "the new words" || meta.Icon != "octicon octicon-comment" {
		t.Fatalf("the latest version describes the app: %+v", meta)
	}

	describeFromLatest(meta, "1.0.1", appDescription{})
	if meta.Title != "AI Sidebar" || meta.Description != "the new words" {
		t.Fatalf("a package that says nothing about the app leaves the listing as it was: %+v", meta)
	}
}
