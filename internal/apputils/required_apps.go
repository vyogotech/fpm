package apputils

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// requiredAppsAssignment finds the start of a `required_apps = [` assignment. Only the
// opening bracket is matched here because the list literal may span several lines.
var requiredAppsAssignment = regexp.MustCompile(`(?m)^\s*required_apps\s*=\s*\[`)

// quotedString extracts one quoted Python string literal.
var quotedString = regexp.MustCompile(`"([^"]*)"|'([^']*)'`)

// GetRequiredAppsFromHooks reads the `required_apps` list an app declares in hooks.py
// and returns the entries verbatim, in declaration order.
//
// Frappe's own reader (`frappe.get_hooks`) imports the module, which fpm cannot do
// without a bench. bench's reader (`bench.utils.app.required_apps_from_hooks`) regexes
// the assignment line and `ast.literal_eval`s it, which only works for a single-line
// list. This parser handles both single- and multi-line list literals, ignores
// comments inside the list, and treats a missing assignment as "no requirements".
func GetRequiredAppsFromHooks(hooksFilePath string) ([]string, error) {
	data, err := os.ReadFile(hooksFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("hooks file not found at %s: %w", hooksFilePath, err)
		}
		return nil, fmt.Errorf("failed to read hooks file %s: %w", hooksFilePath, err)
	}
	return ParseRequiredApps(string(data))
}

// ParseRequiredApps extracts the `required_apps` entries from hooks.py source text.
func ParseRequiredApps(source string) ([]string, error) {
	loc := requiredAppsAssignment.FindStringIndex(source)
	if loc == nil {
		return nil, nil
	}

	// Walk from just after the opening bracket to its matching close, skipping
	// string contents and comments so brackets inside them do not confuse the count.
	rest := source[loc[1]:]
	depth := 1
	end := -1
	inString := byte(0)
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		switch {
		case inString != 0:
			if c == '\\' {
				i++
			} else if c == inString {
				inString = 0
			}
		case c == '"' || c == '\'':
			inString = c
		case c == '#':
			for i < len(rest) && rest[i] != '\n' {
				i++
			}
		case c == '[':
			depth++
		case c == ']':
			depth--
			if depth == 0 {
				end = i
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		return nil, fmt.Errorf("required_apps list in hooks.py is not terminated")
	}

	body := stripPythonComments(rest[:end])
	var apps []string
	for _, m := range quotedString.FindAllStringSubmatch(body, -1) {
		entry := m[1]
		if entry == "" {
			entry = m[2]
		}
		entry = strings.TrimSpace(entry)
		if entry != "" {
			apps = append(apps, entry)
		}
	}
	return apps, nil
}

// stripPythonComments drops `# ...` to end of line outside string literals.
func stripPythonComments(s string) string {
	var b strings.Builder
	inString := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inString != 0:
			b.WriteByte(c)
			if c == '\\' && i+1 < len(s) {
				i++
				b.WriteByte(s[i])
			} else if c == inString {
				inString = 0
			}
		case c == '"' || c == '\'':
			inString = c
			b.WriteByte(c)
		case c == '#':
			for i < len(s) && s[i] != '\n' {
				i++
			}
			b.WriteByte('\n')
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// ParseRequiredAppName reduces a `required_apps` entry to the bare app name, exactly
// as frappe.installer.parse_required_app_name does. Entries can be `erpnext`,
// `frappe/erpnext`, a git URL, or any of those with an `@branch` suffix.
func ParseRequiredAppName(requirement string) string {
	name := strings.TrimRight(strings.TrimSpace(requirement), "/")
	name = strings.SplitN(name, "#", 2)[0]
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	if i := strings.LastIndex(name, ":"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.SplitN(name, "@", 2)[0]
	return strings.TrimSuffix(name, ".git")
}

// ParseRequiredAppOrg returns the organisation a `required_apps` entry names, when
// it names one: `frappe/erpnext` and `https://github.com/frappe/erpnext.git` both
// yield "frappe"; a bare `erpnext` yields "".
func ParseRequiredAppOrg(requirement string) string {
	entry := strings.TrimRight(strings.TrimSpace(requirement), "/")
	entry = strings.SplitN(entry, "#", 2)[0]
	if i := strings.Index(entry, "://"); i >= 0 {
		// URL: drop scheme and host, leaving org/app.
		entry = entry[i+3:]
	} else if m := scpPrefix.FindStringIndex(entry); m != nil {
		// scp-style git@host:org/app
		entry = entry[m[1]:]
	}
	// Only now strip an @branch suffix, so the user@host of an scp URL is not
	// mistaken for it.
	entry = strings.SplitN(entry, "@", 2)[0]
	parts := strings.Split(entry, "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2]
	}
	return ""
}

// scpPrefix matches the user@host: prefix of an scp-style git URL.
var scpPrefix = regexp.MustCompile(`^[^/@:]+@[^/:]+:`)
