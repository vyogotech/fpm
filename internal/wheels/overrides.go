package wheels

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Override is one dependency the packager replaced, recorded so the package says what
// was changed rather than quietly differing from its source.
type Override struct {
	// Name is the distribution whose requirement was replaced, normalised.
	Name string `json:"name"`
	// From is the requirement as the app declared it.
	From string `json:"from"`
	// To is the requirement the package carries and vendored wheels for.
	To string `json:"to"`
	// File is the manifest that was rewritten, relative to the app root.
	File string `json:"file"`
}

func (o Override) String() string {
	return fmt.Sprintf("%s: %s -> %s (%s)", o.Name, o.From, o.To, o.File)
}

// ApplyOverrides rewrites the app's declared dependencies in appDir so that the ones
// named by the overrides are replaced.
//
// It exists for a mirror repackaging someone else's app: an upstream pin can be
// impossible to satisfy for the target — drive pins pycrdt==0.12.26, which publishes
// no wheel for CPython 3.14 and is a Rust extension, so it can be neither downloaded
// nor cross-built — and the repackager has no way to change the upstream source.
//
// The rewrite has to happen in the packaged tree, not only in what pip is told to
// download: `fpm install` runs `pip install -e` against the app's own manifest, so a
// package whose manifest still said ==0.12.26 would reject the 0.14.4 wheel beside it.
//
// Each override is a full requirement specifier ("pycrdt>=0.14.4"). A specifier naming
// a distribution the app does not declare is an error rather than a silent no-op, since
// it means the caller is describing an app it is not looking at.
func ApplyOverrides(appDir string, overrides []string) ([]Override, error) {
	if len(overrides) == 0 {
		return nil, nil
	}
	wanted := map[string]string{}
	for _, spec := range overrides {
		name := RequirementName(spec)
		if name == "" {
			return nil, fmt.Errorf("dependency override %q names no distribution", spec)
		}
		if previous, dup := wanted[name]; dup {
			return nil, fmt.Errorf("dependency override names %s twice (%q and %q)", name, previous, spec)
		}
		wanted[name] = strings.TrimSpace(spec)
	}

	var applied []Override
	for _, file := range []string{RequirementsFileName, PyProjectFileName} {
		path := filepath.Join(appDir, file)
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", path, err)
		}
		rewritten, changes := rewriteRequirements(string(data), wanted, file)
		if len(changes) == 0 {
			continue
		}
		if err := os.WriteFile(path, []byte(rewritten), 0o644); err != nil {
			return nil, fmt.Errorf("failed to rewrite %s: %w", path, err)
		}
		applied = append(applied, changes...)
	}

	for name, spec := range wanted {
		found := false
		for _, a := range applied {
			if a.Name == name {
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("dependency override %q does not match anything this app declares in %s or %s",
				spec, RequirementsFileName, PyProjectFileName)
		}
	}
	return applied, nil
}

// requirementQuoted matches a dependency as it appears inside a manifest: bare on a
// requirements.txt line, or quoted in a pyproject.toml array. Rewriting the text rather
// than re-serialising the file keeps every unrelated line — comments, formatting, the
// rest of the TOML — exactly as the app wrote it.
// Every class is newline-free on purpose: \s spans lines, and a trailing \s* let one
// match swallow the next requirement whole, so the second of two adjacent lines was
// never rewritten.
var requirementQuoted = regexp.MustCompile(
	`(?m)^([ \t]*)(["']?)([A-Za-z0-9][A-Za-z0-9._-]*[ \t]*(?:\[[^\]]*\])?[^"',\n#]*?)(["']?)(,?)([ \t]*(?:#[^\n]*)?)$`)

func rewriteRequirements(content string, wanted map[string]string, file string) (string, []Override) {
	var changes []Override
	out := requirementQuoted.ReplaceAllStringFunc(content, func(line string) string {
		m := requirementQuoted.FindStringSubmatch(line)
		if m == nil {
			return line
		}
		indent, openQuote, spec, closeQuote, comma, trailing := m[1], m[2], strings.TrimSpace(m[3]), m[4], m[5], m[6]
		// A quoted entry has to be quoted on both sides; an unquoted one on neither.
		if (openQuote == "") != (closeQuote == "") {
			return line
		}
		name := RequirementName(spec)
		replacement, ok := wanted[name]
		if !ok || replacement == spec {
			return line
		}
		changes = append(changes, Override{Name: name, From: spec, To: replacement, File: file})
		return indent + openQuote + replacement + closeQuote + comma + trailing
	})
	return out, changes
}

// RequirementName is the distribution a requirement specifier names, normalised the way
// pip compares names (PEP 503). "pycrdt==0.12.26" and "PyCRDT [extra] >=1" both yield
// "pycrdt".
func RequirementName(spec string) string {
	s := strings.TrimSpace(spec)
	if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, "-") {
		return ""
	}
	if i := strings.IndexAny(s, "[<>=!~; "); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return normalizeName(s)
}
