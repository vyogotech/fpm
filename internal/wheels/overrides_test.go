package wheels

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const drivePyProject = `[project]
name = "drive"
dependencies = [
    "av>=12.0.0",
    "mimemapper==0.4.1",
    "pymupdf>=1.24.0",
    "boto3~=1.43.29",
    "pycrdt==0.12.26",
]

[build-system]
requires = ["flit_core >=3.4,<4"]
build-backend = "flit_core.buildapi"
`

// TestApplyOverridesRewritesTheDeclaredDependency is drive's case: an upstream pin that
// cannot be satisfied for the target (pycrdt 0.12.26 publishes no cp314 wheel and is a
// Rust extension, so it can neither be downloaded nor cross-built). The packaged
// manifest has to carry the replacement, not just the wheels directory, because
// `fpm install` runs pip against that manifest.
func TestApplyOverridesRewritesTheDeclaredDependency(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, PyProjectFileName)
	if err := os.WriteFile(path, []byte(drivePyProject), 0o644); err != nil {
		t.Fatal(err)
	}

	applied, err := ApplyOverrides(dir, []string{"pycrdt>=0.14.4"})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 {
		t.Fatalf("expected one override, got %v", applied)
	}
	if applied[0].Name != "pycrdt" || applied[0].From != "pycrdt==0.12.26" || applied[0].To != "pycrdt>=0.14.4" {
		t.Fatalf("override not recorded faithfully: %+v", applied[0])
	}

	after, _ := os.ReadFile(path)
	got := string(after)
	if !strings.Contains(got, `"pycrdt>=0.14.4",`) {
		t.Fatalf("the manifest must carry the replacement:\n%s", got)
	}
	if strings.Contains(got, "0.12.26") {
		t.Fatalf("the old pin must be gone:\n%s", got)
	}
	// Everything else is untouched, including formatting and the build backend.
	for _, keep := range []string{`"av>=12.0.0",`, `"boto3~=1.43.29",`, `requires = ["flit_core >=3.4,<4"]`, `name = "drive"`} {
		if !strings.Contains(got, keep) {
			t.Fatalf("rewriting must not disturb %q:\n%s", keep, got)
		}
	}

	// And the dependency set fpm vendors from now names the replacement.
	req, err := Collect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(req.Specs, " "), "pycrdt>=0.14.4") {
		t.Fatalf("collected specs still carry the old pin: %v", req.Specs)
	}
}

func TestApplyOverridesRequirementsTxt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, RequirementsFileName)
	body := "# app deps\nfrappe\npycrdt==0.12.26  # collaborative editing\nboto3\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	applied, err := ApplyOverrides(dir, []string{"pycrdt>=0.14.4"})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || applied[0].File != RequirementsFileName {
		t.Fatalf("unexpected overrides: %+v", applied)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "pycrdt>=0.14.4  # collaborative editing") {
		t.Fatalf("the inline comment must survive:\n%s", got)
	}
	if !strings.Contains(string(got), "# app deps") || !strings.Contains(string(got), "boto3") {
		t.Fatalf("unrelated lines must survive:\n%s", got)
	}
}

// TestApplyOverridesRejectsAnUnmatchedName: an override that matches nothing means the
// caller is describing an app it is not looking at — an upstream rename, or a typo in
// the catalog — and silently packaging the original pin is how the broken package ships
// anyway.
func TestApplyOverridesRejectsAnUnmatchedName(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, PyProjectFileName), []byte(drivePyProject), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ApplyOverrides(dir, []string{"pycrdtt>=0.14.4"})
	if err == nil {
		t.Fatal("an override matching nothing must be an error")
	}
	if !strings.Contains(err.Error(), "does not match anything this app declares") {
		t.Fatalf("unhelpful error: %v", err)
	}

	_, err = ApplyOverrides(dir, []string{"pycrdt>=0.14.4", "pycrdt>=0.15"})
	if err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("one distribution cannot be overridden twice: %v", err)
	}
}

func TestApplyOverridesNoop(t *testing.T) {
	dir := t.TempDir()
	applied, err := ApplyOverrides(dir, nil)
	if err != nil || applied != nil {
		t.Fatalf("no overrides means no work: %v %v", applied, err)
	}
}

func TestRequirementName(t *testing.T) {
	cases := map[string]string{
		"pycrdt==0.12.26":                   "pycrdt",
		"PyMuPDF >= 1.24.0":                 "pymupdf",
		"boto3~=1.43.29":                    "boto3",
		"uvicorn[standard]>=0.20":           "uvicorn",
		"flit_core >=3.4,<4":                "flit-core",
		`requests; python_version < "3.12"`: "requests",
		"# a comment":                       "",
		"--index-url https://x":             "",
		"":                                  "",
	}
	for spec, want := range cases {
		if got := RequirementName(spec); got != want {
			t.Fatalf("RequirementName(%q) = %q, want %q", spec, got, want)
		}
	}
}

// TestApplyOverridesRewritesAdjacentLines guards the bug this regex had: a trailing
// \s* spans newlines, so one match swallowed the following requirement and the second
// of two adjacent lines was never rewritten.
func TestApplyOverridesRewritesAdjacentLines(t *testing.T) {
	dir := t.TempDir()
	body := "frappe\npycrdt==0.12.26\nboto3==1.0.0\npymupdf==1.0.0\n"
	if err := os.WriteFile(filepath.Join(dir, RequirementsFileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	applied, err := ApplyOverrides(dir, []string{"pycrdt>=0.14.4", "pymupdf>=1.24.0"})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 2 {
		t.Fatalf("both requirements must be rewritten, got %+v", applied)
	}
	got, _ := os.ReadFile(filepath.Join(dir, RequirementsFileName))
	want := "frappe\npycrdt>=0.14.4\nboto3==1.0.0\npymupdf>=1.24.0\n"
	if string(got) != want {
		t.Fatalf("rewrite = %q, want %q", got, want)
	}
}
