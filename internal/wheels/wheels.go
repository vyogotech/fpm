// Package wheels bundles a Frappe app's Python dependencies into the .fpm package, as
// wheels, so that installing it does not require reaching PyPI.
//
// Without bundled dependencies, `fpm install` runs a plain `pip install -e`, which
// resolves requirements.txt over the network. That makes an install impossible on an
// air-gapped host, and means two installs months apart can resolve different dependency
// versions. The bundled wheel set pins the resolved dependency graph by construction.
package wheels

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DirName is the directory, at the root of the package, holding bundled wheels.
const DirName = "wheels"

// RequirementsFileName is the dependency manifest wheels are resolved from.
const RequirementsFileName = "requirements.txt"

// DefaultProdPlatform is the wheel platform tag used for production packages.
// Frappe deployments target Linux on amd64, which is rarely the machine doing the
// packaging, so prod packages cross-build for it rather than for the host.
const DefaultProdPlatform = "manylinux2014_x86_64"

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
// For the packaging host (empty platform) it uses `pip wheel`, which can build wheels
// from source distributions when a dependency publishes no wheel. For a cross-target
// platform it uses `pip download --only-binary=:all:`, since building from source for a
// foreign platform is not possible; a dependency without a wheel for that platform is a
// hard error rather than a silent host-tagged artifact.
func BuildCommand(pythonExe, requirementsPath, destDir, platform string) Command {
	if platform == "" {
		return Command{
			Name: pythonExe,
			Args: []string{
				"-m", "pip", "wheel",
				"-r", requirementsPath,
				"-w", destDir,
			},
		}
	}

	return Command{
		Name: pythonExe,
		Args: []string{
			"-m", "pip", "download",
			"-r", requirementsPath,
			"-d", destDir,
			"--platform", platform,
			"--only-binary=:all:",
		},
	}
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

// Bundle resolves the dependencies an app declares in appDir into destDir as wheels,
// reporting whether anything was bundled.
//
// Dependencies are read from both requirements.txt and pyproject.toml, so apps on either
// convention are handled without the caller having to know which one is in use.
//
// An app that declares no dependencies is not an error: there is simply nothing to
// bundle, and no wheels directory is created.
func Bundle(appDir, destDir, platform string) (bundled bool, err error) {
	req, err := Collect(appDir)
	if err != nil {
		return false, err
	}
	if req.Empty() {
		fmt.Printf("No Python dependencies declared in %s or %s; skipping dependency bundling.\n",
			RequirementsFileName, PyProjectFileName)
		return false, nil
	}

	pythonExe, err := FindPython()
	if err != nil {
		return false, err
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return false, fmt.Errorf("failed to create wheels directory %s: %w", destDir, err)
	}

	// Resolve every manifest in one pip invocation so a dependency constrained in both
	// places is resolved once, rather than the second run overwriting the first.
	mergedPath := filepath.Join(destDir, "fpm-requirements.txt")
	if err := writeMergedRequirements(req, mergedPath); err != nil {
		os.RemoveAll(destDir)
		return false, err
	}
	// The merged file is an input to pip, not part of the shipped package.
	defer os.Remove(mergedPath)

	fmt.Printf("Collected %d dependency specifier(s) from %s.\n", len(req.Specs), req.Describe())
	cmd := BuildCommand(pythonExe, mergedPath, destDir, platform)
	target := platform
	if target == "" {
		target = "packaging host"
	}
	fmt.Printf("Bundling Python dependencies for %s...\n  %s\n", target, cmd)

	execCmd := exec.Command(cmd.Name, cmd.Args...)
	output, runErr := execCmd.CombinedOutput()
	if runErr != nil {
		// Leave no half-populated wheels directory behind: a partial set would produce a
		// package that looks self-contained but fails to install offline.
		os.RemoveAll(destDir)
		hint := "pass --bundle-deps=false to package without bundling dependencies"
		if platform != "" {
			hint = "a dependency may publish no wheel for " + platform +
				"; try --platform for a different target, or " + hint
		}
		return false, fmt.Errorf("failed to bundle dependencies for %s:\n%s\n%s\n%w",
			target, string(output), hint, runErr)
	}

	count, err := countWheels(destDir)
	if err != nil {
		return false, err
	}
	if count == 0 {
		os.RemoveAll(destDir)
		fmt.Printf("Warning: no dependencies were bundled from %s; package will install online.\n", req.Describe())
		return false, nil
	}

	fmt.Printf("Bundled %d dependency file(s) into %s/\n", count, DirName)
	return true, nil
}

// countWheels reports how many distribution files landed in dir.
func countWheels(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("failed to read wheels directory %s: %w", dir, err)
	}
	count := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".whl", ".gz", ".zip": // .tar.gz sdists count as vendored distributions too
			count++
		}
	}
	return count, nil
}
