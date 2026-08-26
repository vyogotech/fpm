package apputils

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRequiredApps(t *testing.T) {
	cases := []struct {
		name   string
		source string
		want   []string
	}{
		{"absent", `app_name = "x"` + "\n", nil},
		{"empty list", "required_apps = []\n", nil},
		{"single line", `required_apps = ["frappe", "erpnext"]` + "\n", []string{"frappe", "erpnext"}},
		{"single quotes", `required_apps = ['frappe/erpnext']` + "\n", []string{"frappe/erpnext"}},
		{"multi line with comments", "required_apps = [\n\t\"frappe\",  # core\n\t# \"skipped\",\n\t\"https://github.com/frappe/hrms.git@version-15\",\n]\n",
			[]string{"frappe", "https://github.com/frappe/hrms.git@version-15"}},
		{"commented out assignment ignored", "# required_apps = [\"erpnext\"]\napp_name = \"x\"\n", nil},
		{"indented is still matched", "\trequired_apps = [\"erpnext\"]\n", []string{"erpnext"}},
		{"bracket inside string", `required_apps = ["weird[app]"]` + "\n", []string{"weird[app]"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseRequiredApps(tc.source)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}

	t.Run("unterminated list", func(t *testing.T) {
		_, err := ParseRequiredApps("required_apps = [\"frappe\",\n")
		require.Error(t, err)
	})
}

func TestGetRequiredAppsFromHooks(t *testing.T) {
	dir := t.TempDir()
	hooks := filepath.Join(dir, "hooks.py")
	require.NoError(t, os.WriteFile(hooks, []byte("app_name = \"a\"\nrequired_apps = [\"frappe\", \"erpnext\"]\n"), 0o644))
	got, err := GetRequiredAppsFromHooks(hooks)
	require.NoError(t, err)
	assert.Equal(t, []string{"frappe", "erpnext"}, got)

	_, err = GetRequiredAppsFromHooks(filepath.Join(dir, "missing.py"))
	require.Error(t, err)
	assert.True(t, os.IsNotExist(errUnwrapAll(err)))
}

func errUnwrapAll(err error) error {
	for {
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return err
		}
		err = u.Unwrap()
	}
}

// These mirror frappe.installer.parse_required_app_name exactly.
func TestParseRequiredAppName(t *testing.T) {
	cases := map[string]string{
		"erpnext":                                       "erpnext",
		"frappe/erpnext":                                "erpnext",
		"https://github.com/frappe/erpnext":             "erpnext",
		"https://github.com/frappe/erpnext.git":         "erpnext",
		"https://github.com/frappe/erpnext.git@v15":     "erpnext",
		"git@github.com:frappe/erpnext.git":             "erpnext",
		"erpnext@develop":                               "erpnext",
		"https://github.com/frappe/hrms/":               "hrms",
		"https://github.com/frappe/hrms#fragment":       "hrms",
		"  frappe/payments  ":                           "payments",
		"https://gitlab.example.com/grp/sub/my_app.git": "my_app",
	}
	for in, want := range cases {
		assert.Equal(t, want, ParseRequiredAppName(in), in)
	}
}

func TestParseRequiredAppOrg(t *testing.T) {
	cases := map[string]string{
		"erpnext":        "",
		"frappe/erpnext": "frappe",
		"https://github.com/frappe/erpnext.git@v15": "frappe",
		"git@github.com:vyogotech/my_app.git":       "vyogotech",
		"https://gitlab.example.com/grp/sub/app":    "sub",
	}
	for in, want := range cases {
		assert.Equal(t, want, ParseRequiredAppOrg(in), in)
	}
}
