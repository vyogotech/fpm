package semver

import "testing"

// The bug these exist to kill: publish picked the latest version with a raw
// string comparison (`appVersion > remoteMeta.LatestVersion`), so a repository
// that had published 1.10.0 would still report 1.9.0 as latest — and
// `fpm install app` with no version pinned would quietly install the older one.

func TestDoubleDigitComponentsOutrankSingleDigit(t *testing.T) {
	cases := []struct{ lower, higher string }{
		{"1.9.0", "1.10.0"},
		{"1.0.9", "1.0.10"},
		{"9.0.0", "10.0.0"},
		{"1.9.9", "1.10.0"},
	}
	for _, tc := range cases {
		if Compare(tc.lower, tc.higher) >= 0 {
			t.Errorf("Compare(%q, %q) should be negative", tc.lower, tc.higher)
		}
	}
}

func TestPrecedenceBasics(t *testing.T) {
	if Compare("1.0.0", "1.0.0") != 0 {
		t.Error("identical versions should compare equal")
	}
	if Compare("2.0.0", "1.9.9") <= 0 {
		t.Error("a higher major must win regardless of the other components")
	}
}

func TestReleaseOutranksItsPrerelease(t *testing.T) {
	if Compare("1.0.0-rc.1", "1.0.0") >= 0 {
		t.Error("a prerelease must rank below its own release")
	}
}

func TestPrereleaseOrderingFollowsTheSpec(t *testing.T) {
	ordered := []string{
		"1.0.0-alpha",
		"1.0.0-alpha.1",
		"1.0.0-alpha.beta",
		"1.0.0-beta",
		"1.0.0-beta.2",
		"1.0.0-beta.11",
		"1.0.0-rc.1",
		"1.0.0",
	}
	for i := 1; i < len(ordered); i++ {
		if Compare(ordered[i-1], ordered[i]) >= 0 {
			t.Errorf("%q should rank below %q", ordered[i-1], ordered[i])
		}
	}
}

func TestBuildMetadataIsIgnored(t *testing.T) {
	if Compare("1.0.0+build.1", "1.0.0+build.2") != 0 {
		t.Error("build metadata is excluded from precedence")
	}
}

func TestMalformedVersionsDoNotPanic(t *testing.T) {
	// Publishers control these strings; a panic here would take down the
	// service rather than rejecting one package.
	for _, value := range []string{"", "latest", "v1.0.0", "not-a-version", "1.0.0.0.0"} {
		_ = Compare(value, "1.0.0")
	}
}

func TestUnparseableVersionsRankBelowRealOnes(t *testing.T) {
	if Compare("garbage", "0.0.1") >= 0 {
		t.Error("an unparseable version must not outrank a real one")
	}
}

func TestLatestPicksTheHighestStableVersion(t *testing.T) {
	versions := []string{"1.9.0", "1.10.0", "2.0.0-rc.1"}
	if got := Latest(versions); got != "1.10.0" {
		t.Errorf("Latest() = %q, want 1.10.0 — prereleases are not offered by default", got)
	}
}

func TestLatestFallsBackToAPrereleaseWhenNothingElseExists(t *testing.T) {
	// Reporting no latest version at all would hide the package entirely.
	if got := Latest([]string{"1.0.0-rc.1", "1.0.0-rc.2"}); got != "1.0.0-rc.2" {
		t.Errorf("Latest() = %q, want 1.0.0-rc.2", got)
	}
}

func TestLatestOfNothingIsEmpty(t *testing.T) {
	if got := Latest(nil); got != "" {
		t.Errorf("Latest(nil) = %q, want an empty string", got)
	}
}

func TestLatestRecomputesFromTheWholeSet(t *testing.T) {
	// Recomputing from every key, rather than comparing the newcomer against
	// the stored value, is what repairs metadata the old string comparison
	// already corrupted.
	if got := Latest([]string{"1.10.0", "1.9.0", "1.2.0"}); got != "1.10.0" {
		t.Errorf("Latest() = %q, want 1.10.0", got)
	}
}

func TestSortAscending(t *testing.T) {
	got := Sort([]string{"2.0.0", "1.0.0", "1.10.0", "1.9.0"})
	want := []string{"1.0.0", "1.9.0", "1.10.0", "2.0.0"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Sort() = %v, want %v", got, want)
		}
	}
}

func TestIsPrerelease(t *testing.T) {
	if !IsPrerelease("1.0.0-rc.1") {
		t.Error("1.0.0-rc.1 is a prerelease")
	}
	if IsPrerelease("1.0.0") {
		t.Error("1.0.0 is not a prerelease")
	}
}

func TestValid(t *testing.T) {
	for _, v := range []string{"1.0.0", "v15.93.1", "1.0", "1", "1.0.0.4", "1.0.0-rc.1+build.5"} {
		if !Valid(v) {
			t.Errorf("Valid(%q) should be true", v)
		}
	}
	for _, v := range []string{"", "version-15", "latest", "v15.x", "abc"} {
		if Valid(v) {
			t.Errorf("Valid(%q) should be false", v)
		}
	}
}

func TestMajor(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"15.93.1", 15},
		{"v14.0.0", 14},
		{"0.5.0", 0},
		{"v2", 2},
	}
	for _, tc := range cases {
		got, ok := Major(tc.in)
		if !ok || got != tc.want {
			t.Errorf("Major(%q) = %d, %v; want %d, true", tc.in, got, ok, tc.want)
		}
	}
	if _, ok := Major("version-15"); ok {
		t.Error("Major(\"version-15\") should not parse")
	}
}
