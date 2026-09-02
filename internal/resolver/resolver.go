// Package resolver pins the Frappe apps an app requires (hooks.py `required_apps`)
// to exact packages, and checks that a pinned closure is present in the local FPM
// store before an offline install starts.
//
// Requirements are never bundled into a package: a common dependency such as erpnext
// would otherwise be duplicated inside every custom app that needs it. Instead
// `fpm package` records `org/app==version` pins, and `fpm install` refuses to start
// unless every pin — transitively — is already in the local store, since the target
// bench has no network to fetch from.
package resolver

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"fpm/internal/apputils"
	"fpm/internal/config"
	"fpm/internal/gitutils"
	"fpm/internal/metadata"
	"fpm/internal/repository"
	"fpm/internal/semver"
)

// FrappeAppName is the framework itself. It is never a package dependency: every
// bench provides it, and frappe's own installer treats it as terminal.
const FrappeAppName = "frappe"

// ErrUnresolved wraps every "required app could not be pinned" failure.
var ErrUnresolved = errors.New("required app could not be resolved")

// ErrMissing wraps every "required app is not in the local store" failure.
var ErrMissing = errors.New("required app missing from local FPM store")

// Options controls where requirements are resolved from.
type Options struct {
	Cfg *config.FPMConfig
	// Remote enables querying configured repositories when the local store has
	// no candidate. Packaging runs with network access, so it defaults on there;
	// installs never set it.
	Remote bool
	// Repos limits resolution to the named configured repositories, tried in the
	// order given. It is exclusive: neither the packaging host's store nor the bench
	// answers a requirement when it is set, because a pin taken from ambient machine
	// state while the caller asked for a repository is exactly what makes builds
	// unreproducible. More than one names every backend a build publishes to, so a
	// pin is found wherever it landed.
	Repos []string
	// BenchPath, when set, lets an app already present in that bench (installed
	// outside fpm — bench get-app, or baked into an image) satisfy a requirement,
	// pinned to the version its module declares. Consulted after the local store
	// and before repositories, and not at all when Repo names one.
	BenchPath string
	// SkipLocalStore keeps the packaging host's own store out of resolution. The
	// store holds whatever was packaged on this machine, whenever that happened,
	// so a package that pins from it depends on when and where it was built.
	// `fpm package` sets this for production packages unless the packager opts
	// back in.
	SkipLocalStore bool
	// Overrides pin requirements outright, before any source is consulted. They
	// come from `fpm package --requires org/app==version` (or a range), and are
	// matched by app name, and by org when both name and override carry one.
	Overrides []metadata.RequiredApp
	// ReleaseLine records each resolved pin as its major release line
	// (">=16.0.0-0,<17.0.0") rather than as one exact version, so a patch-level
	// upgrade of a dependency does not invalidate every package built against it.
	// The exact version resolved is still recorded, as what the package was built
	// against.
	ReleaseLine bool

	// DefaultOrg is the organisation an unqualified requirement is looked up under
	// when the repository publishes no index to answer "who publishes this app". An
	// OCI registry never does. Packaging passes the org of the package being built,
	// which is the right guess: hrms requiring "erpnext" means the erpnext published
	// alongside it, not some other organisation's.
	DefaultOrg string

	// Seams for tests; nil uses the real repository client.
	fetchMetadata func(repo config.RepositoryConfig, org, app string) (*repository.PackageMetadata, bool, error)
	fetchIndex    func(repo config.RepositoryConfig) (*repository.RepositoryIndex, bool, error)
}

// ResolveRequiredApps pins each hooks.py `required_apps` entry. frappe is skipped.
// Every entry is attempted so the error, when there is one, lists all failures.
func ResolveRequiredApps(entries []string, opts Options) ([]metadata.RequiredApp, error) {
	if opts.Cfg == nil {
		return nil, fmt.Errorf("resolver: configuration is required")
	}
	var pins []metadata.RequiredApp
	var failures []string
	seen := map[string]bool{}
	for _, entry := range entries {
		name := apputils.ParseRequiredAppName(entry)
		if name == "" || name == FrappeAppName || seen[name] {
			continue
		}
		seen[name] = true
		org := apputils.ParseRequiredAppOrg(entry)

		if pin, ok := opts.override(org, name); ok {
			pin.Requirement = entry
			pins = append(pins, pin)
			continue
		}

		pin, err := resolveOne(name, org, opts)
		if err != nil {
			failures = append(failures, fmt.Sprintf("  %s (from required_apps entry %q): %v", name, entry, err))
			continue
		}
		if opts.ReleaseLine {
			pin.VersionSpec = semver.MajorLine(pin.Version)
		}
		pin.Requirement = entry
		pins = append(pins, pin)
	}
	if len(failures) > 0 {
		return pins, fmt.Errorf("%w:\n%s\nPublish the missing app(s) to the repository (or package them into the local store) first, "+
			"then package this app again", ErrUnresolved, strings.Join(failures, "\n"))
	}
	return pins, nil
}

// override returns the pin the caller stated for this requirement, if any.
func (o Options) override(org, name string) (metadata.RequiredApp, bool) {
	for _, ov := range o.Overrides {
		if ov.Name != name {
			continue
		}
		if ov.Org != "" && org != "" && ov.Org != org {
			continue
		}
		pin := ov
		if pin.Org == "" {
			pin.Org = org
		}
		pin.ResolvedFrom = OverrideSource
		return pin, true
	}
	return metadata.RequiredApp{}, false
}

// OverrideSource is the ResolvedFrom of a pin the packager stated outright.
const OverrideSource = "flag:--requires"

// UnmatchedOverrides names the overrides that matched no required_apps entry,
// which is nearly always a typo in the flag rather than a deliberate no-op.
func UnmatchedOverrides(entries []string, overrides []metadata.RequiredApp) []string {
	declared := map[string]bool{}
	for _, entry := range entries {
		if name := apputils.ParseRequiredAppName(entry); name != "" {
			declared[name] = true
		}
	}
	var unused []string
	for _, ov := range overrides {
		if !declared[ov.Name] {
			unused = append(unused, ov.Identifier())
		}
	}
	return unused
}

func resolveOne(name, org string, opts Options) (metadata.RequiredApp, error) {
	// A named repository is exclusive: it is the caller saying where pins come
	// from, and consulting the machine's own store first would quietly override
	// that with whatever this machine happens to hold.
	exclusiveRepo := len(opts.Repos) > 0

	// The local store is what an offline install can use, but it is also ambient
	// machine state, so production packaging opts out of it.
	if !exclusiveRepo && !opts.SkipLocalStore {
		if found, ok, err := latestInStore(opts.Cfg.AppsBasePath, org, name); err != nil {
			return metadata.RequiredApp{}, err
		} else if ok {
			return found, nil
		}
	}

	if opts.BenchPath != "" && !exclusiveRepo {
		if version, present := BenchAppVersion(opts.BenchPath, name); present {
			benchOrg := org
			if benchOrg == "" {
				if gitOrg, _, err := gitutils.GetGitRemoteOriginInfo(filepath.Join(opts.BenchPath, "apps", name)); err == nil {
					benchOrg = gitOrg
				}
			}
			return metadata.RequiredApp{
				Name: name, Org: benchOrg, Version: version, ResolvedFrom: "bench:" + opts.BenchPath,
			}, nil
		}
	}

	if !opts.Remote {
		return metadata.RequiredApp{}, fmt.Errorf("not found in %s", strings.Join(opts.sourcesConsulted(), " or "))
	}

	repos := config.ListRepositories(opts.Cfg)
	if exclusiveRepo {
		repos = repos[:0]
		for _, name := range opts.Repos {
			r, ok := config.GetRepository(opts.Cfg, name)
			if !ok {
				return metadata.RequiredApp{}, fmt.Errorf("repository %q is not configured", name)
			}
			repos = append(repos, r)
		}
	}
	if len(repos) == 0 {
		return metadata.RequiredApp{}, fmt.Errorf("no repositories are configured to resolve it from (use 'fpm repo add', "+
			"pin it with --requires %s==<version>, or point at a bench with --bench-path)", name)
	}

	var tried []string
	for _, repo := range repos {
		pin, ok, err := latestInRepo(repo, org, name, opts)
		if err != nil {
			tried = append(tried, fmt.Sprintf("%s: %v", repo.Name, err))
			continue
		}
		if ok {
			return pin, nil
		}
		tried = append(tried, fmt.Sprintf("%s: not published", repo.Name))
	}
	return metadata.RequiredApp{}, fmt.Errorf("not found in %s (%s)",
		strings.Join(opts.sourcesConsulted(), " or "), strings.Join(tried, "; "))
}

// sourcesConsulted names, for an error message, the places resolution actually
// looked — which the flags change, so a bare "not in the local store" would be
// misleading.
func (o Options) sourcesConsulted() []string {
	if len(o.Repos) > 0 {
		return []string{"repository " + strings.Join(o.Repos, " or ")}
	}
	var sources []string
	if !o.SkipLocalStore {
		sources = append(sources, "local FPM store "+o.Cfg.AppsBasePath)
	}
	if o.BenchPath != "" {
		sources = append(sources, "bench "+o.BenchPath)
	}
	if o.Remote {
		sources = append(sources, "any configured repository")
	}
	if len(sources) == 0 {
		return []string{"any enabled source (all sources were disabled)"}
	}
	return sources
}

// BenchAppVersion reports whether a bench has an app (apps/<name>/<name>/hooks.py)
// and the version its module declares (__version__ in apps/<name>/<name>/__init__.py,
// "" when absent).
func BenchAppVersion(benchPath, name string) (version string, present bool) {
	module := filepath.Join(benchPath, "apps", name, name)
	if _, err := os.Stat(filepath.Join(module, "hooks.py")); err != nil {
		return "", false
	}
	data, err := os.ReadFile(filepath.Join(module, "__init__.py"))
	if err != nil {
		return "", true
	}
	if m := versionAssignment.FindSubmatch(data); m != nil {
		return strings.TrimSpace(string(m[1])), true
	}
	return "", true
}

var versionAssignment = regexp.MustCompile(`(?m)^\s*__version__\s*=\s*["']([^"']+)["']`)

// StoreVersions lists the versions of org/app present in the local store, i.e. the
// version directories that hold an extracted app module.
func StoreVersions(appsBasePath, org, app string) []string {
	entries, err := os.ReadDir(filepath.Join(appsBasePath, org, app))
	if err != nil {
		return nil
	}
	var versions []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if InStore(appsBasePath, org, app, e.Name()) {
			versions = append(versions, e.Name())
		}
	}
	return semver.Sort(versions)
}

// InStore reports whether an exact version is extracted in the local store.
func InStore(appsBasePath, org, app, version string) bool {
	hooks := filepath.Join(appsBasePath, org, app, version, app, "hooks.py")
	_, err := os.Stat(hooks)
	return err == nil
}

// StoreOrgs lists the orgs under which an app name exists in the local store.
func StoreOrgs(appsBasePath, app string) []string {
	entries, err := os.ReadDir(appsBasePath)
	if err != nil {
		return nil
	}
	var orgs []string
	for _, e := range entries {
		if e.IsDir() && len(StoreVersions(appsBasePath, e.Name(), app)) > 0 {
			orgs = append(orgs, e.Name())
		}
	}
	sort.Strings(orgs)
	return orgs
}

func latestInStore(appsBasePath, org, name string) (metadata.RequiredApp, bool, error) {
	if org == "" {
		orgs := StoreOrgs(appsBasePath, name)
		switch len(orgs) {
		case 0:
			return metadata.RequiredApp{}, false, nil
		case 1:
			org = orgs[0]
		default:
			return metadata.RequiredApp{}, false, fmt.Errorf("ambiguous: %s exists under several orgs in the local store (%s); qualify it as org/%s in required_apps",
				name, strings.Join(orgs, ", "), name)
		}
	}
	versions := StoreVersions(appsBasePath, org, name)
	if len(versions) == 0 {
		return metadata.RequiredApp{}, false, nil
	}
	return metadata.RequiredApp{
		Name: name, Org: org, Version: semver.Latest(versions), ResolvedFrom: "local-store",
	}, true, nil
}

func latestInRepo(repo config.RepositoryConfig, org, name string, opts Options) (metadata.RequiredApp, bool, error) {
	fetchMeta := opts.fetchMetadata
	fetchIdx := opts.fetchIndex
	if fetchMeta == nil || fetchIdx == nil {
		creds, err := repository.ResolveCredentials(repo.Name, repo.Username, true)
		if err != nil {
			return metadata.RequiredApp{}, false, err
		}
		client, err := repository.NewClient(repo.URL, creds, 30*time.Second)
		if err != nil {
			return metadata.RequiredApp{}, false, err
		}
		if fetchMeta == nil {
			fetchMeta = func(r config.RepositoryConfig, o, a string) (*repository.PackageMetadata, bool, error) {
				return repository.FetchRemotePackageMetadataForRepo(r, o, a, client)
			}
		}
		if fetchIdx == nil {
			fetchIdx = func(r config.RepositoryConfig) (*repository.RepositoryIndex, bool, error) {
				return repository.FetchRepositoryIndexForRepo(r, client)
			}
		}
	}

	if org == "" {
		// Only the index can answer "which org publishes this app name".
		idx, found, err := fetchIdx(repo)
		if err != nil {
			return metadata.RequiredApp{}, false, err
		}
		if !found || idx == nil {
			// No index — an OCI registry has none at all. Fall back to the org the
			// caller says to assume rather than refusing outright.
			if opts.DefaultOrg == "" {
				return metadata.RequiredApp{}, false, fmt.Errorf("publishes no index, so an unqualified %q cannot be looked up; qualify it as org/%s", name, name)
			}
			org = opts.DefaultOrg
		} else {
			var orgs []string
			for _, e := range idx.Packages {
				if e.AppName == name {
					orgs = append(orgs, e.Org)
				}
			}
			switch len(orgs) {
			case 0:
				// The index does not list it; the default org is still worth a try,
				// since an index can lag what the registry actually holds.
				if opts.DefaultOrg == "" {
					return metadata.RequiredApp{}, false, nil
				}
				org = opts.DefaultOrg
			case 1:
				org = orgs[0]
			default:
				return metadata.RequiredApp{}, false, fmt.Errorf("ambiguous: %s is published under several orgs (%s); qualify it as org/%s",
					name, strings.Join(orgs, ", "), name)
			}
		}
	}

	meta, found, err := fetchMeta(repo, org, name)
	if err != nil {
		return metadata.RequiredApp{}, false, err
	}
	if !found || meta == nil {
		return metadata.RequiredApp{}, false, nil
	}
	version := meta.LatestVersion
	if version == "" {
		version = semver.LatestOf(meta.Versions)
	}
	if version == "" {
		return metadata.RequiredApp{}, false, nil
	}
	return metadata.RequiredApp{
		Name: name, Org: org, Version: version,
		ResolvedFrom: "repo:" + repo.Name, ResolvedFromURL: repo.URL,
	}, true, nil
}

// Missing describes one required app that an offline install cannot satisfy.
type Missing struct {
	App        metadata.RequiredApp
	RequiredBy string
	Reason     string
}

func (m Missing) String() string {
	return fmt.Sprintf("%s (required by %s): %s", m.App.Identifier(), m.RequiredBy, m.Reason)
}

// ClosureEntry is one app in the transitive requirement closure.
type ClosureEntry struct {
	App        metadata.RequiredApp
	RequiredBy string
	// Present reports whether the pinned version is extracted in the local store,
	// or provided by the bench.
	Present bool
	// StorePath is the version directory in the store when Present (or the app's
	// directory in the bench when ProvidedByBench).
	StorePath string
	// ProvidedByBench is set when the requirement is satisfied by an app already in
	// the bench rather than by a package in the store. Such apps were installed by
	// other means (bench get-app, an image) and their own requirements are the
	// bench's concern, so they are not recursed into.
	ProvidedByBench bool
}

// CheckLocalClosure walks the transitive `required_apps` closure of apps, verifying
// each pinned version is extracted in the local FPM store and recursing into that
// package's own recorded requirements. It never fetches anything.
//
// The closure is returned in dependency order (deepest first), which is also a
// valid install order. Missing entries are collected rather than returned on the
// first failure, so the error names everything that has to be provided.
func CheckLocalClosure(appsBasePath string, apps []metadata.RequiredApp, requiredBy string) ([]ClosureEntry, []Missing, error) {
	return CheckClosure(appsBasePath, "", apps, requiredBy)
}

// CheckClosure is CheckLocalClosure that also accepts a requirement provided by the
// bench at benchPath (when non-empty): an app present there whose declared version
// matches the pin (or an unpinned requirement) is satisfied without a package. A
// version mismatch is reported as missing, naming both versions.
func CheckClosure(appsBasePath, benchPath string, apps []metadata.RequiredApp, requiredBy string) ([]ClosureEntry, []Missing, error) {
	var closure []ClosureEntry
	var missing []Missing
	visited := map[string]bool{}

	var walk func(list []metadata.RequiredApp, by string, depth int) error
	walk = func(list []metadata.RequiredApp, by string, depth int) error {
		if depth > 32 {
			return fmt.Errorf("required_apps chain deeper than 32 levels from %s; refusing to loop", requiredBy)
		}
		for _, app := range list {
			if app.Name == FrappeAppName {
				continue
			}
			org := app.Org
			if org == "" {
				orgs := StoreOrgs(appsBasePath, app.Name)
				if len(orgs) == 1 {
					org = orgs[0]
				} else if len(orgs) > 1 {
					missing = append(missing, Missing{App: app, RequiredBy: by,
						Reason: fmt.Sprintf("ambiguous: present under orgs %s", strings.Join(orgs, ", "))})
					continue
				}
			}
			key := org + "/" + app.Name
			if visited[key] {
				continue
			}
			visited[key] = true

			// A requirement carrying a constraint is satisfied by any version in
			// the store that meets it, preferring the one the package was built
			// against; an exact pin (or an old package with no constraint at all)
			// keeps its historic meaning.
			available := StoreVersions(appsBasePath, org, app.Name)
			version := app.Version
			switch {
			case app.VersionSpec != "":
				if version == "" || !InStore(appsBasePath, org, app.Name, version) {
					version = app.Constraint().Select(available)
				}
			case version == "":
				// An unpinned requirement (packaged by an older fpm) accepts the
				// latest version present.
				version = semver.Latest(available)
			}
			present := org != "" && version != "" && InStore(appsBasePath, org, app.Name, version)
			resolved := metadata.RequiredApp{Name: app.Name, Org: org, Version: version, Requirement: app.Requirement}
			storePath := filepath.Join(appsBasePath, org, app.Name, version)

			if !present && benchPath != "" {
				if benchVersion, inBench := BenchAppVersion(benchPath, app.Name); inBench {
					if app.Accepts(benchVersion) {
						resolved.Version = benchVersion
						closure = append(closure, ClosureEntry{App: resolved, RequiredBy: by, Present: true,
							StorePath: filepath.Join(benchPath, "apps", app.Name), ProvidedByBench: true})
						continue
					}
					unsatisfied := app.Version
					if app.VersionSpec != "" {
						unsatisfied = app.VersionSpec
					}
					missing = append(missing, Missing{App: unresolvedPin(app, org), RequiredBy: by,
						Reason: fmt.Sprintf("bench has %s version %q but the package requires %q, and no package satisfying that is in the local FPM store",
							app.Name, benchVersion, unsatisfied)})
					continue
				}
			}

			if !present {
				want := app.VersionSpec
				if want == "" {
					want = version
				}
				var reason string
				switch {
				case org == "":
					reason = "not in local FPM store under any org"
				case len(available) > 0:
					reason = fmt.Sprintf("no version satisfying %s in local FPM store (have %s)", want, strings.Join(available, ", "))
				case want != "":
					reason = fmt.Sprintf("no version satisfying %s in local FPM store (%s holds none)",
						want, filepath.Join(appsBasePath, org, app.Name))
				default:
					reason = fmt.Sprintf("not in local FPM store (expected %s)", storePath)
				}
				if benchPath != "" {
					reason += ", nor in bench " + benchPath
				}
				missing = append(missing, Missing{App: unresolvedPin(app, org), RequiredBy: by, Reason: reason})
				continue
			}

			// Recurse into what this dependency itself requires, then record it —
			// so the closure comes out deepest first.
			depMeta, err := metadata.LoadAppMetadata(storePath)
			if err != nil {
				return fmt.Errorf("failed to read metadata of %s in local store: %w", resolved.Identifier(), err)
			}
			if err := walk(depMeta.RequiredApps, resolved.Identifier(), depth+1); err != nil {
				return err
			}
			closure = append(closure, ClosureEntry{App: resolved, RequiredBy: by, Present: true, StorePath: storePath})
		}
		return nil
	}

	if err := walk(apps, requiredBy, 0); err != nil {
		return nil, nil, err
	}
	return closure, missing, nil
}

// unresolvedPin describes a requirement that could not be satisfied: it keeps the
// constraint (so the message says what was needed) and the version the package was
// built against (so a fetch has something concrete to ask a repository for).
func unresolvedPin(app metadata.RequiredApp, org string) metadata.RequiredApp {
	return metadata.RequiredApp{
		Name: app.Name, Org: org, Version: app.Version,
		VersionSpec: app.VersionSpec, Requirement: app.Requirement,
	}
}

// MissingError renders a hard error for an install that cannot proceed.
func MissingError(app string, missing []Missing) error {
	lines := make([]string, 0, len(missing))
	for _, m := range missing {
		lines = append(lines, "  "+m.String())
	}
	return fmt.Errorf("%w: cannot install %s offline because its required apps are not all present:\n%s\n"+
		"Install each missing package into the local store first (fpm install <file.fpm> ...), in dependency order; "+
		"nothing is fetched during install",
		ErrMissing, app, strings.Join(lines, "\n"))
}

// NewHTTPClient is exposed for callers that build repository clients the same way.
func NewHTTPClient(repo config.RepositoryConfig, timeout time.Duration) (*http.Client, error) {
	creds, err := repository.ResolveCredentials(repo.Name, repo.Username, true)
	if err != nil {
		return nil, err
	}
	return repository.NewClient(repo.URL, creds, timeout)
}
