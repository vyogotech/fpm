package mirror

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"fpm/internal/frontend"
	"fpm/internal/metadata"
	"fpm/internal/semver"
	"fpm/internal/utils"
)

// Result actions, in the order a build can end.
const (
	ActionPublished       = "published"
	ActionPublishedNoDeps = "published-nodeps" // wheel vendoring failed, shipped without wheels
	ActionBuilt           = "built"            // --skip-publish
	ActionBuiltNoDeps     = "built-nodeps"
	ActionSkippedExists   = "skipped-exists" // registry already had it (publish-time race)
	ActionFailed          = "failed"
)

// Result is the outcome of one planned build.
type Result struct {
	Slug      string        `json:"slug"`
	AppName   string        `json:"app_name"`
	Version   string        `json:"version"`
	Action    string        `json:"action"`
	Duration  time.Duration `json:"duration_ns"`
	SizeBytes int64         `json:"size_bytes,omitempty"`
	Detail    string        `json:"detail,omitempty"`
}

// Runner executes a plan by driving the real fpm CLI as subprocesses.
//
// Self-exec rather than calling package internals keeps every packaging quirk
// (hooks.py inference, checksum, index update) on its already-tested path, and
// isolates each app in its own process: one crash cannot end the run.
type Runner struct {
	FPMBin     string // path of the fpm binary to drive; typically os.Executable()
	Workspace  *Workspace
	OutputPath string // where finished .fpm artifacts land
	// RepoNames are the configured repositories every built package is published to,
	// in order. More than one mirrors the same catalog into several backends at once
	// (GHCR as OCI and an HTTP FPM registry, say) from a single build.
	RepoNames   []string
	SkipPublish bool
	// CatalogRepos maps every catalog slug to its git URL, including entries that are
	// disabled for publishing. A build-time dependency is fetched from here: frappe is
	// no longer mirrored, but helpdesk's build still reads its source off disk.
	CatalogRepos map[string]string
	// BuildDepRefs pins a build dependency's ref per app slug, from the catalog's
	// build_deps column: BuildDepRefs["helpdesk"]["frappe"] = "version-15".
	BuildDepRefs  map[string]map[string]string
	PythonVersion string   // destination interpreter version for vendored wheels (e.g. "3.11")
	Platforms     []string // target platforms for vendored wheels (e.g. "manylinux2014_x86_64")
	Log           func(format string, args ...any)
}

// Run executes every planned item, isolating failures per app.
func (r *Runner) Run(plan *Plan) []Result {
	if r.Log == nil {
		r.Log = func(format string, args ...any) { fmt.Printf(format+"\n", args...) }
	}

	results := make([]Result, 0, len(plan.Items))
	for i, item := range plan.Items {
		r.Log("[%d/%d] %s %s (%s)", i+1, len(plan.Items), item.Slug, item.Version, item.Reason)
		start := time.Now()
		result := r.runOne(item)
		result.Duration = time.Since(start)
		r.Log("[%d/%d] %s %s: %s", i+1, len(plan.Items), item.Slug, item.Version, result.Action)
		results = append(results, result)
	}
	return results
}

func (r *Runner) runOne(item BuildItem) Result {
	result := Result{Slug: item.Slug, AppName: item.AppName, Version: item.Version}
	fail := func(stage string, err error) Result {
		result.Action = ActionFailed
		result.Detail = fmt.Sprintf("%s: %v", stage, err)
		return result
	}

	checkout, err := r.Workspace.Checkout(item.Slug, item.Repo, item.Ref, item.isBranch)
	if err != nil {
		return fail("checkout", err)
	}

	// BuildItem.AppName is the catalog's *override*, empty for every app whose module
	// is named after its slug — which is most of them. The slug is the fallback the
	// rest of this function already uses, and the frontend build needs a real name to
	// locate <app>/public.
	appName := item.AppName
	if appName == "" {
		appName = item.Slug
	}

	buildRoot := checkout
	if item.buildScript != "" {
		if err := r.runBuildScript(item.buildScript, checkout); err != nil {
			return fail("asset build", err)
		}
	} else {
		root, cleanup, err := r.autoBuildFrontendAssets(appName, checkout)
		// Runs after packageApp below, which is the point: the staged tree has to
		// outlive the build and be gone before the next app is checked out.
		defer cleanup()
		if err != nil {
			return fail("asset build", err)
		}
		buildRoot = root
	}

	artifact, noDeps, err := r.packageApp(item, buildRoot)
	if err != nil {
		return fail("package", err)
	}
	defer os.RemoveAll(filepath.Dir(artifact))

	manifest, err := metadata.ReadMetadataFromFPMArchive(artifact)
	if err != nil {
		return fail("verify", err)
	}
	if manifest.PackageVersion != item.Version {
		return fail("verify", fmt.Errorf("archive says version %q, expected %q", manifest.PackageVersion, item.Version))
	}
	result.AppName = manifest.AppName
	if manifest.AppName != appName {
		r.Log("  note: %s's app name is %q, not %q — add app_name=%s to the catalog so "+
			"skip-if-published checks the right metadata", item.Slug, manifest.AppName, appName, manifest.AppName)
	}

	if manifest.AssetsBuilt {
		r.Log("  assets: verified %d compiled bundle(s)", len(manifest.AssetBundles))
	}
	if manifest.WheelPlatform != "" {
		r.Log("  wheels: verified vendored wheels for %s (python %s)", manifest.WheelPlatform, manifest.WheelPythonVersion)
	}

	final := filepath.Join(r.OutputPath, filepath.Base(artifact))
	if err := moveFile(artifact, final); err != nil {
		return fail("store", err)
	}
	if info, err := os.Stat(final); err == nil {
		result.SizeBytes = info.Size()
	}

	if r.SkipPublish {
		result.Action = ActionBuilt
		if noDeps {
			result.Action = ActionBuiltNoDeps
		}
		result.Detail = final
		return result
	}

	// Published to every configured repository, whatever mix of backends they are.
	// A repository that already has this version is not a failure: the plan builds a
	// version missing from any one of them, so the others are expected to report it.
	// The run's contract is that all of them hold it afterwards.
	pushed, existed := make([]string, 0, len(r.RepoNames)), make([]string, 0, len(r.RepoNames))
	for _, repoName := range r.RepoNames {
		out, err := r.fpm("publish", "--from-file", final, "--repo", repoName)
		if err != nil {
			// The registry (and the CLI's own pre-check) refuse duplicates; in a
			// bulk run that is idempotent success, not an error.
			if strings.Contains(out, "already exists") {
				existed = append(existed, repoName)
				continue
			}
			return fail("publish to "+repoName, fmt.Errorf("%w\n%s", err, errorExcerpt(out)))
		}
		pushed = append(pushed, repoName)
	}

	if len(pushed) == 0 {
		result.Action = ActionSkippedExists
		result.Detail = "already in " + strings.Join(existed, ", ")
		return result
	}
	result.Action = ActionPublished
	if noDeps {
		result.Action = ActionPublishedNoDeps
	}
	if len(r.RepoNames) > 1 {
		result.Detail = "published to " + strings.Join(pushed, ", ")
		if len(existed) > 0 {
			result.Detail += "; already in " + strings.Join(existed, ", ")
		}
	}
	return result
}

// autoBuildFrontendAssets compiles the app's JavaScript frontend when the catalog
// entry declares no build script of its own.
//
// It delegates to internal/frontend so the mirror, `fpm package` and anything else
// build these apps identically. That matters: this used to build every directory that
// had a build script, which for frappe/crm meant the root (whose script is `cd
// frontend && yarn build`) and frontend/ — the same Vite build twice — and it
// installed without forcing devDependencies, so a runner with NODE_ENV=production
// failed on crm's autoprefixer.
//
// It returns the tree to package from, which is the checkout itself unless the build
// had to be staged elsewhere, and a cleanup the caller must run after packaging —
// the build may have written a bench config next to the checkout or staged a copy of
// it, and neither is the workspace's to keep.
func (r *Runner) autoBuildFrontendAssets(appName, checkout string) (buildRoot string, cleanup func(), err error) {
	// Build-time dependencies first: another app's source this build reads off disk.
	// fpm resolves `required_apps` to pinned versions at packaging time and installs
	// them at install time, but neither puts a sibling app's tree where a build script
	// can `cd` into it. Pulling them is the same idea applied one stage earlier.
	if err := r.ensureBuildDependencies(appName, checkout); err != nil {
		return checkout, func() {}, err
	}

	res, buildErr := frontend.Build(frontend.BuildOptions{
		SourcePath: checkout,
		AppName:    appName,
		Stdout:     logWriter{r.Log},
		Env: []string{
			"npm_config_cache=" + r.Workspace.npmCache(),
			"YARN_CACHE_FOLDER=" + r.Workspace.yarnCache(),
		},
	})
	cleanup = res.Cleanup
	if cleanup == nil {
		cleanup = func() {}
	}
	if buildErr != nil {
		return checkout, cleanup, buildErr
	}
	if !res.Built {
		return checkout, cleanup, nil
	}
	r.Log("  frontend: %d file(s) in %s", res.Output.Files, strings.Join(res.Output.Dirs, ", "))
	// A frontend that needed a bench was built in a staged copy; packaging the
	// original checkout would ship none of what was just built.
	if res.BuildRoot != "" {
		return res.BuildRoot, cleanup, nil
	}
	return checkout, cleanup, nil
}

// ensureBuildDependencies checks out the bench apps this app's build reads off disk.
// A dependency that is not in the catalog is reported rather than guessed at, and one
// that is already present costs nothing.
func (r *Runner) ensureBuildDependencies(appName, checkout string) error {
	siblings, err := frontend.SiblingApps(checkout, appName)
	if err != nil || len(siblings) == 0 {
		return err
	}
	for _, ref := range siblings {
		// A reference may name a directory inside the app — "frappe/ui" — which is the
		// part the build actually compiles.
		slug, subdir, _ := strings.Cut(ref, "/")
		repoURL, ok := r.CatalogRepos[slug]
		if !ok {
			return fmt.Errorf("%s's build reads ../../%s off disk, but %q is not in the catalog, "+
				"so there is nowhere to fetch it from", appName, ref, slug)
		}
		depRef, pinned := r.BuildDepRefs[appName][slug]
		if !pinned {
			// No pin: the newest release. Right for a dependency that only supplies
			// source to read, wrong when the app is built against a specific line —
			// helpdesk's own CI sets FRAPPE_BRANCH=version-15 — which is what the
			// catalog's build_deps column is for.
			var err error
			if depRef, err = latestReleaseRef(repoURL); err != nil {
				return fmt.Errorf("resolving a ref for build dependency %s: %w", slug, err)
			}
		}
		how := "newest release"
		if pinned {
			how = "pinned by the catalog"
		}
		r.Log("  build dependency: %s at %s (%s; its source is read by %s's build)", slug, depRef, how, appName)
		dir, err := r.Workspace.EnsureBuildDependency(slug, repoURL, depRef)
		if err != nil {
			return err
		}
		// The consumer compiles this directory's source, so its own dependencies have
		// to be there. helpdesk's build installs them itself only when a marker file is
		// missing, which is not something to rely on — and the source it compiles
		// imports packages that only the dependency's own manifest lists.
		if subdir != "" {
			if err := r.installDependencyDeps(filepath.Join(dir, subdir), slug+"/"+subdir); err != nil {
				return err
			}
		}
	}
	return nil
}

// installDependencyDeps installs node dependencies for a directory inside a build
// dependency, so a consumer compiling its source can resolve what that source imports.
func (r *Runner) installDependencyDeps(dir, label string) error {
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err != nil {
		return nil
	}
	// Deliberately not skipped when node_modules is already present. It may have been
	// installed against a different ref of this dependency, or by the consumer's own
	// guard, and a tree that merely exists is not a tree that has what this ref's source
	// imports — which is exactly how @vueuse/core stayed missing. yarn --check-files and
	// pnpm both reconcile an existing tree, so running it again is cheap and honest.
	tool := "yarn"
	args := []string{"install", "--check-files", "--non-interactive", "--production=false"}
	if _, err := os.Stat(filepath.Join(dir, "pnpm-lock.yaml")); err == nil {
		tool, args = "pnpm", []string{"install", "--prod=false", "--no-frozen-lockfile"}
	}
	if _, err := exec.LookPath(tool); err != nil {
		return fmt.Errorf("build dependency %s needs %s to install its own dependencies: %w", label, tool, err)
	}

	r.Log("  build dependency: installing %s dependencies (%s)", label, tool)
	cmd := exec.Command(tool, args...)
	cmd.Dir = dir
	cmd.Env = append(r.Workspace.BuildEnv(), "CI=false")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("installing %s dependencies failed: %w\n%s", label, err, tail(string(out), 1200))
	}
	return r.installPeerDeps(dir, label)
}

// installPeerDeps installs a build dependency's peerDependencies into its own tree.
//
// A peer dependency is by definition the consumer's to provide, and normally that is
// exactly what happens: helpdesk's desk depends on frappe-ui and supplies its peers from
// its own node_modules. But here the consumer compiles the dependency's *source* in
// place, and node resolution from apps/frappe/ui/src walks up through frappe/ui,
// frappe and apps — it never reaches apps/helpdesk/desk/node_modules. frappe/ui
// declares @vueuse/core as a peer, yarn does not install peers, and the build fails on
// an import the dependency's own manifest never asked to have installed.
//
// Standing in for the consumer means satisfying them here. --no-save keeps the
// dependency's manifest untouched: this is a build-time convenience, not a change to it.
func (r *Runner) installPeerDeps(dir, label string) error {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil
	}
	var pkg struct {
		PeerDependencies map[string]string `json:"peerDependencies"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil || len(pkg.PeerDependencies) == 0 {
		return nil
	}

	specs := make([]string, 0, len(pkg.PeerDependencies))
	for name, constraint := range pkg.PeerDependencies {
		specs = append(specs, name+"@"+constraint)
	}
	sort.Strings(specs)

	r.Log("  build dependency: satisfying %s peer dependencies (%s)", label, strings.Join(specs, " "))
	args := append([]string{"install", "--no-save", "--no-audit", "--no-fund", "--legacy-peer-deps"}, specs...)
	cmd := exec.Command("npm", args...)
	cmd.Dir = dir
	cmd.Env = append(r.Workspace.BuildEnv(), "CI=false")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("satisfying %s peer dependencies failed: %w\n%s", label, err, tail(string(out), 1200))
	}
	return nil
}

// latestReleaseRef is the newest release tag of a repository, or its default branch when
// it publishes none. A build-time dependency supplies source to read — frappe's ui
// package, say — so the newest release is the right default; it is not a package whose
// version is pinned into anything.
func latestReleaseRef(repoURL string) (string, error) {
	tags, err := ListRemoteTags(repoURL)
	if err != nil {
		return "", err
	}
	best := ""
	for _, tag := range tags {
		if best == "" || semver.Compare(NormalizeVersion(tag.Name), NormalizeVersion(best)) > 0 {
			best = tag.Name
		}
	}
	if best == "" {
		return "origin/HEAD", nil
	}
	return "refs/tags/" + best, nil
}

// logWriter adapts a printf-style logger to io.Writer so the frontend build's
// progress lines land in the mirror's log.
type logWriter struct {
	log func(format string, args ...any)
}

func (w logWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line != "" {
			w.log("  %s", line)
		}
	}
	return len(p), nil
}

// packageApp builds the archive into a fresh temporary directory, so the one
// .fpm file in it is the artifact — no reconstruction of the file name from
// the repo name, which is the prototype bug this tool replaces.
func (r *Runner) packageApp(item BuildItem, checkout string) (artifact string, noDeps bool, err error) {
	tmpOut, err := os.MkdirTemp("", "fpm-mirror-*")
	if err != nil {
		return "", false, err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(tmpOut)
		}
	}()

	args := []string{
		"package", checkout,
		"--version", item.Version,
		"--org", Org,
		"--output-path", tmpOut,
		"--overwrite",
		"--skip-local-install",
	}
	if item.AppName != "" {
		args = append(args, "--app-name", item.AppName)
	}
	if !item.BundleDeps {
		args = append(args, "--bundle-deps=false")
	} else {
		if r.PythonVersion != "" {
			args = append(args, "--python-version", r.PythonVersion)
		}
		for _, p := range r.Platforms {
			args = append(args, "--platform", p)
		}
	}

	out, err := r.fpm(args...)
	if err != nil && item.BundleDeps {
		// Wheel vendoring is the usual culprit (a dependency publishing no
		// wheel for the target platform). A package without bundled wheels
		// still installs — pip resolves at install time — so retry once and
		// let the report say so.
		r.Log("  package failed, retrying without bundled wheels: %s", firstLine(errorExcerpt(out)))
		out, err = r.fpm(append(args, "--bundle-deps=false")...)
		noDeps = err == nil
	}
	if err != nil {
		return "", false, fmt.Errorf("%w\n%s", err, errorExcerpt(out))
	}

	matches, err := filepath.Glob(filepath.Join(tmpOut, "*.fpm"))
	if err != nil || len(matches) != 1 {
		return "", false, fmt.Errorf("expected exactly one .fpm in %s, found %d", tmpOut, len(matches))
	}
	return matches[0], noDeps, nil
}

func (r *Runner) runBuildScript(script, checkout string) error {
	cmd := exec.Command("bash", script)
	cmd.Dir = checkout
	cmd.Env = r.Workspace.BuildEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w\n%s", script, err, tail(string(out), 1200))
	}
	if _, err := os.Stat(filepath.Join(checkout, "compiled_assets")); err != nil {
		return fmt.Errorf("%s completed but left no compiled_assets/ directory", script)
	}
	return nil
}

func (r *Runner) fpm(args ...string) (string, error) {
	cmd := exec.Command(r.FPMBin, args...)
	cmd.Env = r.Workspace.BuildEnv()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// errorExcerpt pulls the error lines out of subprocess output. A failing
// cobra command appends its whole usage text, which would otherwise be all a
// bounded excerpt ever showed.
func errorExcerpt(out string) string {
	var picked []string
	for _, line := range strings.Split(out, "\n") {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "error") || strings.Contains(lower, "failed") ||
			strings.Contains(lower, "no such file") || strings.Contains(lower, "no matching distribution") {
			picked = append(picked, strings.TrimSpace(line))
			if len(picked) == 6 {
				break
			}
		}
	}
	if len(picked) == 0 {
		return tail(out, 1200)
	}
	return strings.Join(picked, "\n")
}

func moveFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	// Rename fails across filesystems (temp dir vs output dir); fall back to copy.
	if err := utils.CopyRegularFile(src, dst, 0o644); err != nil {
		return err
	}
	return os.Remove(src)
}
