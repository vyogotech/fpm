// Package semver orders package versions by semantic precedence.
//
// It exists because publish previously advanced a package's latest_version
// with a raw string comparison:
//
//	if appVersion > remoteMeta.LatestVersion   // wrong: "1.9.0" > "1.10.0"
//
// which meant a repository that had published 1.10.0 kept reporting 1.9.0 as
// latest, and `fpm install <app>` with no pinned version installed the older
// package without saying so.
//
// Latest deliberately recomputes from the entire version set rather than
// comparing a newcomer against the stored value. That repairs metadata the old
// comparison already corrupted, instead of merely stopping the damage.
//
// Stdlib only: comparing three integers does not justify a dependency.
package semver

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// MAJOR[.MINOR[.PATCH]][-prerelease][+build], tolerating a leading "v".
var versionPattern = regexp.MustCompile(
	`^\s*v?(\d+(?:\.\d+)*)(?:-([0-9A-Za-z.\-]+))?(?:\+([0-9A-Za-z.\-]+))?\s*$`,
)

type parsed struct {
	ok          bool
	numbers     [3]int
	prerelease  []string
	isPrerelese bool
}

func parse(value string) parsed {
	match := versionPattern.FindStringSubmatch(value)
	if match == nil {
		return parsed{}
	}

	result := parsed{ok: true}
	for i, part := range strings.Split(match[1], ".") {
		if i > 2 {
			// Four-part versions are not semver but do occur; treating
			// "1.0.0.0" as 1.0.0 beats refusing to order it.
			break
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return parsed{}
		}
		result.numbers[i] = n
	}

	if match[2] != "" {
		result.prerelease = strings.Split(match[2], ".")
		result.isPrerelese = true
	}
	// match[3] is build metadata, excluded from precedence by the spec.
	return result
}

// compareIdentifiers orders one prerelease identifier against another.
// Numeric identifiers compare numerically and rank below alphanumeric ones,
// which is what puts beta.11 above beta.2 while keeping 1.0.0-1 below
// 1.0.0-alpha.
func compareIdentifiers(a, b string) int {
	aNum, aErr := strconv.Atoi(a)
	bNum, bErr := strconv.Atoi(b)

	switch {
	case aErr == nil && bErr == nil:
		return sign(aNum - bNum)
	case aErr == nil:
		return -1 // numeric ranks below alphanumeric
	case bErr == nil:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	default:
		return 0
	}
}

// Compare returns -1, 0 or 1 as a ranks below, equal to, or above b.
//
// Unparseable versions rank below every parseable one rather than causing an
// error: publishers control these strings, and one malformed version must not
// break ordering for the rest of a package's releases.
func Compare(a, b string) int {
	pa, pb := parse(a), parse(b)

	if !pa.ok || !pb.ok {
		switch {
		case pa.ok:
			return 1
		case pb.ok:
			return -1
		default:
			return strings.Compare(a, b)
		}
	}

	for i := 0; i < 3; i++ {
		if c := sign(pa.numbers[i] - pb.numbers[i]); c != 0 {
			return c
		}
	}

	// A version with no prerelease part outranks one that has any.
	if pa.isPrerelese != pb.isPrerelese {
		if pa.isPrerelese {
			return -1
		}
		return 1
	}
	if !pa.isPrerelese {
		return 0
	}

	for i := 0; i < len(pa.prerelease) && i < len(pb.prerelease); i++ {
		if c := compareIdentifiers(pa.prerelease[i], pb.prerelease[i]); c != 0 {
			return c
		}
	}
	// A shorter identifier list ranks below a longer one sharing its prefix:
	// 1.0.0-alpha < 1.0.0-alpha.1.
	return sign(len(pa.prerelease) - len(pb.prerelease))
}

// IsPrerelease reports whether a version carries a prerelease identifier.
func IsPrerelease(value string) bool {
	return parse(value).isPrerelese
}

// Sort returns the versions ordered ascending by precedence.
func Sort(versions []string) []string {
	out := append([]string(nil), versions...)
	sort.SliceStable(out, func(i, j int) bool { return Compare(out[i], out[j]) < 0 })
	return out
}

// Latest returns the highest stable version, or the highest prerelease when
// every published version is a prerelease.
//
// Prereleases are excluded by default so that a package which has published
// 1.0.0 and 1.1.0-rc.1 offers 1.0.0 for installation. Falling back when there
// is nothing else avoids hiding a package that has only ever shipped
// prereleases.
func Latest(versions []string) string {
	if len(versions) == 0 {
		return ""
	}

	best, bestPre := "", ""
	for _, version := range versions {
		if IsPrerelease(version) {
			if bestPre == "" || Compare(version, bestPre) > 0 {
				bestPre = version
			}
			continue
		}
		if best == "" || Compare(version, best) > 0 {
			best = version
		}
	}

	if best != "" {
		return best
	}
	return bestPre
}

// LatestOf is a convenience for the map shape package-metadata.json uses.
func LatestOf[T any](versions map[string]T) string {
	keys := make([]string, 0, len(versions))
	for key := range versions {
		keys = append(keys, key)
	}
	return Latest(keys)
}
