// Package mirror bulk-builds and publishes Frappe ecosystem apps.
//
// The catalog is a checked-in CSV of Frappe ecosystem repositories; mirror discovers
// release tags, packages the latest release of each major line, and publishes
// the versions the registry does not have yet. Apps from any Git repository
// (official, community, or third-party) are supported.
package mirror

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Org is the default organization namespace the mirror publishes under.
const Org = "frappe"

// Track selects how versions are discovered for an app.
const (
	TrackTags   = "tags"   // release tags, latest per major line (default)
	TrackBranch = "branch" // tip of one branch, as a prerelease pseudo-version
)

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// App is one catalog row.
type App struct {
	Slug        string
	Repo        string
	AppName     string // overrides hooks.py-derived app name when set
	Track       string
	Branch      string
	BranchMajor int
	Majors      []int // allowlist of major lines; empty means every tagged major
	BundleDeps  bool
	Enabled     bool
	// Tier orders a run: every app in a lower tier is published before a higher one
	// starts. 0 for an app that requires nothing else in the catalog.
	Tier  int
	Notes string

	// BuildScript is the absolute path of catalog/build/<slug>.sh when that
	// script exists; empty otherwise. Its contract: run in the checkout root,
	// leave a compiled_assets/ directory there for the archiver to pick up.
	BuildScript string
}

// MetadataName is the registry key the app's package metadata lives under:
// the explicit app_name override when present, the slug otherwise.
func (a App) MetadataName() string {
	if a.AppName != "" {
		return a.AppName
	}
	return a.Slug
}

// Catalog is the parsed, validated app list.
type Catalog struct {
	Apps []App
}

// catalogColumns is the exact header contract. Unknown columns are rejected
// rather than ignored so a typo ("majours") fails loudly instead of silently
// applying defaults to every row.
var catalogColumns = []string{
	"slug", "repo", "app_name", "track", "branch", "branch_major",
	"majors", "bundle_deps", "enabled", "tier", "notes",
}

// CatalogOptions configures catalog validation behavior.
type CatalogOptions struct {
	AllowThirdParty bool
}

// LoadCatalog reads and validates a catalog CSV using default options (allowing any valid git repo URL).
func LoadCatalog(path string) (*Catalog, error) {
	return LoadCatalogWithOptions(path, CatalogOptions{AllowThirdParty: true})
}

// frappeOrgRepo matches a repository under the frappe GitHub organisation, in either
// URL form a catalog row may use.
var frappeOrgRepo = regexp.MustCompile(`^(?:https?://github\.com/|git@github\.com:)frappe/`)

// IsFrappeOrg reports whether a catalog repo URL belongs to the frappe organisation.
// Everything else is a third-party listing.
func IsFrappeOrg(repo string) bool {
	return frappeOrgRepo.MatchString(strings.TrimSpace(repo))
}

// LoadCatalogWithOptions reads and validates a catalog CSV with explicit options.
func LoadCatalogWithOptions(path string, opts CatalogOptions) (*Catalog, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open catalog %s: %w", path, err)
	}
	defer f.Close()

	reader := csv.NewReader(f)
	reader.TrimLeadingSpace = true
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("failed to parse catalog %s: %w", path, err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("catalog %s is empty; expected a header row %v", path, catalogColumns)
	}

	col, err := headerIndex(records[0])
	if err != nil {
		return nil, fmt.Errorf("catalog %s: %w", path, err)
	}

	buildDir := filepath.Join(filepath.Dir(path), "build")
	catalog := &Catalog{}
	seen := map[string]bool{}

	for line, record := range records[1:] {
		get := func(name string) string { return strings.TrimSpace(record[col[name]]) }

		app := App{
			Slug:       get("slug"),
			Repo:       get("repo"),
			AppName:    get("app_name"),
			Track:      get("track"),
			Branch:     get("branch"),
			Notes:      get("notes"),
			BundleDeps: true,
			Enabled:    true,
		}

		rowErr := func(format string, args ...any) error {
			return fmt.Errorf("catalog %s row %d (%s): %s", path, line+2, app.Slug, fmt.Sprintf(format, args...))
		}

		if !slugPattern.MatchString(app.Slug) {
			return nil, rowErr("slug must match %s", slugPattern)
		}
		if seen[app.Slug] {
			return nil, rowErr("duplicate slug")
		}
		seen[app.Slug] = true

		if !strings.HasPrefix(app.Repo, "https://") && !strings.HasPrefix(app.Repo, "http://") && !strings.HasPrefix(app.Repo, "git@") {
			return nil, rowErr("repo must be a valid git URL (https://, http://, or git@)")
		}

		switch app.Track {
		case "":
			app.Track = TrackTags
		case TrackTags, TrackBranch:
		default:
			return nil, rowErr("track must be %q or %q", TrackTags, TrackBranch)
		}
		if app.Track == TrackBranch && app.Branch == "" {
			return nil, rowErr("track=branch requires a branch")
		}
		if app.Track == TrackTags && app.Branch != "" {
			return nil, rowErr("branch is only valid with track=branch")
		}

		// tier orders a run: everything in tier 0 is published before tier 1 starts,
		// so an app whose required_apps name another catalog entry can resolve and
		// reference them. hrms needs erpnext in the registry to pin it at packaging
		// time and to hang its OCI referrers subject off; webshop needs erpnext and
		// payments. Default 0: most apps depend on nothing in the catalog.
		if raw := get("tier"); raw != "" {
			app.Tier, err = strconv.Atoi(raw)
			if err != nil || app.Tier < 0 {
				return nil, rowErr("tier must be a non-negative integer")
			}
		}

		if raw := get("branch_major"); raw != "" {
			app.BranchMajor, err = strconv.Atoi(raw)
			if err != nil || app.BranchMajor < 0 {
				return nil, rowErr("branch_major must be a non-negative integer")
			}
		}

		if raw := get("majors"); raw != "" {
			for _, part := range strings.Split(raw, ";") {
				major, err := strconv.Atoi(strings.TrimSpace(part))
				if err != nil || major < 0 {
					return nil, rowErr("majors must be semicolon-separated non-negative integers")
				}
				app.Majors = append(app.Majors, major)
			}
		}

		if app.BundleDeps, err = parseBool(get("bundle_deps"), true); err != nil {
			return nil, rowErr("bundle_deps: %v", err)
		}
		if app.Enabled, err = parseBool(get("enabled"), true); err != nil {
			return nil, rowErr("enabled: %v", err)
		}

		// The mirror publishes the frappe organisation's own apps. A third-party
		// listing is disabled rather than dropped, so `--apps <slug>` still says why
		// it will not build instead of "not in the catalog".
		if !opts.AllowThirdParty && !IsFrappeOrg(app.Repo) {
			app.Enabled = false
			app.Notes = strings.TrimSpace(app.Notes + " (not in the frappe org; excluded by --allow-third-party=false)")
		}

		script := filepath.Join(buildDir, app.Slug+".sh")
		if info, err := os.Stat(script); err == nil && !info.IsDir() {
			app.BuildScript = script
		}

		catalog.Apps = append(catalog.Apps, app)
	}

	return catalog, nil
}

// Tiers returns the distinct tiers present among the given apps, ascending.
func Tiers(apps []App) []int {
	seen := map[int]bool{}
	var out []int
	for _, app := range apps {
		if !seen[app.Tier] {
			seen[app.Tier] = true
			out = append(out, app.Tier)
		}
	}
	sort.Ints(out)
	return out
}

// InTier returns the apps belonging to one tier.
func InTier(apps []App, tier int) []App {
	var out []App
	for _, app := range apps {
		if app.Tier == tier {
			out = append(out, app)
		}
	}
	return out
}

// Enabled returns the enabled apps, restricted to the given slugs when the
// filter is non-empty. A filter naming an unknown slug is an error: a typo in
// --apps must not silently mirror nothing.
func (c *Catalog) Enabled(filter []string) ([]App, error) {
	bySlug := map[string]App{}
	for _, app := range c.Apps {
		bySlug[app.Slug] = app
	}

	if len(filter) == 0 {
		var out []App
		for _, app := range c.Apps {
			if app.Enabled {
				out = append(out, app)
			}
		}
		return out, nil
	}

	var out []App
	for _, slug := range filter {
		slug = strings.TrimSpace(slug)
		app, ok := bySlug[slug]
		if !ok {
			return nil, fmt.Errorf("app %q is not in the catalog", slug)
		}
		if !app.Enabled {
			return nil, fmt.Errorf("app %q is disabled in the catalog: %s", slug, app.Notes)
		}
		out = append(out, app)
	}
	return out, nil
}

func headerIndex(header []string) (map[string]int, error) {
	col := map[string]int{}
	for i, name := range header {
		name = strings.TrimSpace(name)
		col[name] = i
	}
	for name := range col {
		if !contains(catalogColumns, name) {
			return nil, fmt.Errorf("unknown column %q; expected %v", name, catalogColumns)
		}
	}
	for _, name := range catalogColumns {
		if _, ok := col[name]; !ok {
			return nil, fmt.Errorf("missing column %q; expected %v", name, catalogColumns)
		}
	}
	return col, nil
}

func parseBool(raw string, def bool) (bool, error) {
	switch raw {
	case "":
		return def, nil
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("must be true, false, or empty (got %q)", raw)
	}
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
