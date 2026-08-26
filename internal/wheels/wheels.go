// Package wheels bundles a Frappe app's Python dependencies into the .fpm package, as
// wheels, so that installing it does not require reaching PyPI.
//
// Without bundled dependencies, `fpm install` runs a plain `pip install -e`, which
// resolves requirements.txt over the network. That makes an install impossible on an
// air-gapped host, and means two installs months apart can resolve different dependency
// versions. The bundled wheel set pins the resolved dependency graph by construction,
// and the lock file written next to it records that graph in a readable form.
package wheels

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// DirName is the directory, at the root of the package, holding bundled wheels.
const DirName = "wheels"

// RequirementsFileName is the dependency manifest wheels are resolved from.
const RequirementsFileName = "requirements.txt"

// LockFileName is written into DirName alongside the wheels. It lists every
// distribution that was vendored as name==version, one per line, so the exact
// dependency set a package installs can be read without unpacking wheel filenames.
const LockFileName = "fpm-lock.txt"

// DefaultProdPlatform is the wheel platform tag used for production packages.
// Frappe deployments target Linux on amd64, which is rarely the machine doing the
// packaging, so prod packages cross-build for it rather than for the host.
const DefaultProdPlatform = "manylinux2014_x86_64"

// DefaultImplementation is the interpreter implementation tag wheels are resolved
// for when cross-building: CPython.
const DefaultImplementation = "cp"

// HostPlatformTag is recorded in metadata when wheels were built for the packaging
// host, whose exact tag pip determines at build time.
const HostPlatformTag = "host"

// Target describes the interpreter and platform wheels are resolved for.
//
// The zero value is the packaging host: pip resolves for its own interpreter and can
// build wheels from source. Any other target is a cross-build, where every value must
// be given explicitly: the destination bench's platform and Python version are facts
// about the destination, and guessing them from the packaging host is exactly how a
// package ends up with wheels pip rejects at install time, when there is no network
// to fall back to.
type Target struct {
	// Platforms are pip --platform tags, e.g. manylinux2014_x86_64. More than one
	// may be given when the destination accepts several (pip picks the best match
	// per dependency). Empty means the packaging host.
	Platforms []string
	// PythonVersion is the destination interpreter version, e.g. "3.11". Required
	// for a cross-build.
	PythonVersion string
	// Implementation is the interpreter implementation tag; defaults to "cp".
	Implementation string
	// ABIs optionally restricts wheel ABI tags (e.g. cp311, abi3). When empty pip
	// derives them from PythonVersion and Implementation.
	ABIs []string
}

// IsHost reports whether the target is the packaging host.
func (t Target) IsHost() bool { return len(t.Platforms) == 0 }

// Tag renders the target's platform for metadata: HostPlatformTag for the host,
// otherwise the platform tags joined with commas.
func (t Target) Tag() string {
	if t.IsHost() {
		return HostPlatformTag
	}
	return strings.Join(t.Platforms, ",")
}

// Describe names the target for progress output.
func (t Target) Describe() string {
	if t.IsHost() {
		return "packaging host"
	}
	impl := t.Implementation
	if impl == "" {
		impl = DefaultImplementation
	}
	return fmt.Sprintf("%s (%s%s)", strings.Join(t.Platforms, ","), impl, t.PythonVersion)
}

var pythonVersionPattern = regexp.MustCompile(`^\d+\.\d+$`)

// Validate rejects a target that pip could only complete by guessing.
func (t Target) Validate() error {
	if t.IsHost() {
		if t.PythonVersion != "" {
			return fmt.Errorf("--python-version %s only applies with --platform: a host build uses the packaging host's own interpreter", t.PythonVersion)
		}
		return nil
	}
	for _, p := range t.Platforms {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("--platform must not be empty")
		}
	}
	if t.PythonVersion == "" {
		return fmt.Errorf("--python-version is required when vendoring wheels for %s: "+
			"give the destination bench's interpreter version (e.g. 3.11) so pip resolves wheels for it, "+
			"not for the packaging host's interpreter", strings.Join(t.Platforms, ","))
	}
	if !pythonVersionPattern.MatchString(t.PythonVersion) {
		return fmt.Errorf("--python-version %q must be MAJOR.MINOR, e.g. 3.11", t.PythonVersion)
	}
	return nil
}

// ParseTag parses a metadata wheel_platform value back into the platform list.
func ParseTag(tag string) []string {
	if tag == "" || tag == HostPlatformTag {
		return nil
	}
	var out []string
	for _, p := range strings.Split(tag, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// DefaultBundleForPackageType reports whether a package type bundles its dependencies
// when the user has not said either way. A production package is a deployment artifact,
// so it is self-contained by default; a development package is for local iteration,
// where bundling only slows the loop and host-built wheels are not worth shipping.
func DefaultBundleForPackageType(packageType string) bool {
	return packageType == "prod"
}

// PlatformForPackageType returns the wheel platform tag to bundle for.
// Production packages default to amd64 Linux; anything else builds for the packaging
// host, which is signalled by an empty platform tag.
func PlatformForPackageType(packageType string) string {
	if packageType == "prod" {
		return DefaultProdPlatform
	}
	return ""
}

// Command describes the pip invocation used to bundle wheels. It is built separately
// from being run so the argument construction can be tested without pip present.
type Command struct {
	Name string
	Args []string
}

func (c Command) String() string {
	return strings.TrimSpace(c.Name + " " + strings.Join(c.Args, " "))
}

// BuildCommand returns the pip invocation that populates destDir from requirementsPath.
//
// For the packaging host it uses `pip wheel`, which can build wheels from source
// distributions when a dependency publishes no wheel. For a cross-target it uses
// `pip download --only-binary=:all:` with the platform, Python version and
// implementation all pinned, since building from source for a foreign platform is
// not possible; a dependency without a wheel for that target is a hard error rather
// than a silent host-tagged artifact.
func BuildCommand(pythonExe, requirementsPath, destDir string, target Target) Command {
	if target.IsHost() {
		return Command{
			Name: pythonExe,
			Args: []string{
				"-m", "pip", "wheel",
				"-r", requirementsPath,
				"-w", destDir,
			},
		}
	}

	args := []string{
		"-m", "pip", "download",
		"-r", requirementsPath,
		"-d", destDir,
		"--only-binary=:all:",
	}
	for _, p := range target.Platforms {
		args = append(args, "--platform", p)
	}
	impl := target.Implementation
	if impl == "" {
		impl = DefaultImplementation
	}
	args = append(args, "--python-version", target.PythonVersion, "--implementation", impl)
	for _, abi := range target.ABIs {
		args = append(args, "--abi", abi)
	}
	return Command{Name: pythonExe, Args: args}
}

// FindPython locates an interpreter to drive pip with, preferring python3.
func FindPython() (string, error) {
	for _, candidate := range []string{"python3", "python"} {
		if path, err := exec.LookPath(candidate); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("no python interpreter found on PATH (tried python3, python); " +
		"bundling dependencies requires python with pip available, " +
		"or pass --bundle-deps=false to package without them")
}

// Pin is one vendored distribution.
type Pin struct {
	Name    string
	Version string
	File    string
}

func (p Pin) String() string { return p.Name + "==" + p.Version }

// Result reports what Bundle vendored.
type Result struct {
	// Bundled is false when the app declares no dependencies and nothing was done.
	Bundled bool
	// Pins lists every vendored distribution, sorted by name.
	Pins []Pin
}

// Bundle resolves the dependencies an app declares in appDir into destDir as wheels.
//
// Dependencies are read from both requirements.txt and pyproject.toml — including
// [build-system].requires, since the offline install performs a PEP 517 build — so
// apps on either convention are handled without the caller having to know which one
// is in use. Every resolved distribution is recorded in destDir/fpm-lock.txt.
//
// An app that declares no dependencies is not an error: there is simply nothing to
// bundle, and no wheels directory is created. A dependency that cannot be satisfied
// for the target is an error, and no partial wheels directory is left behind.
func Bundle(appDir, destDir string, target Target) (Result, error) {
	req, err := Collect(appDir)
	if err != nil {
		return Result{}, err
	}
	if req.Empty() {
		fmt.Printf("No Python dependencies declared in %s or %s; skipping dependency bundling.\n",
			RequirementsFileName, PyProjectFileName)
		return Result{}, nil
	}
	// Only an app with something to vendor needs a complete target; checked after
	// Collect so an app with no dependencies packages without any platform flags.
	if err := target.Validate(); err != nil {
		return Result{}, err
	}

	pythonExe, err := FindPython()
	if err != nil {
		return Result{}, err
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return Result{}, fmt.Errorf("failed to create wheels directory %s: %w", destDir, err)
	}

	// Resolve every manifest in one pip invocation so a dependency constrained in both
	// places is resolved once, rather than the second run overwriting the first.
	mergedPath := filepath.Join(destDir, "fpm-requirements.txt")
	if err := writeMergedRequirements(req, mergedPath); err != nil {
		os.RemoveAll(destDir)
		return Result{}, err
	}
	// The merged file is an input to pip, not part of the shipped package.
	defer os.Remove(mergedPath)

	fmt.Printf("Collected %d dependency specifier(s) from %s.\n", len(req.Specs), req.Describe())
	cmd := BuildCommand(pythonExe, mergedPath, destDir, target)
	fmt.Printf("Bundling Python dependencies for %s...\n  %s\n", target.Describe(), cmd)

	// A requirement that publishes no wheel for the target (or no wheel at all —
	// googlemaps, for one, ships only an sdist) cannot be cross-downloaded. Its sdist
	// is built on the packaging host instead, and accepted only when the result is a
	// universal wheel: pure Python is the same on every platform, while a wheel with
	// compiled code built here would be wrong for the destination and is refused.
	// The target resolution is then re-run with the built wheel offered through
	// --find-links, so the requirement's own dependencies resolve for the target.
	var builtFromSdist []Pin
	attempted := map[string]bool{}
	var output []byte
	var runErr error
	for attempt := 0; ; attempt++ {
		args := cmd.Args
		if attempt > 0 {
			args = append(append([]string(nil), cmd.Args...), "--find-links", destDir)
		}
		execCmd := exec.Command(cmd.Name, args...)
		output, runErr = execCmd.CombinedOutput()
		if runErr == nil {
			break
		}
		spec := unsatisfiedRequirement(string(output))
		if target.IsHost() || spec == "" || attempted[spec] || attempt >= 16 {
			// Leave no half-populated wheels directory behind: a partial set would produce a
			// package that looks self-contained but fails to install offline.
			os.RemoveAll(destDir)
			hint := "pass --bundle-deps=false to package without bundling dependencies"
			if !target.IsHost() {
				hint = "a dependency may publish no wheel for " + target.Describe() +
					"; check the platform tags and Python version match the destination bench, or " + hint
			}
			return Result{}, fmt.Errorf("failed to bundle dependencies for %s:\n%s\n%s\n%w",
				target.Describe(), string(output), hint, runErr)
		}
		attempted[spec] = true
		fmt.Printf("No wheel for %s on %s; building its sdist on the packaging host (accepted only if pure Python)...\n",
			spec, target.Describe())
		pin, buildErr := buildUniversalWheelFromSdist(pythonExe, spec, destDir)
		if buildErr != nil {
			os.RemoveAll(destDir)
			return Result{}, fmt.Errorf("failed to bundle dependencies for %s: %w", target.Describe(), buildErr)
		}
		fmt.Printf("  built %s (universal wheel) from sdist\n", pin.File)
		builtFromSdist = append(builtFromSdist, pin)
	}

	files, err := listDistributions(destDir)
	if err != nil {
		return Result{}, err
	}
	if len(files) == 0 {
		os.RemoveAll(destDir)
		fmt.Printf("Warning: no dependencies were bundled from %s; package will install online.\n", req.Describe())
		return Result{}, nil
	}

	pins := PinsFromFiles(files)
	if err := writeLock(filepath.Join(destDir, LockFileName), target, req, pins, builtFromSdist); err != nil {
		os.RemoveAll(destDir)
		return Result{}, err
	}

	fmt.Printf("Bundled %d dependency file(s) into %s/ (pinned in %s/%s)\n", len(files), DirName, DirName, LockFileName)
	return Result{Bundled: true, Pins: pins}, nil
}

// listDistributions returns the distribution files in dir, sorted.
func listDistributions(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read wheels directory %s: %w", dir, err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if isDistribution(e.Name()) {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	return files, nil
}

func isDistribution(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".whl", ".gz", ".zip": // .tar.gz sdists count as vendored distributions too
		return true
	}
	return false
}

// countWheels reports how many distribution files landed in dir.
func countWheels(dir string) (int, error) {
	files, err := listDistributions(dir)
	if err != nil {
		return 0, err
	}
	return len(files), nil
}

// PinsFromFiles derives name==version pins from distribution filenames, sorted by
// name. Files that do not parse are skipped: they cannot be pinned, but pip will
// still consider them at install time.
func PinsFromFiles(files []string) []Pin {
	var pins []Pin
	for _, f := range files {
		if pin, ok := ParseDistributionFilename(f); ok {
			pins = append(pins, pin)
		}
	}
	sort.Slice(pins, func(i, j int) bool {
		if pins[i].Name != pins[j].Name {
			return pins[i].Name < pins[j].Name
		}
		return pins[i].Version < pins[j].Version
	})
	return pins
}

// ParseDistributionFilename extracts the project name and version from a wheel
// ({name}-{version}[-{build}]-{python}-{abi}-{platform}.whl) or sdist
// ({name}-{version}.tar.gz / .zip) filename.
func ParseDistributionFilename(file string) (Pin, bool) {
	base := filepath.Base(file)
	lower := strings.ToLower(base)
	switch {
	case strings.HasSuffix(lower, ".whl"):
		parts := strings.Split(strings.TrimSuffix(base, filepath.Ext(base)), "-")
		if len(parts) < 5 {
			return Pin{}, false
		}
		return Pin{Name: normalizeName(parts[0]), Version: parts[1], File: base}, true
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".zip"):
		stem := base
		if strings.HasSuffix(lower, ".tar.gz") {
			stem = base[:len(base)-len(".tar.gz")]
		} else {
			stem = base[:len(base)-len(".zip")]
		}
		i := strings.LastIndex(stem, "-")
		if i <= 0 || i == len(stem)-1 {
			return Pin{}, false
		}
		return Pin{Name: normalizeName(stem[:i]), Version: stem[i+1:], File: base}, true
	}
	return Pin{}, false
}

// normalizeName applies PEP 503 normalisation so a pin reads the same whether the
// file spelled the project with hyphens, underscores or dots.
func normalizeName(name string) string {
	var b strings.Builder
	prevSep := false
	for _, r := range strings.ToLower(name) {
		if r == '-' || r == '_' || r == '.' {
			if !prevSep {
				b.WriteByte('-')
			}
			prevSep = true
			continue
		}
		prevSep = false
		b.WriteRune(r)
	}
	return b.String()
}

// unsatisfiedRequirement extracts the requirement pip could not satisfy from its
// output ("No matching distribution found for <spec>"), or "" when the failure was
// something else.
func unsatisfiedRequirement(pipOutput string) string {
	m := noMatchingDistribution.FindStringSubmatch(pipOutput)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

var noMatchingDistribution = regexp.MustCompile(`(?m)No matching distribution found for (.+?)\s*$`)

// IsUniversalWheel reports whether a wheel filename carries the none-any tags, i.e.
// it contains no compiled code and installs on any platform and interpreter.
func IsUniversalWheel(file string) bool {
	base := strings.TrimSuffix(filepath.Base(file), ".whl")
	parts := strings.Split(base, "-")
	if len(parts) < 5 {
		return false
	}
	return parts[len(parts)-2] == "none" && parts[len(parts)-1] == "any"
}

// buildUniversalWheelFromSdist downloads the sdist that satisfies spec, builds it
// on the packaging host, and moves the wheel into destDir when — and only when — it
// is universal.
func buildUniversalWheelFromSdist(pythonExe, spec, destDir string) (Pin, error) {
	tmp, err := os.MkdirTemp("", "fpm-sdist-")
	if err != nil {
		return Pin{}, err
	}
	defer os.RemoveAll(tmp)

	dl := exec.Command(pythonExe, "-m", "pip", "download", "--no-deps", "--no-binary", ":all:", "-d", tmp, spec)
	if out, err := dl.CombinedOutput(); err != nil {
		return Pin{}, fmt.Errorf("%s has no wheel for the target and its sdist could not be downloaded:\n%s\n%w", spec, strings.TrimSpace(string(out)), err)
	}
	entries, _ := os.ReadDir(tmp)
	var sdist string
	for _, e := range entries {
		if !e.IsDir() && isDistribution(e.Name()) && !strings.HasSuffix(e.Name(), ".whl") {
			sdist = filepath.Join(tmp, e.Name())
		}
	}
	if sdist == "" {
		return Pin{}, fmt.Errorf("%s: pip downloaded no source distribution", spec)
	}

	builtDir := filepath.Join(tmp, "built")
	bw := exec.Command(pythonExe, "-m", "pip", "wheel", "--no-deps", "-w", builtDir, sdist)
	if out, err := bw.CombinedOutput(); err != nil {
		return Pin{}, fmt.Errorf("%s has no wheel for the target and building its sdist failed:\n%s\n%w", spec, strings.TrimSpace(string(out)), err)
	}
	built, _ := os.ReadDir(builtDir)
	var wheel string
	for _, e := range built {
		if strings.HasSuffix(e.Name(), ".whl") {
			wheel = e.Name()
		}
	}
	if wheel == "" {
		return Pin{}, fmt.Errorf("%s: building the sdist produced no wheel", spec)
	}
	if !IsUniversalWheel(wheel) {
		return Pin{}, fmt.Errorf("%s has no wheel for the target, and its source builds a platform-specific wheel (%s) "+
			"that would be wrong for the destination; it needs a published wheel for the target platform", spec, wheel)
	}
	if err := os.Rename(filepath.Join(builtDir, wheel), filepath.Join(destDir, wheel)); err != nil {
		// Cross-device: copy instead.
		data, rerr := os.ReadFile(filepath.Join(builtDir, wheel))
		if rerr != nil {
			return Pin{}, rerr
		}
		if werr := os.WriteFile(filepath.Join(destDir, wheel), data, 0o644); werr != nil {
			return Pin{}, werr
		}
	}
	pin, _ := ParseDistributionFilename(wheel)
	return pin, nil
}

// writeLock records the vendored set. The file is part of the package, so it is
// deterministic for a given input: sorted pins, no timestamps.
func writeLock(path string, target Target, req Requirements, pins []Pin, builtFromSdist []Pin) error {
	var b strings.Builder
	b.WriteString("# Generated by fpm. Every distribution vendored into " + DirName + "/, resolved from " + req.Describe() + ".\n")
	b.WriteString("# target-platform: " + target.Tag() + "\n")
	if target.PythonVersion != "" {
		b.WriteString("# target-python: " + target.PythonVersion + "\n")
	}
	for _, p := range builtFromSdist {
		b.WriteString("# built-from-sdist (universal wheel, no wheel published for the target): " + p.String() + "\n")
	}
	for _, p := range pins {
		b.WriteString(p.String() + "\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	return nil
}

// ReadLock parses a lock file's pins. A missing file yields no pins and no error, so
// packages built before the lock existed still report their bundled wheels by name.
func ReadLock(path string) ([]Pin, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}
	return ParseLock(string(data)), nil
}

// ParseLock parses lock file content.
func ParseLock(content string) []Pin {
	var pins []Pin
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, version, ok := strings.Cut(line, "==")
		if !ok {
			continue
		}
		pins = append(pins, Pin{Name: name, Version: version})
	}
	return pins
}
