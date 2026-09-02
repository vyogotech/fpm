package metadata

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"fpm/internal/semver"
)

// AppMetadata defines the structure of the app_metadata.json file
// that will be included in the .fpm package.
type AppMetadata struct {
	PackageName         string            `json:"package_name,omitempty"` // This might be the same as AppName or the repo name
	PackageVersion      string            `json:"package_version,omitempty"`
	Title               string            `json:"title,omitempty"`
	Description         string            `json:"description,omitempty"`
	Author              string            `json:"author,omitempty"`
	Publisher           string            `json:"publisher,omitempty"`
	Email               string            `json:"email,omitempty"`
	License             string            `json:"license,omitempty"`
	Icon                string            `json:"icon,omitempty"`
	IconFile            string            `json:"icon_file,omitempty"`
	Org                 string            `json:"org,omitempty"`                  // GitHub organization or similar
	AppName             string            `json:"app_name,omitempty"`             // The actual Frappe app name (e.g., erpnext)
	Dependencies        map[string]string `json:"dependencies,omitempty"`         // e.g., "erpnext": "13.2.1"
	FrappeCompatibility []string          `json:"frappe_compatibility,omitempty"` // e.g., ["13.x.x", "14.x.x"]
	Hooks               map[string]string `json:"hooks,omitempty"`                // e.g., "install_hooks": "install_hooks.py"
	SourceControlURL    string            `json:"source_control_url,omitempty"`
	PackageType         string            `json:"package_type,omitempty"`
	ContentChecksum     string            `json:"content_checksum,omitempty"`
	// WheelPlatform is the pip platform tag the bundled wheels/ directory was vendored
	// for, or "host" when built for the packaging machine. Empty means the package
	// bundles no wheels and installs its Python dependencies from the network.
	// Several tags may be joined with commas when the wheels satisfy more than one
	// (e.g. "manylinux2014_x86_64,manylinux_2_28_x86_64").
	WheelPlatform string `json:"wheel_platform,omitempty"`
	// WheelPythonVersion is the CPython version (e.g. "3.11") the bundled wheels were
	// resolved for. Empty when no wheels are bundled or they were built for the host.
	WheelPythonVersion string `json:"wheel_python_version,omitempty"`

	// CommitSHA is the exact git commit the package was built from, so external
	// caching and build-deduplication can key on a stable identity rather than a
	// branch or tag name that moves.
	CommitSHA string `json:"commit_sha,omitempty"`
	// GitRef is the branch or tag HEAD pointed at when packaging, for humans; it
	// is not stable and must not be used as a cache key.
	GitRef string `json:"git_ref,omitempty"`
	// GitDirty is set when the working tree had uncommitted changes to tracked
	// files, meaning CommitSHA alone does not reproduce the package.
	GitDirty bool `json:"git_dirty,omitempty"`

	// RequiredApps lists the Frappe apps this app's hooks.py declares in
	// `required_apps`, each resolved to a pinned package at packaging time. Frappe
	// itself is never listed: every bench provides it.
	RequiredApps []RequiredApp `json:"required_apps,omitempty"`

	// AssetsBuilt records that `fpm package` ran the bench asset build and the
	// package ships the built output under <app>/public/dist.
	AssetsBuilt bool `json:"assets_built,omitempty"`
	// AssetBundles maps each built bundle to its hashed path, as it will appear in
	// the bench's sites/assets/assets.json (and assets-rtl.json for rtl_ keys)
	// after install. It is what external tooling can read without unpacking.
	AssetBundles map[string]string `json:"asset_bundles,omitempty"`

	// FrontendBuilt records that `fpm package` compiled the app's JavaScript SPA
	// (the Vite project frappe/crm, frappe/helpdesk and friends ship) and that the
	// package carries its output. Unlike AssetsBuilt this covers the
	// <app>/public/frontend scheme, which frappe's esbuild never produces.
	FrontendBuilt bool `json:"frontend_built,omitempty"`
	// FrontendDirs are the compiled frontend output directories inside the package,
	// relative to the package root and sorted, e.g. ["crm/public/frontend"]. Each is
	// served at /assets/<app>/<name>/ through the sites/assets/<app> symlink. The
	// directory name is the app's choice, not a convention — erpnext's is
	// "erpnext/public/banking" — and an app may ship more than one.
	FrontendDirs []string `json:"frontend_dirs,omitempty"`
	// FrontendRoutes are the www templates the frontends are rendered from, relative
	// to the package root and sorted, e.g. ["crm/www/crm.html"]. Frappe's website
	// router serves each at its own path. Empty means no frontend has a route.
	FrontendRoutes []string `json:"frontend_routes,omitempty"`
	// FrontendSource is the directory the frontend was built from, relative to the
	// checkout root ("." for the root package.json, "frontend" for crm).
	FrontendSource string `json:"frontend_source,omitempty"`
}

// RequiredApp is one entry of hooks.py `required_apps`, resolved to a package.
type RequiredApp struct {
	// Name is the bare Frappe app name, e.g. "erpnext".
	Name string `json:"name"`
	// Org is the organisation the app was resolved under.
	Org string `json:"org,omitempty"`
	// Version is the exact package version the requirement resolved to when this
	// package was built. It is what the package was built and tested against; it
	// is only what an install must match when VersionSpec is empty.
	Version string `json:"version,omitempty"`
	// VersionSpec is the constraint an install has to satisfy, e.g.
	// ">=16.0.0-0,<17.0.0". Empty means the exact Version is required, which is
	// how packages built before ranges existed are read.
	//
	// It exists because pinning exactly made packages mutually uninstallable for
	// no real reason: two apps needing the same dependency, packaged a day apart,
	// pinned two different patch versions, and a bench holds one copy of an app.
	VersionSpec string `json:"version_spec,omitempty"`
	// Requirement is the raw hooks.py entry, e.g. "frappe/erpnext" or a git URL.
	Requirement string `json:"requirement,omitempty"`
	// ResolvedFrom names where the pin came from, so a package is auditable after
	// the fact: "local-store", "bench:<path>", "repo:<name>", or "flag:--requires"
	// when the packager stated it outright.
	ResolvedFrom string `json:"resolved_from,omitempty"`
	// ResolvedFromURL is the repository URL behind ResolvedFrom when the pin came
	// from a repository; a repository's local name alone does not identify it.
	ResolvedFromURL string `json:"resolved_from_url,omitempty"`
}

// Identifier renders the requirement as <org>/<name>==<version>, or
// <org>/<name><spec> when it accepts a range rather than one version.
func (r RequiredApp) Identifier() string {
	id := r.Name
	if r.Org != "" {
		id = r.Org + "/" + r.Name
	}
	if r.VersionSpec != "" {
		return id + r.VersionSpec
	}
	if r.Version != "" {
		id += "==" + r.Version
	}
	return id
}

// Constraint is the requirement's acceptance rule: its VersionSpec when it has
// one, otherwise an exact match on Version, otherwise "anything". A malformed
// spec falls back to the exact version rather than silently accepting anything.
func (r RequiredApp) Constraint() semver.Constraint {
	if r.VersionSpec != "" {
		if c, err := semver.ParseConstraint(r.VersionSpec); err == nil {
			return c
		}
	}
	if r.Version != "" {
		return semver.MustParseConstraint("==" + r.Version)
	}
	return semver.Constraint{}
}

// Accepts reports whether a version of the app satisfies this requirement.
func (r RequiredApp) Accepts(version string) bool {
	if r.VersionSpec == "" && r.Version == "" {
		// Packaged without a pin at all: any version present satisfies it.
		return true
	}
	if r.VersionSpec == "" {
		// Exact pins predate constraints and are compared as written, so a
		// version string that is not semver at all still matches itself.
		return version == r.Version || r.Constraint().Matches(version)
	}
	return r.Constraint().Matches(version)
}

// Describe renders the requirement for a human: the constraint plus, when the
// two differ, the version it was actually built against.
func (r RequiredApp) Describe() string {
	if r.VersionSpec != "" && r.Version != "" {
		return fmt.Sprintf("%s (built against %s)", r.Identifier(), r.Version)
	}
	return r.Identifier()
}

// LoadAppMetadata loads metadata from app_metadata.json file in the given appPath.
// If the file doesn't exist, it returns an empty AppMetadata struct and no error.
func LoadAppMetadata(appPath string) (*AppMetadata, error) {
	metadataFilePath := filepath.Join(appPath, "app_metadata.json")
	data := &AppMetadata{
		Dependencies:        make(map[string]string),
		FrappeCompatibility: make([]string, 0),
		Hooks:               make(map[string]string),
	}

	if _, err := os.Stat(metadataFilePath); os.IsNotExist(err) {
		// File doesn't exist, return a new struct (already initialized)
		return data, nil
	} else if err != nil {
		return nil, err // Other stat error
	}

	fileBytes, err := os.ReadFile(metadataFilePath)
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(fileBytes, data); err != nil {
		return nil, err
	}
	return data, nil
}

// GenerateAppMetadata creates a basic AppMetadata struct.
// It infers the packageName from the appPath's directory name.
// It sets the packageVersion from the provided argument.
func GenerateAppMetadata(appPath string, version string) (*AppMetadata, error) {
	absPath, err := filepath.Abs(appPath)
	if err != nil {
		return nil, err
	}
	// Infer package name from the directory name
	// This might need to be more sophisticated, e.g. looking for a specific module name
	packageName := filepath.Base(absPath)
	// A common convention for frappe apps is that the actual app module is one level deeper
	// e.g. my_app_repo/my_app_module. So we check if there's a directory with the same name inside.
	internalAppDir := filepath.Join(absPath, packageName)
	if stat, err := os.Stat(internalAppDir); err == nil && stat.IsDir() {
		// If my_app_repo/my_app_repo exists, that's likely the app's name
		// This is a simple heuristic.
	} else {
		// If not, check parent dir for common "apps" folder structure like in a bench
		parentDir := filepath.Base(filepath.Dir(absPath))
		if parentDir == "apps" {
			// we are likely in frappe-bench/apps/my_app, so packageName is correct
		} else {
			// Could not reliably infer app name, user might need to specify it
			// For now, we stick with the base directory name.
			// Consider adding a warning or requiring explicit app name if complex.
		}
	}

	return &AppMetadata{
		PackageName:         packageName,
		PackageVersion:      version,
		Dependencies:        make(map[string]string), // Initialize to avoid nil map
		FrappeCompatibility: make([]string, 0),       // Initialize to avoid nil slice
		Hooks:               make(map[string]string),
	}, nil
}

// SaveAppMetadata saves the AppMetadata struct to an app_metadata.json file
// in the specified directory (usually the staging directory for the package).
func SaveAppMetadata(targetDir string, data *AppMetadata) error {
	metadataFilePath := filepath.Join(targetDir, "app_metadata.json")
	fileBytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(metadataFilePath, fileBytes, 0644)
}
