package semver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseConstraintAndMatch(t *testing.T) {
	cases := []struct {
		spec    string
		matches []string
		misses  []string
	}{
		{">=16.0.0,<17.0.0", []string{"16.0.0", "16.30.0", "16.99.1"}, []string{"15.120.0", "17.0.0", "17.0.0-dev", "16.5.0-rc.1"}},
		{">=16.0.0-0,<17.0.0", []string{"16.0.0", "16.30.0", "16.0.0-beta.1"}, []string{"17.0.0-dev", "15.120.0"}},
		{"==16.30.0", []string{"16.30.0", "v16.30.0", "16.30"}, []string{"16.30.1"}},
		{"16.30.0", []string{"16.30.0"}, []string{"16.31.0"}},
		{"!=16.30.0", []string{"16.30.1"}, []string{"16.30.0"}},
		{">15", []string{"15.0.1", "16.0.0"}, []string{"15.0.0", "14.9.9"}},
		{"", []string{"anything", "16.0.0"}, nil},
		{"*", []string{"16.0.0"}, nil},
	}
	for _, tc := range cases {
		c, err := ParseConstraint(tc.spec)
		require.NoError(t, err, tc.spec)
		for _, v := range tc.matches {
			assert.True(t, c.Matches(v), "%q should match %q", tc.spec, v)
		}
		for _, v := range tc.misses {
			assert.False(t, c.Matches(v), "%q should not match %q", tc.spec, v)
		}
	}
}

// An unorderable version satisfies no bounded constraint: accepting it would mean
// installing something the package never declared compatibility with.
func TestConstraintRejectsUnparseableVersion(t *testing.T) {
	c := MustParseConstraint(">=16.0.0,<17.0.0")
	assert.False(t, c.Matches("sixteen"))
	assert.True(t, Constraint{}.Matches("sixteen"), "an empty constraint still accepts anything")
}

func TestParseConstraintErrors(t *testing.T) {
	for _, spec := range []string{">=", ">=not-a-version", "16.0.0,<>"} {
		_, err := ParseConstraint(spec)
		assert.Error(t, err, spec)
	}
}

func TestMajorLine(t *testing.T) {
	assert.Equal(t, ">=16.0.0-0,<17.0.0", MajorLine("16.30.0"))
	assert.Equal(t, ">=0.0.0-0,<1.0.0", MajorLine("0.0.0-git.20260828.86fefa9faf"))
	assert.Equal(t, "", MajorLine("not-a-version"))

	// The reason the lower bound carries -0: the two packages from issue #14 pinned
	// the same app at two pseudo-versions, and both must accept the one that is
	// actually installed.
	line := MustParseConstraint(MajorLine("0.0.0-git.20260828.86fefa9faf"))
	assert.True(t, line.Matches("0.0.0-git.20260827.86fefa9faf"))
	assert.True(t, line.Matches("0.9.1"))
	assert.False(t, line.Matches("1.0.0"))

	// An erpnext patch upgrade no longer invalidates a package built against 16.16.0.
	v16 := MustParseConstraint(MajorLine("16.16.0"))
	assert.True(t, v16.Matches("16.30.0"))
	assert.False(t, v16.Matches("15.120.0"))
}

func TestConstraintSelect(t *testing.T) {
	available := []string{"15.120.0", "16.0.0", "16.30.0", "17.0.0-dev"}
	assert.Equal(t, "16.30.0", MustParseConstraint(">=16.0.0-0,<17.0.0").Select(available))
	assert.Equal(t, "17.0.0-dev", MustParseConstraint(">=17.0.0-0,<18.0.0").Select(available),
		"a line with only prereleases still resolves")
	assert.Equal(t, "", MustParseConstraint(">=18.0.0").Select(available))
	assert.Equal(t, "17.0.0-dev", Constraint{}.Select([]string{"17.0.0-dev"}))
}

func TestSplitRequirement(t *testing.T) {
	cases := map[string][2]string{
		"frappe/erpnext==16.30.0":         {"frappe/erpnext", "==16.30.0"},
		"frappe/erpnext>=16.0.0,<17.0.0":  {"frappe/erpnext", ">=16.0.0,<17.0.0"},
		"erpnext":                         {"erpnext", ""},
		"  frappe/erpnext  ==  16.30.0  ": {"frappe/erpnext", "==  16.30.0"},
	}
	for input, want := range cases {
		name, spec := SplitRequirement(input)
		assert.Equal(t, want[0], name, input)
		assert.Equal(t, want[1], spec, input)
	}
}

func TestExactVersion(t *testing.T) {
	v, ok := MustParseConstraint("==16.30.0").ExactVersion()
	assert.True(t, ok)
	assert.Equal(t, "16.30.0", v)

	_, ok = MustParseConstraint(">=16.0.0,<17.0.0").ExactVersion()
	assert.False(t, ok)
}
