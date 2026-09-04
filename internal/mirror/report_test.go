package mirror

import "testing"

// TestAnyFailedCountsWithheldPackages: a run's contract is that every configured
// repository holds each planned version afterwards. An app built but kept out of the
// registry — its desk bundles would not compile, so installing it renders nothing —
// does not meet it, and exiting clean would leave a green run whose registry is quietly
// missing an app. That is how the catalogue came to hold packages that served nothing.
func TestAnyFailedCountsWithheldPackages(t *testing.T) {
	cases := []struct {
		action string
		failed bool
	}{
		{ActionPublished, false},
		{ActionPublishedNoDeps, false},
		{ActionPublishedNoAssets, false}, // --allow-unbuilt-assets: asked for, and it did publish
		{ActionSkippedExists, false},
		{ActionBuilt, false},         // --skip-publish: not publishing is the point
		{ActionBuiltNoAssets, false}, // likewise
		{ActionWithheldNoAssets, true},
		{ActionFailed, true},
	}
	for _, tc := range cases {
		if got := AnyFailed([]Result{{Slug: "wiki", Action: tc.action}}); got != tc.failed {
			t.Errorf("AnyFailed(%s) = %v, want %v", tc.action, got, tc.failed)
		}
	}
}
