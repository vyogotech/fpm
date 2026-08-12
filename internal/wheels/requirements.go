package wheels

import (
	"archive/zip"
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// PyProjectFileName is the PEP 621 project manifest. Frappe apps on v15 and later
// declare their dependencies here rather than in requirements.txt.
const PyProjectFileName = "pyproject.toml"

// pyProject models the parts of pyproject.toml that determine what must be available at
// install time: the app's runtime dependencies, and the backend needed to build it.
//
// Optional extras are deliberately not read, since pip does not install them by default.
type pyProject struct {
	Project struct {
		Dependencies []string `toml:"dependencies"`
	} `toml:"project"`
	BuildSystem struct {
		Requires []string `toml:"requires"`
	} `toml:"build-system"`
}

// Requirements is the set of dependency specifiers an app declares, along with the
// manifests they came from.
type Requirements struct {
	Specs   []string
	Sources []string
}

// Empty reports whether the app declares no dependencies to bundle.
func (r Requirements) Empty() bool { return len(r.Specs) == 0 }

// Describe names the manifests the specifiers were read from, for progress output.
func (r Requirements) Describe() string {
	if len(r.Sources) == 0 {
		return "no dependency manifest"
	}
	return strings.Join(r.Sources, " and ")
}

// Collect reads the dependency specifiers an app declares in appDir.
//
// Both requirements.txt and pyproject.toml are read, because Frappe apps span both
// conventions and an app mid-migration may carry both. Specifiers are merged and
// de-duplicated; genuinely conflicting constraints are left for pip to reject, rather
// than being silently resolved here.
//
// A missing manifest is not an error. An app with neither simply has nothing to bundle.
func Collect(appDir string) (Requirements, error) {
	var req Requirements
	seen := make(map[string]bool)

	add := func(spec string) {
		if spec == "" || seen[spec] {
			return
		}
		seen[spec] = true
		req.Specs = append(req.Specs, spec)
	}

	reqTxtPath := filepath.Join(appDir, RequirementsFileName)
	txtSpecs, err := parseRequirementsTxt(reqTxtPath)
	if err != nil {
		return Requirements{}, err
	}
	if len(txtSpecs) > 0 {
		req.Sources = append(req.Sources, RequirementsFileName)
		for _, s := range txtSpecs {
			add(s)
		}
	}

	pyProjectPath := filepath.Join(appDir, PyProjectFileName)
	tomlSpecs, err := parsePyProject(pyProjectPath)
	if err != nil {
		return Requirements{}, err
	}
	if len(tomlSpecs) > 0 {
		req.Sources = append(req.Sources, PyProjectFileName)
		for _, s := range tomlSpecs {
			add(s)
		}
	}

	return req, nil
}

// CollectFromArchive reads the dependency specifiers declared inside an .fpm package,
// without extracting it. It also reports the wheels the package bundles, so a caller can
// show what an install would actually resolve from.
func CollectFromArchive(fpmPath string) (req Requirements, bundledWheels []string, err error) {
	r, err := zip.OpenReader(fpmPath)
	if err != nil {
		return Requirements{}, nil, fmt.Errorf("failed to open FPM package %s: %w", fpmPath, err)
	}
	defer r.Close()

	seen := make(map[string]bool)
	add := func(spec string) {
		if spec == "" || seen[spec] {
			return
		}
		seen[spec] = true
		req.Specs = append(req.Specs, spec)
	}

	readEntry := func(f *zip.File) ([]byte, error) {
		rc, openErr := f.Open()
		if openErr != nil {
			return nil, fmt.Errorf("failed to open %s in %s: %w", f.Name, fpmPath, openErr)
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}

	var reqTxt, pyProjectData []byte
	for _, f := range r.File {
		switch {
		case f.Name == RequirementsFileName:
			if reqTxt, err = readEntry(f); err != nil {
				return Requirements{}, nil, err
			}
		case f.Name == PyProjectFileName:
			if pyProjectData, err = readEntry(f); err != nil {
				return Requirements{}, nil, err
			}
		case strings.HasPrefix(f.Name, DirName+"/") && !strings.HasSuffix(f.Name, "/"):
			bundledWheels = append(bundledWheels, path.Base(f.Name))
		}
	}

	if specs := parseRequirementsTxtBytes(reqTxt); len(specs) > 0 {
		req.Sources = append(req.Sources, RequirementsFileName)
		for _, s := range specs {
			add(s)
		}
	}
	if len(pyProjectData) > 0 {
		specs, parseErr := parsePyProjectBytes(pyProjectData)
		if parseErr != nil {
			return Requirements{}, nil, parseErr
		}
		if len(specs) > 0 {
			req.Sources = append(req.Sources, PyProjectFileName)
			for _, s := range specs {
				add(s)
			}
		}
	}

	sort.Strings(bundledWheels)
	return req, bundledWheels, nil
}

// parseRequirementsTxt reads dependency specifiers from a pip requirements file,
// dropping comments and blank lines.
//
// Lines that direct pip elsewhere (-r/-c includes, --index-url and friends) are passed
// through untouched, so an app that splits its requirements across files still resolves
// correctly.
func parseRequirementsTxt(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %w", path, err)
	}
	return parseRequirementsTxtBytes(data), nil
}

// parseRequirementsTxtBytes parses requirements content already held in memory, so the
// same rules apply whether it came from disk or from inside an .fpm archive.
func parseRequirementsTxtBytes(data []byte) []string {
	var specs []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Strip trailing inline comments, which pip allows after whitespace.
		if idx := strings.Index(line, " #"); idx != -1 {
			line = strings.TrimSpace(line[:idx])
		}
		if line != "" {
			specs = append(specs, line)
		}
	}
	return specs
}

// parsePyProject reads the dependencies pip needs at install time from pyproject.toml.
//
// This is `[project].dependencies` plus `[build-system].requires`. The build backend is
// included because `fpm install` runs `pip install -e`, which performs a PEP 517 build of
// the app on the target machine. With pip pinned to the bundled wheels, a backend that is
// not bundled cannot be fetched, and the offline install fails before it starts.
func parsePyProject(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}
	return parsePyProjectBytes(data)
}

// parsePyProjectBytes parses pyproject.toml content already held in memory.
func parsePyProjectBytes(data []byte) ([]string, error) {
	var parsed pyProject
	if _, err := toml.Decode(string(data), &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", PyProjectFileName, err)
	}

	var specs []string
	for _, dep := range parsed.Project.Dependencies {
		if dep = strings.TrimSpace(dep); dep != "" {
			specs = append(specs, dep)
		}
	}
	for _, dep := range parsed.BuildSystem.Requires {
		if dep = strings.TrimSpace(dep); dep != "" {
			specs = append(specs, dep)
		}
	}
	return specs, nil
}

// writeMergedRequirements writes the collected specifiers to a pip requirements file so
// a single pip invocation resolves dependencies from every manifest at once.
func writeMergedRequirements(req Requirements, path string) error {
	var b strings.Builder
	b.WriteString("# Generated by fpm from " + req.Describe() + "\n")
	// Sorting keeps the generated file stable across runs for a given input set.
	specs := append([]string(nil), req.Specs...)
	sort.Strings(specs)
	for _, s := range specs {
		b.WriteString(s + "\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("failed to write merged requirements to %s: %w", path, err)
	}
	return nil
}
