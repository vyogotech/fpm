package semver

import (
	"fmt"
	"strings"
)

// Constraint accepts a set of versions rather than one.
//
// `fpm package` used to record every required app as an exact pin, so a bench
// that had moved on by a patch release could not install a package built a day
// earlier, and two apps depending on the same app at different pins could not be
// co-installed at all. A constraint records what the package actually needs — a
// release line — while the exact version it was built against stays recorded
// separately for audit.
//
// The syntax is the intersection (comma = AND) of comparators:
//
//	>=16.0.0,<17.0.0     the v16 line
//	==16.30.0            exactly one version
//	16.30.0              bare version, same as ==
//	*                    any version
//
// Operators: ==, =, !=, >, >=, <, <=. Ordering is semver.Compare, so a
// prerelease ranks below its release and "16.0" equals "16.0.0".
type Constraint struct {
	raw     string
	clauses []clause
}

type clause struct {
	op      string
	version string
}

var comparators = []string{">=", "<=", "==", "!=", ">", "<", "="}

// ParseConstraint parses a comma-separated comparator list. An empty string (or
// "*") yields the zero Constraint, which matches every version.
func ParseConstraint(spec string) (Constraint, error) {
	trimmed := strings.TrimSpace(spec)
	if trimmed == "" || trimmed == "*" {
		return Constraint{}, nil
	}

	c := Constraint{raw: trimmed}
	for _, part := range strings.Split(trimmed, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		op, version := "==", part
		for _, candidate := range comparators {
			if strings.HasPrefix(part, candidate) {
				op = candidate
				version = strings.TrimSpace(strings.TrimPrefix(part, candidate))
				break
			}
		}
		if op == "=" {
			op = "=="
		}
		if version == "" {
			return Constraint{}, fmt.Errorf("version constraint %q: %q has an operator but no version", spec, part)
		}
		if !Valid(version) {
			return Constraint{}, fmt.Errorf("version constraint %q: %q is not a version", spec, version)
		}
		c.clauses = append(c.clauses, clause{op: op, version: version})
	}
	if len(c.clauses) == 0 {
		return Constraint{}, nil
	}
	return c, nil
}

// MustParseConstraint is ParseConstraint for constraints this package builds
// itself, where a parse failure is a programming error.
func MustParseConstraint(spec string) Constraint {
	c, err := ParseConstraint(spec)
	if err != nil {
		panic(err)
	}
	return c
}

// Any reports whether the constraint accepts every version, which is what an
// empty spec means.
func (c Constraint) Any() bool { return len(c.clauses) == 0 }

// String returns the constraint as written.
func (c Constraint) String() string {
	if c.Any() {
		return ""
	}
	return c.raw
}

// Matches reports whether a version satisfies every clause. An unparseable
// version satisfies nothing, since it cannot be ordered against the bounds.
//
// A prerelease is only accepted when a clause names a prerelease of the same
// release, which is node-semver's rule and the one this ecosystem needs: an
// erpnext "17.0.0-dev" lying around in a store must not satisfy the v16 line
// merely because it sorts below 17.0.0, while a "0.0.0-git.<date>"
// pseudo-version must still satisfy the line ">=0.0.0-0,<1.0.0" that another
// package pinned it to.
func (c Constraint) Matches(version string) bool {
	if c.Any() {
		return true
	}
	if !Valid(version) {
		return false
	}
	if IsPrerelease(version) && !c.allowsPrerelease(version) {
		return false
	}
	for _, cl := range c.clauses {
		cmp := Compare(version, cl.version)
		ok := false
		switch cl.op {
		case "==":
			ok = cmp == 0
		case "!=":
			ok = cmp != 0
		case ">":
			ok = cmp > 0
		case ">=":
			ok = cmp >= 0
		case "<":
			ok = cmp < 0
		case "<=":
			ok = cmp <= 0
		}
		if !ok {
			return false
		}
	}
	return true
}

// allowsPrerelease reports whether any clause opts into prereleases of the same
// release as version.
func (c Constraint) allowsPrerelease(version string) bool {
	for _, cl := range c.clauses {
		if cl.op == "!=" {
			continue
		}
		if IsPrerelease(cl.version) && sameRelease(cl.version, version) {
			return true
		}
	}
	return false
}

// sameRelease reports whether two versions share a major.minor.patch tuple,
// ignoring any prerelease part.
func sameRelease(a, b string) bool {
	pa, pb := parse(a), parse(b)
	return pa.ok && pb.ok && pa.numbers == pb.numbers
}

// Select returns the version to use out of those available: the highest stable
// one that satisfies the constraint, or the highest prerelease when only
// prereleases do. It returns "" when nothing satisfies it.
func (c Constraint) Select(versions []string) string {
	var matching []string
	for _, v := range versions {
		if c.Matches(v) {
			matching = append(matching, v)
		}
	}
	return Latest(matching)
}

// MajorLine renders the release line a version belongs to, e.g. 16.30.0 ->
// ">=16.0.0-0,<17.0.0". It is the default constraint `fpm package` records for a
// resolved requirement: within an app's major version Frappe apps stay
// co-installable, so a patch upgrade of erpnext must not invalidate every
// package built against it.
//
// The lower bound carries the "-0" prerelease so that a prerelease of the line
// (16.0.0-beta.1, or a 0.0.0-git.<date> pseudo-version) is inside its own line
// rather than below it.
//
// An unparseable version has no line; MajorLine returns "" and the caller keeps
// the exact pin.
func MajorLine(version string) string {
	major, ok := Major(version)
	if !ok {
		return ""
	}
	return fmt.Sprintf(">=%d.0.0-0,<%d.0.0", major, major+1)
}

// SplitRequirement splits an "org/app<spec>" requirement — as `fpm package
// --requires` takes it — into its name and constraint parts. The org is
// optional; the spec is optional and may be any constraint syntax:
//
//	frappe/erpnext==16.30.0
//	frappe/erpnext>=16.0.0,<17.0.0
//	erpnext
func SplitRequirement(requirement string) (name, spec string) {
	trimmed := strings.TrimSpace(requirement)
	if i := strings.IndexAny(trimmed, "=<>!~^"); i >= 0 {
		return strings.TrimSpace(trimmed[:i]), strings.TrimSpace(trimmed[i:])
	}
	return trimmed, ""
}

// ExactVersion returns the single version a constraint pins to, if it pins to
// exactly one. A recorded "==16.30.0" is still an exact pin and callers that
// need a concrete version (fetching from a repository, say) can use it.
func (c Constraint) ExactVersion() (string, bool) {
	if len(c.clauses) != 1 || c.clauses[0].op != "==" {
		return "", false
	}
	return c.clauses[0].version, true
}
