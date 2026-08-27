// Package frontend builds the JavaScript single-page frontend that modern Frappe
// apps ship alongside their Python module, so `fpm package` can put the compiled
// result in the archive instead of shipping a checkout that renders a blank page.
//
// Frappe has two distinct asset schemes and fpm has to serve both:
//
//   - The classic scheme: `<app>/<app>/public/**/*.bundle.{js,css}` entry points
//     compiled by frappe's own esbuild into `<app>/public/dist/` and recorded in
//     `sites/assets/assets.json`. That is `bench build`, and internal/benchbuild
//     plus internal/assets already implement it.
//   - The SPA scheme, used by frappe/crm, frappe/helpdesk, frappe/insights,
//     frappe/gameplan and friends: a Vite project in its own directory with its own
//     package.json, compiled into `<app>/<app>/public/frontend/` and served through
//     the `sites/assets/<app>` -> `<app>/public` symlink at `/assets/<app>/frontend/`.
//     Its HTML entry is copied to `<app>/<app>/www/<app>.html`, which frappe's website
//     router renders at `/<app>`. `bench build` does NOT produce any of this — esbuild
//     only globs `*.bundle.*` and an SPA has none — so nothing in the classic path
//     builds it. This package does.
//
// Both outputs live under `<app>/<app>/public/`, and both are listed in the app's
// .gitignore (crm ignores `crm/public/frontend` and `crm/www/crm.html`), so they are
// never present in a fresh checkout and must be produced at packaging time.
//
// Detection deliberately prefers the checkout's root package.json when it has a
// build script, because that is the app's own entry point and it already delegates:
// crm's root build is `cd frontend && yarn build` and its postinstall is
// `cd frontend && yarn install --check-files`. Building the root and the subdirectory
// separately would run the same Vite build twice.
package frontend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"fpm/internal/utils"
)

// ErrBuildFailed wraps every frontend build failure so a caller can tell it apart
// from a validation, vendoring or network failure.
var ErrBuildFailed = errors.New("frontend build failed")

// DefaultTimeout bounds one install+build pair. Vite builds of a large frappe-ui app
// take a few minutes on a cold cache; twenty minutes is generous without hanging CI
// forever on a wedged install.
const DefaultTimeout = 20 * time.Minute

// buildLogTail is how much of a failed build's output is quoted in the error. A
// bundler prints the line that matters (the unresolved import, the missing module)
// and then a long stack trace, so a small budget shows only the stack. Use
// --build-verbose to stream the whole thing instead.
const buildLogTail = 6000

// subdirCandidates are the conventional directory names for an app's SPA, tried in
// order when the checkout root has no build script of its own. `frontend` covers crm,
// insights, gameplan and builder; `desk` covers helpdesk; the rest are seen in the
// wider ecosystem.
var subdirCandidates = []string{"frontend", "desk", "dashboard", "ui", "spa"}

// Project is a detected JS build: the directory whose package.json declares a build
// script, and how to drive it.
type Project struct {
	// Dir is the absolute directory containing the package.json to build.
	Dir string
	// Rel is Dir relative to the app checkout root, "." for the root itself.
	Rel string
	// BuildScript is the package.json `scripts.build` entry, kept for reporting.
	BuildScript string
	// PkgManager is the resolved package manager: pnpm, yarn or npm.
	PkgManager string
	// HasLockfile reports whether Dir has a lockfile for PkgManager, which decides
	// whether an immutable/frozen install is safe to ask for.
	HasLockfile bool
}

// Output describes the built artifacts found under the app module. Paths are
// relative to the app checkout root and use forward slashes, so they can go into
// metadata and be compared across platforms.
//
// The output directory is not fixed. Each app points its bundler wherever it likes
// under <app>/public and names the route to match:
//
//	frappe/crm       crm/public/frontend        --base=/assets/crm/frontend/    crm/www/crm.html
//	frappe/erpnext   erpnext/public/banking     --base=/assets/erpnext/banking/ erpnext/www/banking.html
//	frappe_ai        frappe_ai/public/frontend/dist  (library mode, no index.html)
//	frappe           frappe/public/dist         (classic esbuild bundles)
//
// So Dirs is discovered, never assumed: a directory under <app>/public holding an
// index.html is an SPA root, and a directory named dist holding files is bundler
// output. Assuming `public/frontend` would fail erpnext outright.
type Output struct {
	// Dirs are the built output roots under <app>/public, sorted.
	Dirs []string
	// Entries are the index.html files inside Dirs, sorted. A library-mode build
	// (frappe_ai) produces none.
	Entries []string
	// Routes are the <app>/www/*.html templates frappe renders the SPAs at, sorted.
	// Only routes that belong to a discovered SPA root are listed, never every www
	// template the app happens to ship.
	Routes []string
	// Files counts every regular file under Dirs.
	Files int
	// Bytes totals the size of those files.
	Bytes int64
}

// Any reports whether anything servable was built.
func (o Output) Any() bool { return len(o.Dirs) > 0 }

// Primary is the output root to report as the app's frontend, or "" when there is
// none. An SPA root wins over plain bundler output, since that is what the app is
// actually served from.
func (o Output) Primary() string {
	for _, entry := range o.Entries {
		return path.Dir(entry)
	}
	if len(o.Dirs) > 0 {
		return o.Dirs[0]
	}
	return ""
}

// BuildOptions configures one frontend build.
type BuildOptions struct {
	// SourcePath is the app checkout root, the directory containing <AppName>/.
	SourcePath string
	// AppName is the Frappe app module name, e.g. "crm".
	AppName string
	// Stdout receives progress; nil means os.Stdout.
	Stdout io.Writer
	// Verbose streams the package manager's output instead of only reporting it on
	// failure.
	Verbose bool
	// Env is appended to the build environment, after the defaults this package sets.
	Env []string
	// Timeout bounds the install and the build separately. Zero means DefaultTimeout.
	Timeout time.Duration
	// SiteConfigPath is a real bench's sites/common_site_config.json to build a
	// bench-resolving frontend against. Empty synthesizes one from frappe's defaults.
	SiteConfigPath string
	// NoScaffold refuses to build a bench-resolving frontend outside a bench instead
	// of staging one, for a caller that would rather fail than build against
	// synthesized values.
	NoScaffold bool
}

// Result reports what Build did.
type Result struct {
	// Built is true when a project was detected and its build ran to completion.
	Built bool
	// Project is the detected project, nil when the app has no JS frontend.
	Project *Project
	// Output describes the artifacts the build left behind.
	Output Output
	// Log is the combined install+build output, kept for diagnostics.
	Log string
	// BuildRoot is the checkout the build ran in and that the caller must package
	// from: SourcePath normally, or the staged copy inside a scaffolded bench when the
	// frontend needed one. Empty when nothing was built.
	BuildRoot string
	// Cleanup removes anything staged for the build. The caller runs it after
	// packaging from BuildRoot. Never nil.
	Cleanup func()
}

// Detect finds the app's JS frontend project, or returns nil when it has none.
//
// The checkout root wins when its package.json has a build script: that is the app's
// declared entry point and it already delegates into the SPA subdirectory. Only when
// the root has no build script are the conventional subdirectories tried.
func Detect(sourcePath, appName string) (*Project, error) {
	root, err := filepath.Abs(sourcePath)
	if err != nil {
		return nil, err
	}

	dirs := []string{root}
	for _, name := range subdirCandidates {
		dirs = append(dirs, filepath.Join(root, name))
	}
	if appName != "" {
		dirs = append(dirs, filepath.Join(root, appName, "frontend"))
	}

	for _, dir := range dirs {
		pkg, ok, err := readPackageJSON(dir)
		if err != nil {
			return nil, err
		}
		if !ok || strings.TrimSpace(pkg.Scripts["build"]) == "" {
			continue
		}
		// frappe's own root build script is `node esbuild`: the framework's bundler,
		// which compiles every app in a bench and reads sites/apps.txt to know which.
		// It is `bench build`, not an app frontend, and internal/benchbuild owns it —
		// running it here fails on a missing apps.txt and would be duplicated work if
		// it did not.
		if isFrappeBundler(dir, pkg.Scripts["build"]) || isMissingTargetDir(dir, pkg.Scripts["build"]) {
			continue
		}
		rel, relErr := filepath.Rel(root, dir)
		if relErr != nil {
			rel = dir
		}
		pm, hasLock := detectPackageManager(dir, root, pkg.PackageManager)
		return &Project{
			Dir:         dir,
			Rel:         filepath.ToSlash(rel),
			BuildScript: strings.TrimSpace(pkg.Scripts["build"]),
			PkgManager:  pm,
			HasLockfile: hasLock,
		}, nil
	}
	return nil, nil
}

// Build detects the app's frontend and compiles it. An app with no frontend is not
// an error: Result.Built is false and the caller carries on.
//
// A detected frontend that builds but writes nothing under <app>/public is an error.
// Silently shipping such a package is the failure this whole package exists to
// prevent — it installs cleanly and then serves a blank page.
func Build(opts BuildOptions) (Result, error) {
	out := opts.Stdout
	if out == nil {
		out = os.Stdout
	}
	root, err := filepath.Abs(opts.SourcePath)
	if err != nil {
		return Result{}, err
	}
	if opts.AppName == "" {
		return Result{}, fmt.Errorf("%w: app name is required", ErrBuildFailed)
	}

	noop := func() {}
	project, err := Detect(root, opts.AppName)
	if err != nil {
		return Result{Cleanup: noop}, fmt.Errorf("%w: %v", ErrBuildFailed, err)
	}
	if project == nil {
		return Result{Cleanup: noop}, nil
	}

	if _, lookErr := exec.LookPath(project.PkgManager); lookErr != nil {
		return Result{Project: project, Cleanup: noop}, fmt.Errorf(
			"%w: '%s' declares a frontend in %s (build: %q) but %s is not on PATH; "+
				"install it, or pass --build-frontend=false to package without the compiled frontend",
			ErrBuildFailed, opts.AppName, project.Rel, project.BuildScript, project.PkgManager)
	}

	// A frontend that imports the bench's common_site_config.json can only be built
	// from <bench>/apps/<app>. Rather than refuse, stage a throwaway bench around a
	// copy of the checkout and build there; the package is then created from that copy.
	cleanup := noop
	if RequiresBench(root) && !InsideBench(root) {
		if opts.NoScaffold {
			return Result{Project: project, Cleanup: noop}, fmt.Errorf(
				"%w: '%s' has a frontend that resolves the bench from its own path "+
					"(it imports %s.json) and %s is not inside a bench",
				ErrBuildFailed, opts.AppName, benchConfigImport, root)
		}
		// Writing the one missing file next to the checkout is enough and costs
		// nothing. Only when that location cannot be written — a checkout near the
		// filesystem root, a read-only parent — is the whole tree staged into a
		// throwaway bench instead.
		if written, configCleanup := ensureBenchConfig(root, opts.SiteConfigPath, out); written {
			cleanup = configCleanup
		} else {
			staged, stagedCleanup, scaffoldErr := scaffoldBench(root, opts.AppName, opts.SiteConfigPath, out)
			if scaffoldErr != nil {
				return Result{Project: project, Cleanup: noop}, scaffoldErr
			}
			cleanup = stagedCleanup
			root = staged
			// The project must be re-resolved against the staged copy: everything
			// below runs there, including the package manager's working directory.
			if project, err = Detect(root, opts.AppName); err != nil || project == nil {
				cleanup()
				return Result{Cleanup: noop}, fmt.Errorf("%w: staged checkout at %s lost its frontend: %v",
					ErrBuildFailed, root, err)
			}
		}
	}
	// Every error below must release what was staged, since the caller only runs
	// Cleanup for a Result it got back without one.
	fail := func(r Result, e error) (Result, error) {
		cleanup()
		r.Cleanup = noop
		return r, e
	}

	fmt.Fprintf(out, "Detected '%s' frontend in %s (%s, build: %q)\n",
		opts.AppName, project.Rel, project.PkgManager, project.BuildScript)

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	env := buildEnv(opts.Env)
	result := Result{Project: project, BuildRoot: root, Cleanup: cleanup}

	var log strings.Builder
	run := func(step string, args []string, extraEnv ...string) error {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Dir = project.Dir
		cmd.Env = append(append([]string{}, env...), extraEnv...)
		fmt.Fprintf(out, "Running: %s (in %s)\n", strings.Join(args, " "), project.Rel)

		var buf strings.Builder
		if opts.Verbose {
			cmd.Stdout = io.MultiWriter(&buf, out)
			cmd.Stderr = io.MultiWriter(&buf, out)
		} else {
			cmd.Stdout = &buf
			cmd.Stderr = &buf
		}
		runErr := cmd.Run()
		log.WriteString(buf.String())
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("%w: frontend %s for '%s' timed out after %s\n%s",
				ErrBuildFailed, step, opts.AppName, timeout, tail(buf.String(), buildLogTail))
		}
		if runErr != nil {
			return fmt.Errorf("%w: frontend %s for '%s' (%s in %s) failed: %v\n%s%s",
				ErrBuildFailed, step, opts.AppName, project.PkgManager, project.Rel, runErr,
				tail(buf.String(), 2000), benchHint(root, opts.AppName, buf.String()))
		}
		return nil
	}

	before := publicFiles(root, opts.AppName)

	cleanupPM, pmErr := ensureLocalPackageManager(project.Dir, project.PkgManager)
	if pmErr == nil && cleanupPM != nil {
		defer cleanupPM()
	}

	if err := run("install", installArgs(project)); err != nil {
		// A frozen install refuses when the app's own lockfile has drifted from its
		// package.json — pnpm stopped reading `pnpm.overrides` from package.json, so
		// drive's committed lockfile no longer matches it and every install fails with
		// ERR_PNPM_LOCKFILE_CONFIG_MISMATCH. That is the app's drift to resolve, not a
		// reason to have no package at all, so retry unfrozen and say so.
		relaxed, ok := unfrozenInstallArgs(project)
		if !ok || !isFrozenLockfileRefusal(log.String()) {
			result.Log = log.String()
			return fail(result, err)
		}
		fmt.Fprintf(out, "Frozen install refused: %s's lockfile has drifted from its package.json. "+
			"Retrying with %s — the build is reproducible from the lockfile that results, not the one committed.\n",
			opts.AppName, strings.Join(relaxed, " "))
		// The flag alone is not enough, and neither are npm_config_* variables: an
		// app's own postinstall may run a *nested* install — drive's is
		// `npm run check-pnpm && cd frontend && pnpm install` — and `npm run`
		// re-derives npm_config_* for its children from npm's own config, dropping
		// anything injected. A config *file* survives that, so the setting is written
		// to one and pointed at with NPM_CONFIG_USERCONFIG. The user's own ~/.npmrc is
		// carried into it, so registry credentials and mirrors still apply.
		npmrc, cleanupNpmrc, npmrcErr := unfrozenNpmrc()
		if npmrcErr != nil {
			result.Log = log.String()
			return fail(result, npmrcErr)
		}
		defer cleanupNpmrc()
		// CI=false matters most of all: pnpm freezes by default *because* it detects
		// CI, and pnpm 11 has moved its settings out of .npmrc (it says so itself:
		// "no longer read by pnpm ... the new home of each setting"). Unsetting the
		// signal is what actually reaches an install nested behind `npm run`.
		if retryErr := run("install (unfrozen)", relaxed,
			"CI=false", "NPM_CONFIG_USERCONFIG="+npmrc,
			"npm_config_frozen_lockfile=false", "npm_config_prod=false"); retryErr != nil {
			result.Log = log.String()
			return fail(result, retryErr)
		}
	}
	if err := run("build", buildArgs(project)); err != nil {
		result.Log = log.String()
		return fail(result, err)
	}
	result.Log = log.String()

	// crm's and erpnext's build scripts end in `copy-html-entry`, which does this
	// themselves. Apps whose build script stops at `vite build` do not, and without
	// the www template frappe has no route to render the SPA at.
	if _, err := EnsureWWWEntry(root, opts.AppName); err != nil {
		return fail(result, fmt.Errorf("%w: %v", ErrBuildFailed, err))
	}

	written := writtenDirs(root, opts.AppName, before)
	output, err := outputsFrom(root, opts.AppName, written)
	if err != nil {
		return fail(result, fmt.Errorf("%w: %v", ErrBuildFailed, err))
	}
	// A build that wrote beside its own project (<project>/dist) instead of into the
	// app module has produced nothing frappe can serve: only <app>/public is linked
	// into sites/assets. The apps that matter aim into the module already — crm's vite
	// outDir is ../crm/public/frontend — so this is a rescue for the ones that do not,
	// not a path anything should rely on.
	if !output.Any() {
		adopted, adoptErr := adoptProjectOutput(root, opts.AppName, project)
		if adoptErr != nil {
			return fail(result, fmt.Errorf("%w: %v", ErrBuildFailed, adoptErr))
		}
		if adopted != "" {
			fmt.Fprintf(out, "Frontend output was written to %s, outside the app module; "+
				"moved it to %s so frappe can serve it\n", path.Join(project.Rel, "dist"), adopted)
			if _, err := EnsureWWWEntry(root, opts.AppName); err != nil {
				return fail(result, fmt.Errorf("%w: %v", ErrBuildFailed, err))
			}
			if output, err = outputsFrom(root, opts.AppName, append(written, adopted)); err != nil {
				return fail(result, fmt.Errorf("%w: %v", ErrBuildFailed, err))
			}
		}
	}
	if !output.Any() {
		return fail(result, fmt.Errorf(
			"%w: '%s' declares a frontend build in %s (%q) but it wrote nothing servable under %s; "+
				"the package would install and then serve a blank page\n%s",
			ErrBuildFailed, opts.AppName, project.Rel, project.BuildScript,
			filepath.Join(opts.AppName, "public"), tail(log.String(), buildLogTail)))
	}

	result.Built = true
	result.Output = output
	report(out, opts.AppName, output)
	return result, nil
}

// Outputs discovers what a frontend build has left under <app>/public. It is safe to
// call without having built: an app with no compiled frontend yields a zero Output.
//
// A directory is treated as built output when it holds an index.html (an SPA root:
// crm's public/frontend, erpnext's public/banking) or when it is named dist and holds
// files (classic esbuild bundles, and library-mode Vite output like frappe_ai's
// public/frontend/dist). Nothing is assumed about the directory's name — see the
// Output doc comment for why that matters.
func Outputs(sourcePath, appName string) (Output, error) {
	return outputsFrom(sourcePath, appName, nil)
}

// outputsFrom is Outputs with a set of directories the caller already knows are build
// output. Build passes the directories its own build actually wrote to, which is
// authoritative where the heuristics can only guess: an output directory with no
// index.html that is not called dist is invisible to a scan but is still what the
// build produced.
func outputsFrom(sourcePath, appName string, knownRoots []string) (Output, error) {
	root, err := filepath.Abs(sourcePath)
	if err != nil {
		return Output{}, err
	}
	public := filepath.Join(root, appName, "public")
	if !dirExists(public) {
		return Output{}, nil
	}

	// roots maps an absolute output root to whether it holds an index.html. Both kinds
	// of marker are collected in one walk, then reconciled, so a `dist` directory that
	// also holds an index.html is recorded as an SPA root rather than bare output.
	roots := map[string]bool{}
	markRoot := func(dir string, hasIndex bool) {
		if hasIndex {
			roots[dir] = true
			return
		}
		if _, seen := roots[dir]; !seen {
			roots[dir] = false
		}
	}
	for _, known := range knownRoots {
		abs := known
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(root, filepath.FromSlash(known))
		}
		if files, _, mErr := measure(abs); mErr == nil && files > 0 {
			markRoot(abs, fileExists(filepath.Join(abs, "index.html")))
		}
	}
	err = filepath.WalkDir(public, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			// node_modules under public/ is an install-time artifact some apps carry
			// (wiki does); it is never build output and walking it is expensive.
			if d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			// A directory named dist is bundler output even without an index.html,
			// which is how a library-mode build (frappe_ai) and frappe's own esbuild
			// output are recognised. The walk continues into it so an index.html
			// deeper down still registers.
			if d.Name() == "dist" {
				if files, _, mErr := measure(p); mErr == nil && files > 0 {
					markRoot(p, false)
				}
			}
			return nil
		}
		if d.Name() == "index.html" {
			if dir := filepath.Dir(p); dir != public {
				markRoot(dir, true)
			}
		}
		return nil
	})
	if err != nil {
		return Output{}, fmt.Errorf("failed to scan built assets in %s: %w", public, err)
	}

	var out Output
	for dir, hasIndex := range roots {
		// A nested output root (public/frontend/dist inside public/frontend) is part
		// of its parent, not a separate deliverable.
		if hasAncestorIn(dir, roots) {
			continue
		}
		out.Dirs = append(out.Dirs, relSlash(root, dir))
		if hasIndex {
			out.Entries = append(out.Entries, relSlash(root, filepath.Join(dir, "index.html")))
		}
		if files, bytes, mErr := measure(dir); mErr == nil {
			out.Files += files
			out.Bytes += bytes
		}
	}
	sort.Strings(out.Dirs)
	sort.Strings(out.Entries)

	// Only the templates that actually reference the built output are this frontend's
	// routes. erpnext declares 24 to_routes, all but one of them DocType portal pages
	// that have nothing to do with its banking SPA; listing those as frontend routes
	// would be simply wrong.
	seenRoute := map[string]bool{}
	for _, name := range RouteNames(root, appName) {
		rel := routeFile(root, appName, name)
		if rel == "" || seenRoute[rel] {
			continue
		}
		if !rendersFrontend(root, appName, rel, out) {
			continue
		}
		seenRoute[rel] = true
		out.Routes = append(out.Routes, rel)
	}
	sort.Strings(out.Routes)
	return out, nil
}

// rendersFrontend reports whether a www template is the page that renders this
// frontend, rather than one of the app's unrelated portal pages.
//
// Two signals, because apps produce the template two different ways. A build ending in
// `copy-html-entry` (crm, erpnext) copies the SPA's index.html verbatim, so the files
// are identical. A build using frappe-ui's vite plugin writes the template directly
// with jinja boot data in it, so it differs from index.html — but it still links its
// bundles by the /assets/<app>/<dir>/ URL the sites/assets symlink serves.
func rendersFrontend(root, appName, rel string, out Output) bool {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return false
	}
	for _, dir := range out.Dirs {
		if strings.Contains(string(data), "/assets/"+appName+"/"+path.Base(dir)+"/") {
			return true
		}
	}
	for _, entry := range out.Entries {
		index, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(entry)))
		if err == nil && bytes.Equal(index, data) {
			return true
		}
	}
	return false
}

// toRoutePattern extracts the `to_route` values of hooks.py website_route_rules.
// Frappe resolves each against the app's www/ directory, so they name the templates
// the frontend is rendered from. Values are plain quoted strings in every app in the
// ecosystem, even when the matching from_route is an f-string.
var toRoutePattern = regexp.MustCompile(`["']to_route["']\s*:\s*["']([^"']+)["']`)

// RouteNames reads the app's own hooks.py for the routes it publishes, in file order
// and deduplicated.
//
// This is read rather than guessed because the name follows no convention: crm routes
// at crm, insights at _insights, builder at _builder, gameplan at g, helpdesk at
// helpdesk, erpnext's banking SPA at banking. Deriving it from the output directory
// name or the app name is wrong for most of them, and inventing a template frappe does
// not route to just leaves a dead page in the package.
func RouteNames(sourcePath, appName string) []string {
	root, err := filepath.Abs(sourcePath)
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(root, appName, "hooks.py"))
	if err != nil {
		return nil
	}
	var names []string
	seen := map[string]bool{}
	for _, m := range toRoutePattern.FindAllSubmatch(data, -1) {
		name := strings.TrimSpace(string(m[1]))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

// routeFile resolves one route name to the template backing it, relative to the
// checkout root, or "" when the app ships none. Frappe accepts either www/<name>.html
// or www/<name>/index.html; helpdesk uses the second form.
func routeFile(root, appName, name string) string {
	www := filepath.Join(root, appName, "www")
	for _, candidate := range []string{
		filepath.Join(www, name+".html"),
		filepath.Join(www, filepath.FromSlash(name), "index.html"),
	} {
		if fileExists(candidate) {
			return relSlash(root, candidate)
		}
	}
	return ""
}

// EnsureWWWEntry fills in a route template the app's build script did not write.
// crm's and erpnext's builds end in `copy-html-entry` and produce it themselves; an
// app whose build stops at `vite build` leaves the SPA with no route at all, and fpm
// supplies it. It returns the paths written, relative to the checkout root.
//
// The route name comes from the app's own hooks.py, never from a convention: crm
// routes at crm but insights routes at _insights, builder at _builder and gameplan at
// g. A template frappe does not route to is a dead page, so when hooks.py declares no
// route, or the app builds several SPAs and the mapping is ambiguous, nothing is
// written — the caller reports it instead of inventing a filename.
//
// An existing template is never overwritten: an app that ships a hand-written one
// (with its own jinja context) owns that file.
func EnsureWWWEntry(sourcePath, appName string) ([]string, error) {
	root, err := filepath.Abs(sourcePath)
	if err != nil {
		return nil, err
	}
	out, err := Outputs(root, appName)
	if err != nil {
		return nil, err
	}
	// One SPA and one or more declared routes is the only unambiguous case: every
	// route renders that SPA. With several SPAs there is no way to tell which route
	// belongs to which, and guessing would put the wrong app behind a URL.
	names := RouteNames(root, appName)
	// Exactly one SPA and exactly one declared route is the only case where the
	// mapping is certain. erpnext declares 24 to_routes — most naming DocTypes, not
	// templates — so filling in every one that lacks a file would scatter copies of
	// the banking SPA across a dozen unrelated URLs. hrms declares two routes for two
	// separate SPAs and its build writes both itself.
	if len(out.Entries) != 1 || len(names) != 1 {
		return nil, nil
	}
	index := filepath.Join(root, filepath.FromSlash(out.Entries[0]))

	var written []string
	for _, name := range names {
		if routeFile(root, appName, name) != "" {
			continue
		}
		target := filepath.Join(root, appName, "www", name+".html")
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return written, fmt.Errorf("failed to create %s: %w", filepath.Dir(target), err)
		}
		if err := utils.CopyRegularFile(index, target, 0o644); err != nil {
			return written, fmt.Errorf("failed to write the SPA route template %s: %w", target, err)
		}
		written = append(written, relSlash(root, target))
	}
	sort.Strings(written)
	return written, nil
}

func report(out io.Writer, appName string, o Output) {
	fmt.Fprintf(out, "Frontend build produced %d file(s) (%s) in %s\n",
		o.Files, humanBytes(o.Bytes), strings.Join(o.Dirs, ", "))
	for _, dir := range o.Dirs {
		// public/<name> is served at /assets/<app>/<name>/ through the
		// sites/assets/<app> -> <app>/public symlink frappe's make_asset_dirs creates.
		served := strings.TrimPrefix(dir, appName+"/public/")
		fmt.Fprintf(out, "  %s -> /assets/%s/%s/\n", dir, appName, served)
	}
	for _, route := range o.Routes {
		fmt.Fprintf(out, "  route: %s\n", route)
	}
	if len(o.Entries) > 0 && len(o.Routes) == 0 {
		fmt.Fprintf(out, "  note: the SPA has no route template under %s/www, and %s/hooks.py "+
			"declares no website_route_rules to derive one from; frappe will not serve it at a URL\n",
			appName, appName)
	}
}

// packageJSON is the subset of package.json this package reads.
type packageJSON struct {
	Name           string            `json:"name"`
	PackageManager string            `json:"packageManager"`
	Scripts        map[string]string `json:"scripts"`
}

func readPackageJSON(dir string) (packageJSON, bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if os.IsNotExist(err) {
		return packageJSON{}, false, nil
	}
	if err != nil {
		// A directory in the candidate list that is not readable is not this
		// package's problem to diagnose; treat it as absent.
		return packageJSON{}, false, nil
	}
	var pkg packageJSON
	if err := json.Unmarshal(data, &pkg); err != nil {
		return packageJSON{}, false, fmt.Errorf("cannot parse %s: %w", filepath.Join(dir, "package.json"), err)
	}
	return pkg, true, nil
}

// detectPackageManager resolves which package manager drives dir. A lockfile in dir
// is the strongest signal; then the `packageManager` field (corepack); then a
// lockfile at the checkout root, because a delegating root build (crm) installs from
// the root. Frappe apps are a yarn ecosystem, so yarn is the last resort — but only
// if it is actually installed, otherwise npm, which ships with node.
func detectPackageManager(dir, root, packageManagerField string) (pm string, hasLockfile bool) {
	type candidate struct {
		name string
		lock string
	}
	candidates := []candidate{
		{"pnpm", "pnpm-lock.yaml"},
		{"yarn", "yarn.lock"},
		{"npm", "package-lock.json"},
	}

	for _, c := range candidates {
		if fileExists(filepath.Join(dir, c.lock)) {
			return c.name, true
		}
	}
	if field := strings.ToLower(packageManagerField); field != "" {
		for _, c := range candidates {
			if strings.HasPrefix(field, c.name) {
				return c.name, false
			}
		}
	}
	if root != dir {
		for _, c := range candidates {
			if fileExists(filepath.Join(root, c.lock)) {
				return c.name, false
			}
		}
	}
	if _, err := exec.LookPath("yarn"); err == nil {
		return "yarn", false
	}
	return "npm", false
}

// installArgs is the dependency install for pm. Frozen/immutable installs are only
// requested when a lockfile is present, so an app without one still installs.
//
// devDependencies are forced on. A frontend build needs them — crm keeps
// autoprefixer, postcss and tailwindcss there — and every package manager drops them
// when NODE_ENV=production is inherited from the environment, which is exactly what a
// release pipeline sets. Without this the install succeeds and the build then fails on
// a missing module.
func installArgs(p *Project) []string {
	switch p.PkgManager {
	case "pnpm":
		if p.HasLockfile {
			return []string{"pnpm", "install", "--prod=false", "--frozen-lockfile"}
		}
		return []string{"pnpm", "install", "--prod=false"}
	case "npm":
		if p.HasLockfile {
			return []string{"npm", "ci", "--include=dev", "--no-audit", "--no-fund"}
		}
		return []string{"npm", "install", "--include=dev", "--no-audit", "--no-fund"}
	default:
		// `--check-files` is what bench itself runs for an app's node dependencies,
		// and it repairs a node_modules left behind by an earlier partial install.
		return []string{"yarn", "install", "--check-files", "--non-interactive", "--production=false"}
	}
}

// unfrozenNpmrc writes a throwaway npm config that turns the frozen install off, and
// returns its path. It is the only channel that reaches an install nested inside an
// `npm run`, which rebuilds npm_config_* for its children and so loses anything passed
// in the environment.
//
// The user's own ~/.npmrc is copied in first, so registry credentials, scopes and
// mirrors keep working; only frozen-lockfile is added on top.
func unfrozenNpmrc() (path string, cleanup func(), err error) {
	noop := func() {}
	dir, err := os.MkdirTemp("", "fpm-npmrc-")
	if err != nil {
		return "", noop, fmt.Errorf("%w: cannot create an npm config: %v", ErrBuildFailed, err)
	}
	cleanup = func() { os.RemoveAll(dir) }

	var existing []byte
	if home, homeErr := os.UserHomeDir(); homeErr == nil {
		existing, _ = os.ReadFile(filepath.Join(home, ".npmrc"))
	}
	contents := string(existing)
	if contents != "" && !strings.HasSuffix(contents, "\n") {
		contents += "\n"
	}
	contents += "frozen-lockfile=false\n"

	path = filepath.Join(dir, "npmrc")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		cleanup()
		return "", noop, fmt.Errorf("%w: cannot write an npm config: %v", ErrBuildFailed, err)
	}
	return path, cleanup, nil
}

// unfrozenInstallArgs is the same install with the lockfile treated as a starting point
// rather than a contract. Only pnpm and npm distinguish the two; yarn 1 never freezes.
func unfrozenInstallArgs(p *Project) ([]string, bool) {
	switch p.PkgManager {
	case "pnpm":
		return []string{"pnpm", "install", "--prod=false", "--no-frozen-lockfile"}, true
	case "npm":
		return []string{"npm", "install", "--include=dev", "--no-audit", "--no-fund"}, true
	default:
		return nil, false
	}
}

// isFrozenLockfileRefusal recognises a package manager declining because the lockfile
// does not match the manifest, as opposed to a network or dependency failure — which
// retrying unfrozen would only paper over.
func isFrozenLockfileRefusal(log string) bool {
	for _, marker := range []string{
		"ERR_PNPM_LOCKFILE_CONFIG_MISMATCH",
		"ERR_PNPM_OUTDATED_LOCKFILE",
		"Cannot proceed with the frozen installation",
		"can only install packages when your package.json and package-lock.json",
		"npm ci` can only install",
	} {
		if strings.Contains(log, marker) {
			return true
		}
	}
	return false
}

func buildArgs(p *Project) []string {
	switch p.PkgManager {
	case "pnpm":
		return []string{"pnpm", "run", "build"}
	case "npm":
		return []string{"npm", "run", "build"}
	default:
		return []string{"yarn", "build"}
	}
}

// buildEnv assembles the environment for the install and the build. Defaults are set
// only when the caller has not already set them, so an explicit value always wins.
func buildEnv(extra []string) []string {
	env := os.Environ()
	env = append(env, extra...)

	set := map[string]bool{}
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i > 0 {
			set[kv[:i]] = true
		}
	}
	defaults := [][2]string{
		// Rollup holds the whole module graph in memory; a frappe-ui app overflows
		// node's default heap on a small runner.
		{"NODE_OPTIONS", "--max-old-space-size=4096"},
		// Keeps yarn/npm non-interactive and quiets progress bars in captured output.
		{"CI", "true"},
		// A checkout whose packageManager field pins a version corepack has not
		// downloaded must not hard-fail the build.
		{"COREPACK_ENABLE_STRICT", "0"},
	}
	// NODE_ENV is deliberately not set. Setting it to "production" makes yarn skip
	// devDependencies, and a frontend build needs them: crm's autoprefixer, postcss
	// and tailwindcss are all devDependencies, so the install succeeds and then
	// `vite build` dies with "Cannot find module 'autoprefixer'". Vite sets
	// NODE_ENV=production for the code it emits on its own.
	for _, d := range defaults {
		if !set[d[0]] {
			env = append(env, d[0]+"="+d[1])
		}
	}
	return env
}

// publicFiles lists every regular file under <app>/public, relative to that
// directory. A missing public/ yields an empty set, which is the common case: the
// build is about to create it.
func publicFiles(root, appName string) map[string]bool {
	public := filepath.Join(root, appName, "public")
	files := map[string]bool{}
	_ = filepath.WalkDir(public, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if rel, err := filepath.Rel(public, p); err == nil {
			files[filepath.ToSlash(rel)] = true
		}
		return nil
	})
	return files
}

// writtenDirs reports the immediate subdirectories of <app>/public that gained a file
// during the build, as absolute paths. Files directly in public/ are ignored: those
// are the app's own hand-maintained assets, not a bundler's output.
//
// Only new paths count. A rebuild that overwrites the same filenames reports nothing,
// which is correct to fall back from — the scan in outputsFrom still recognises the
// index.html or dist/ the app produced.
func writtenDirs(root, appName string, before map[string]bool) []string {
	public := filepath.Join(root, appName, "public")
	seen := map[string]bool{}
	for rel := range publicFiles(root, appName) {
		if before[rel] {
			continue
		}
		top, _, nested := strings.Cut(rel, "/")
		if !nested {
			continue
		}
		seen[filepath.Join(public, top)] = true
	}
	dirs := make([]string, 0, len(seen))
	for d := range seen {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	return dirs
}

// adoptProjectOutput copies <project>/dist into <app>/public/<project> when the build
// left its output beside the project rather than inside the app module. It returns the
// destination relative to the checkout root, or "" when there was nothing to adopt.
//
// The destination is <app>/public/<project-dir-name>, not <app>/public/dist: dist is
// where frappe's esbuild puts hashed *.bundle.* files that go into assets.json, and
// dropping a Vite build there would offer it to the manifest scan as something it is
// not. public/<name> is served at /assets/<app>/<name>/ either way.
func adoptProjectOutput(root, appName string, project *Project) (string, error) {
	if project == nil || project.Dir == root {
		return "", nil
	}
	src := filepath.Join(project.Dir, "dist")
	if files, _, err := measure(src); err != nil || files == 0 {
		return "", nil
	}
	dst := filepath.Join(root, appName, "public", filepath.Base(project.Dir))
	if dirExists(dst) {
		// Something is already there; it is not this build's to overwrite.
		return "", nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", fmt.Errorf("failed to create %s: %w", filepath.Dir(dst), err)
	}
	if err := utils.CopyDirectory(src, dst); err != nil {
		return "", fmt.Errorf("failed to move the frontend output from %s to %s: %w", src, dst, err)
	}
	return relSlash(root, dst), nil
}

// InsideBench reports whether root sits at <bench>/apps/<app>, which is where a
// frappe-ui frontend expects to find the bench. These frontends resolve the bench
// from their own physical path — crm's frontend/src/socket.js imports
// `../../../../sites/common_site_config.json` — so a checkout anywhere else cannot
// build, no matter what is installed.
func InsideBench(root string) bool {
	return fileExists(filepath.Join(root, "..", "..", "sites", "common_site_config.json"))
}

// benchHint explains the one frontend build failure whose cause is fpm's own doing:
// the app was built outside a bench. Raw rollup output ("Could not resolve
// ../../../../sites/common_site_config.json") gives the user no way to know that
// --bench-path is the answer.
func benchHint(root, appName, log string) string {
	if InsideBench(root) {
		return ""
	}
	if !strings.Contains(log, "common_site_config") && !strings.Contains(log, "sites/") {
		return ""
	}
	return fmt.Sprintf("\n\nThis frontend resolves the bench from its own path "+
		"(it imports ../../../../sites/common_site_config.json), so it can only be built from "+
		"<bench>/apps/%s. Repackage with:\n"+
		"    fpm package %s --bench-path <bench> --version <version>\n"+
		"which stages the checkout there for the build, or run the build yourself inside a bench "+
		"and package with --build-frontend=false.", appName, root)
}

// hasAncestorIn reports whether any other key in roots is a parent directory of dir.
func hasAncestorIn(dir string, roots map[string]bool) bool {
	for other := range roots {
		if other != dir && strings.HasPrefix(dir, other+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func measure(dir string) (files int, total int64, err error) {
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		files++
		total += info.Size()
		return nil
	})
	return files, total, err
}

func relSlash(root, p string) string {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return filepath.ToSlash(p)
	}
	return filepath.ToSlash(rel)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

func tail(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return "…" + s[len(s)-max:]
}

// benchConfigImport is how a frappe-ui frontend reaches the bench it is being built
// in. crm, gameplan, helpdesk and drive all carry exactly one such import, in their
// socket module:
//
//	import { socketio_port } from '../../../../sites/common_site_config.json'
//
// Four levels up from <bench>/apps/<app>/frontend/src is the bench root, so the file
// only exists when the checkout physically sits at <bench>/apps/<app>. Insights and
// builder have no such import and build anywhere.
const benchConfigImport = "sites/common_site_config"

// scaffoldSkip are not copied into a scaffolded bench: regenerated by the build, or
// large and irrelevant to it.
var scaffoldSkip = map[string]bool{".git": true, "node_modules": true, "__pycache__": true}

// sourceExts are the files a frontend's bench import can appear in.
var sourceExts = map[string]bool{".js": true, ".ts": true, ".jsx": true, ".tsx": true, ".vue": true, ".mjs": true}

// defaultSiteConfig is what `bench init` writes to sites/common_site_config.json.
// Only socketio_port is actually read by any app's frontend today, but rollup resolves
// JSON imports as named exports and errors on a missing one, so a scaffold that
// carries the whole default set will not fail an app that reads a different key.
//
// The values are a real bench's defaults, and the one that matters is inert in
// production: crm's socket module uses socketio_port only when window.location.port is
// set, which is a bench served directly on a port. Behind nginx on 80/443 the browser
// sees no port and the baked value is never read.
var defaultSiteConfig = map[string]any{
	"db_host":            "localhost",
	"db_port":            3306,
	"developer_mode":     0,
	"file_watcher_port":  6787,
	"redis_cache":        "redis://localhost:13000",
	"redis_queue":        "redis://localhost:11000",
	"redis_socketio":     "redis://localhost:13000",
	"serve_default_site": true,
	"socketio_port":      9000,
	"webserver_port":     8000,
}

// isFrappeBundler reports whether a build script is frappe's own esbuild entry point
// rather than an app's frontend. Both conditions must hold: the script invokes esbuild
// and the directory actually ships frappe's bundler, so an app that merely happens to
// use esbuild for its own frontend is not skipped.
func isFrappeBundler(dir, buildScript string) bool {
	if !strings.Contains(buildScript, "esbuild") {
		return false
	}
	for _, entry := range []string{
		filepath.Join(dir, "esbuild", "esbuild.js"),
		filepath.Join(dir, "esbuild", "index.js"),
	} {
		if fileExists(entry) {
			return true
		}
	}
	return false
}

func isMissingTargetDir(dir, buildScript string) bool {
	buildScript = strings.TrimSpace(buildScript)
	if strings.HasPrefix(buildScript, "cd ") {
		parts := strings.Fields(buildScript)
		if len(parts) >= 2 {
			target := parts[1]
			if _, err := os.Stat(filepath.Join(dir, target)); os.IsNotExist(err) {
				return true
			}
		}
	}
	return false
}

// RequiresBench reports whether the checkout's frontend sources import the bench's
// common_site_config.json, and so can only be built from <bench>/apps/<app>.
//
// It is a source scan rather than a build attempt because the answer decides where to
// build, and finding out by failing costs a dependency install first.
func RequiresBench(sourcePath string) bool {
	root, err := filepath.Abs(sourcePath)
	if err != nil {
		return false
	}
	found := false
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if found {
			return filepath.SkipAll
		}
		if d.IsDir() {
			if scaffoldSkip[d.Name()] || d.Name() == "dist" || d.Name() == "public" {
				return filepath.SkipDir
			}
			return nil
		}
		if !sourceExts[strings.ToLower(filepath.Ext(d.Name()))] {
			return nil
		}
		data, readErr := os.ReadFile(p)
		if readErr == nil && bytes.Contains(data, []byte(benchConfigImport)) {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// scaffoldBench stages the checkout inside a throwaway bench-shaped directory so a
// frontend that resolves the bench from its own path can be built without one:
//
//	<tmp>/sites/common_site_config.json    synthesized, or copied from siteConfigPath
//	<tmp>/apps/<app>/                      a copy of the checkout, minus .git/node_modules
//
// It returns the staged checkout, which is what the caller must package from — the
// build writes its output there, not into the original tree.
//
// The copy is what makes this work at all: a symlink would not, because Vite resolves
// importers to their real path, so `../../../../sites` would still be evaluated
// against the original location.
func scaffoldBench(root, appName, siteConfigPath string, out io.Writer) (buildRoot string, cleanup func(), err error) {
	noop := func() {}
	tmp, err := os.MkdirTemp("", "fpm-bench-")
	if err != nil {
		return "", noop, fmt.Errorf("%w: cannot create a build directory: %v", ErrBuildFailed, err)
	}
	cleanup = func() { os.RemoveAll(tmp) }

	config, source, err := siteConfig(siteConfigPath)
	if err != nil {
		cleanup()
		return "", noop, err
	}
	sites := filepath.Join(tmp, "sites")
	if err := os.MkdirAll(sites, 0o755); err != nil {
		cleanup()
		return "", noop, fmt.Errorf("%w: %v", ErrBuildFailed, err)
	}
	if err := os.WriteFile(filepath.Join(sites, "common_site_config.json"), config, 0o644); err != nil {
		cleanup()
		return "", noop, fmt.Errorf("%w: %v", ErrBuildFailed, err)
	}

	staged := filepath.Join(tmp, "apps", appName)
	if err := os.MkdirAll(filepath.Dir(staged), 0o755); err != nil {
		cleanup()
		return "", noop, fmt.Errorf("%w: %v", ErrBuildFailed, err)
	}
	fmt.Fprintf(out, "This frontend reads the bench from its own path; building it in a temporary bench "+
		"with %s\n", source)
	if err := utils.CopyTree(root, staged, scaffoldSkip); err != nil {
		cleanup()
		return "", noop, fmt.Errorf("%w: cannot stage %s for the build: %v", ErrBuildFailed, root, err)
	}
	return staged, cleanup, nil
}

// siteConfig returns the common_site_config.json contents to build against and a
// human description of where they came from.
// DefaultSiteConfigJSON is frappe's default sites/common_site_config.json, rendered.
// Exported so anything that lays out a bench-shaped working directory — the catalog
// mirror's workspace, for one — writes the same contents this package would.
func DefaultSiteConfigJSON() ([]byte, error) {
	return json.MarshalIndent(defaultSiteConfig, "", " ")
}

func siteConfig(path string) (data []byte, source string, err error) {
	if path != "" {
		data, err = os.ReadFile(path)
		if err != nil {
			return nil, "", fmt.Errorf("%w: cannot read the site config %s: %v", ErrBuildFailed, path, err)
		}
		var probe map[string]any
		if err := json.Unmarshal(data, &probe); err != nil {
			return nil, "", fmt.Errorf("%w: %s is not valid JSON: %v", ErrBuildFailed, path, err)
		}
		return data, "the site config from " + path, nil
	}
	data, err = json.MarshalIndent(defaultSiteConfig, "", " ")
	if err != nil {
		return nil, "", err
	}
	// The defaults are the right answer for a normal deployment. socketio_port is the
	// only key any app's frontend reads, and it is compiled in only to be used when
	// window.location.port is set — a bench served directly on a port. Behind nginx or
	// a kubernetes ingress on 80/443 the browser sees no port and the value is never
	// read, so a real bench's config would make no difference to the built bundle.
	return data, fmt.Sprintf("frappe's defaults (socketio_port %v — never read behind nginx or an "+
		"ingress; --frontend-site-config builds against a real bench)",
		defaultSiteConfig["socketio_port"]), nil
}

// ensureBenchConfig satisfies a frontend's `../../../../sites/common_site_config.json`
// import in place, by writing the file the build is missing and removing it afterwards.
//
// It is the cheap path: a checkout at <somewhere>/<parent>/<app> needs only
// <somewhere>/sites/common_site_config.json to exist for the build to resolve, so
// nothing has to be copied. Frappe's defaults are the right contents — socketio_port is
// the only key any app's frontend reads, and it is compiled in only to be used when
// window.location.port is set, which never happens behind nginx or a kubernetes
// ingress on 80/443.
//
// Nothing is overwritten: an existing config (a real bench) is left exactly as it is.
// A location that cannot be written reports ok=false so the caller can stage a
// throwaway bench instead, which always works.
func ensureBenchConfig(root, siteConfigPath string, out io.Writer) (ok bool, cleanup func()) {
	noop := func() {}
	benchRoot := filepath.Dir(filepath.Dir(root))
	sites := filepath.Join(benchRoot, "sites")
	configPath := filepath.Join(sites, "common_site_config.json")

	if fileExists(configPath) {
		return true, noop
	}
	config, source, err := siteConfig(siteConfigPath)
	if err != nil {
		return false, noop
	}

	createdSites := false
	if !dirExists(sites) {
		if mkErr := os.Mkdir(sites, 0o755); mkErr != nil {
			return false, noop
		}
		createdSites = true
	}
	if writeErr := os.WriteFile(configPath, config, 0o644); writeErr != nil {
		if createdSites {
			os.Remove(sites)
		}
		return false, noop
	}

	fmt.Fprintf(out, "This frontend imports the bench's %s.json, which is not there.\n"+
		"  Wrote %s with %s; it is removed after the build.\n", benchConfigImport, configPath, source)
	return true, func() {
		os.Remove(configPath)
		if createdSites {
			// Only if this left it empty; a directory that gained other files is not
			// fpm's to delete.
			os.Remove(sites)
		}
	}
}

// siblingAppRef matches a build script reaching for another app in the bench:
// `cd ../../frappe/ui`, `../../erpnext/...`. Two levels up from <bench>/apps/<app>/<sub>
// is apps/, so the next segment names a sibling app and the one after it, when present,
// names the directory inside it that is actually used.
var siblingAppRef = regexp.MustCompile(`\.\./\.\./([a-z][a-z0-9_-]*)/([a-z][a-z0-9_-]*)?`)

// SiblingApps reports the other bench apps a project's package.json scripts read off
// disk during the build, deduplicated and sorted. An entry may name a directory inside
// the app — "frappe/ui" — because that is the part the build actually compiles, and its
// own dependencies have to be installed for it to.
//
// These are build-time dependencies, distinct from the `required_apps` fpm already
// resolves: helpdesk's desk build runs `cd ../../frappe/ui && yarn install`, which needs
// frappe's source present, not a pinned version recorded in metadata. A caller that lays
// out a bench-shaped workspace can fetch them before building.
func SiblingApps(sourcePath, appName string) ([]string, error) {
	project, err := Detect(sourcePath, appName)
	if err != nil || project == nil {
		return nil, err
	}

	seen := map[string]bool{}
	scan := func(dir string) {
		pkg, ok, err := readPackageJSON(dir)
		if err != nil || !ok {
			return
		}
		for _, script := range pkg.Scripts {
			for _, m := range siblingAppRef.FindAllStringSubmatch(script, -1) {
				if m[1] == appName {
					continue
				}
				ref := m[1]
				if m[2] != "" {
					ref += "/" + m[2]
				}
				seen[ref] = true
			}
		}
	}
	// The root script usually delegates, so the subdirectory it delegates to is where
	// the reference actually lives.
	scan(project.Dir)
	root, err := filepath.Abs(sourcePath)
	if err != nil {
		return nil, err
	}
	for _, name := range subdirCandidates {
		scan(filepath.Join(root, name))
	}

	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

func ensureLocalPackageManager(dir, pm string) (cleanup func(), err error) {
	pkgPath := filepath.Join(dir, "package.json")
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return func() {}, nil
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return func() {}, nil
	}
	if _, ok := raw["packageManager"]; ok {
		return func() {}, nil
	}
	switch pm {
	case "yarn":
		raw["packageManager"] = "yarn@1.22.22"
	case "pnpm":
		raw["packageManager"] = "pnpm@9.0.0"
	case "npm":
		raw["packageManager"] = "npm@10.0.0"
	default:
		return func() {}, nil
	}
	updated, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return func() {}, nil
	}
	if err := os.WriteFile(pkgPath, updated, 0o644); err != nil {
		return func() {}, err
	}
	return func() {
		_ = os.WriteFile(pkgPath, data, 0o644)
	}, nil
}
