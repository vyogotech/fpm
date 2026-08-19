package mirror

import "testing"

func tagsOf(names ...string) []Tag {
	out := make([]Tag, len(names))
	for i, name := range names {
		out[i] = Tag{Name: name, SHA: "0000000000000000000000000000000000000000"}
	}
	return out
}

func TestLatestPerMajorPicksNewestStablePerLine(t *testing.T) {
	got := LatestPerMajor(tagsOf(
		"v14.0.0", "v14.9.0", "v14.10.0", // double-digit must win
		"v15.0.0", "v15.2.1",
		"v16.0.0-beta.1", // prerelease-only line must not appear
		"version-15",     // branch-style junk tag must be ignored
		"latest",
	), nil)

	want := []string{"v14.10.0", "v15.2.1"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i].Name != want[i] {
			t.Errorf("line %d: got %s, want %s", i, got[i].Name, want[i])
		}
	}
}

func TestLatestPerMajorHonorsAllowlist(t *testing.T) {
	got := LatestPerMajor(tagsOf("v12.5.0", "v14.1.0", "v15.1.0"), []int{14, 15})
	if len(got) != 2 || got[0].Name != "v14.1.0" || got[1].Name != "v15.1.0" {
		t.Errorf("allowlist not applied: %v", got)
	}
}

func TestLatestPerMajorFourPartVersions(t *testing.T) {
	got := LatestPerMajor(tagsOf("v1.0.0.1", "v1.0.0.2"), nil)
	if len(got) != 1 {
		t.Fatalf("got %v, want one v1 entry", got)
	}
}

func TestNormalizeVersion(t *testing.T) {
	cases := map[string]string{"v15.93.1": "15.93.1", "15.93.1": "15.93.1", " v1.0 ": "1.0"}
	for in, want := range cases {
		if got := NormalizeVersion(in); got != want {
			t.Errorf("NormalizeVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBranchPseudoVersionIsPrerelease(t *testing.T) {
	got := BranchPseudoVersion(2, "20260819", "abcdef0123456789")
	if got != "2.0.0-git.20260819.abcdef0123" {
		t.Errorf("BranchPseudoVersion = %q", got)
	}
}
