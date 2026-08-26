package cmd

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"fpm/internal/config"
	"fpm/internal/metadata"
	"fpm/internal/resolver"
	"fpm/internal/wheels"

	"github.com/spf13/cobra"
)

var (
	depsCheck     bool
	depsJSON      bool
	depsBenchPath string
)

// DepsReport is the machine-readable output of `fpm deps --json`.
type DepsReport struct {
	Org                string            `json:"org"`
	App                string            `json:"app"`
	Version            string            `json:"version"`
	PackageType        string            `json:"package_type,omitempty"`
	Source             string            `json:"source"`
	CommitSHA          string            `json:"commit_sha,omitempty"`
	PythonDependencies []string          `json:"python_dependencies"`
	BundledWheels      []string          `json:"bundled_wheels"`
	Pins               []string          `json:"pins,omitempty"`
	WheelPlatform      string            `json:"wheel_platform,omitempty"`
	WheelPythonVersion string            `json:"wheel_python_version,omitempty"`
	RequiredApps       []DepsRequiredApp `json:"required_apps"`
	MissingRequired    []string          `json:"missing_required_apps"`
	AllRequiredPresent bool              `json:"all_required_present"`
	Dependencies       map[string]string `json:"declared_dependencies,omitempty"`
	AssetBundles       map[string]string `json:"asset_bundles,omitempty"`
}

// DepsRequiredApp is one entry of the transitive required-app closure.
type DepsRequiredApp struct {
	Org         string `json:"org,omitempty"`
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Requirement string `json:"requirement,omitempty"`
	RequiredBy  string `json:"required_by"`
	Present     bool   `json:"present_in_local_store"`
	StorePath   string `json:"store_path,omitempty"`
	// ProvidedByBench: satisfied by an app already in --bench-path, not by a package.
	ProvidedByBench bool `json:"provided_by_bench,omitempty"`
}

var depsCmd = &cobra.Command{
	Use:   "deps <package>",
	Short: "Inspect the dependencies a package declares, bundles and requires",
	Long: `Shows the Python dependencies a package declares and whether it bundles them for
offline installation, and the Frappe apps it requires (hooks.py required_apps) with
their transitive closure and whether each is already in the local FPM store.

The package may be a path to a local .fpm file, or an identifier resolved from the local
FPM app store in the form <org>/<app> or <org>/<app>==<version>. Without a version, the
latest in the local store is used.

Dependencies are read from the requirements.txt and pyproject.toml the package ships, so
this reports what an install would actually resolve, not what the source tree declares
today. Nothing is fetched: this is the question to ask before an offline install.

With --check the command exits with status ` + fmt.Sprint(ExitMissingRequiredApps) + ` when a required app is missing.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		fpmPath, err := resolveDepsTarget(args[0])
		if err != nil {
			return err
		}

		meta, err := metadata.ReadMetadataFromFPMArchive(fpmPath)
		if err != nil {
			return fmt.Errorf("failed to read metadata from %s: %w", fpmPath, err)
		}

		req, bundled, err := wheels.CollectFromArchive(fpmPath)
		if err != nil {
			return fmt.Errorf("failed to read dependencies from %s: %w", fpmPath, err)
		}
		pins, err := readLockFromArchive(fpmPath)
		if err != nil {
			return err
		}

		cfg, err := config.InitConfig()
		if err != nil {
			return fmt.Errorf("failed to initialize FPM configuration: %w", err)
		}
		packageID := fmt.Sprintf("%s/%s==%s", meta.Org, meta.AppName, meta.PackageVersion)
		closure, missing, err := resolver.CheckClosure(cfg.AppsBasePath, depsBenchPath, meta.RequiredApps, packageID)
		if err != nil {
			return err
		}

		if depsJSON {
			report := DepsReport{
				Org: meta.Org, App: meta.AppName, Version: meta.PackageVersion, PackageType: meta.PackageType,
				Source: fpmPath, CommitSHA: meta.CommitSHA,
				PythonDependencies: nonNil(req.Specs), BundledWheels: nonNil(bundled),
				WheelPlatform: meta.WheelPlatform, WheelPythonVersion: meta.WheelPythonVersion,
				RequiredApps: []DepsRequiredApp{}, MissingRequired: []string{},
				AllRequiredPresent: len(missing) == 0,
				Dependencies:       meta.Dependencies, AssetBundles: meta.AssetBundles,
			}
			for _, p := range pins {
				report.Pins = append(report.Pins, p.String())
			}
			for _, e := range closure {
				report.RequiredApps = append(report.RequiredApps, DepsRequiredApp{
					Org: e.App.Org, Name: e.App.Name, Version: e.App.Version, Requirement: e.App.Requirement,
					RequiredBy: e.RequiredBy, Present: !e.ProvidedByBench, StorePath: e.StorePath, ProvidedByBench: e.ProvidedByBench,
				})
			}
			for _, m := range missing {
				report.RequiredApps = append(report.RequiredApps, DepsRequiredApp{
					Org: m.App.Org, Name: m.App.Name, Version: m.App.Version, Requirement: m.App.Requirement,
					RequiredBy: m.RequiredBy, Present: false,
				})
				report.MissingRequired = append(report.MissingRequired, m.App.Identifier())
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			if err := enc.Encode(report); err != nil {
				return err
			}
		} else {
			printDeps(cmd.OutOrStdout(), fpmPath, meta, req, bundled, pins, closure, missing)
		}

		if depsCheck && len(missing) > 0 {
			return resolver.MissingError(packageID, missing)
		}
		return nil
	},
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func printDeps(out io.Writer, fpmPath string, meta *metadata.AppMetadata, req wheels.Requirements,
	bundled []string, pins []wheels.Pin, closure []resolver.ClosureEntry, missing []resolver.Missing,
) {
	fmt.Fprintf(out, "Package:  %s/%s\n", meta.Org, meta.AppName)
	fmt.Fprintf(out, "Version:  %s\n", meta.PackageVersion)
	if meta.PackageType != "" {
		fmt.Fprintf(out, "Type:     %s\n", meta.PackageType)
	}
	if meta.CommitSHA != "" {
		fmt.Fprintf(out, "Commit:   %s\n", meta.CommitSHA)
	}
	fmt.Fprintf(out, "Source:   %s\n", fpmPath)

	fmt.Fprintf(out, "\nDeclared Python dependencies")
	if len(req.Sources) > 0 {
		fmt.Fprintf(out, " (from %s)", req.Describe())
	}
	fmt.Fprintln(out, ":")
	if req.Empty() {
		fmt.Fprintf(out, "  none declared in %s or %s\n",
			wheels.RequirementsFileName, wheels.PyProjectFileName)
	} else {
		for _, spec := range req.Specs {
			fmt.Fprintf(out, "  %s\n", spec)
		}
	}

	fmt.Fprintln(out, "\nBundled dependencies:")
	if len(bundled) == 0 {
		fmt.Fprintf(out, "  none - installing this package resolves dependencies from the network\n")
	} else {
		platform := meta.WheelPlatform
		if platform == "" {
			platform = "unspecified"
		}
		if meta.WheelPythonVersion != "" {
			platform += ", python " + meta.WheelPythonVersion
		}
		fmt.Fprintf(out, "  %d wheel(s), built for %s\n", len(bundled), platform)
		for _, w := range bundled {
			fmt.Fprintf(out, "    %s\n", w)
		}
		if len(pins) > 0 {
			fmt.Fprintf(out, "  Locked versions (%s/%s):\n", wheels.DirName, wheels.LockFileName)
			for _, p := range pins {
				fmt.Fprintf(out, "    %s\n", p)
			}
		}
	}

	// Frappe app requirements, pinned at packaging time, checked against the local store.
	fmt.Fprintln(out, "\nRequired Frappe apps (hooks.py required_apps, pinned at packaging):")
	if len(meta.RequiredApps) == 0 {
		fmt.Fprintln(out, "  none (only frappe)")
	} else {
		for _, e := range closure {
			where := "present in local store"
			if e.ProvidedByBench {
				where = "provided by bench " + filepath.Dir(filepath.Dir(e.StorePath))
			}
			fmt.Fprintf(out, "  %s  [%s]  required by %s\n", e.App.Identifier(), where, e.RequiredBy)
		}
		for _, m := range missing {
			fmt.Fprintf(out, "  %s  [MISSING: %s]  required by %s\n", m.App.Identifier(), m.Reason, m.RequiredBy)
		}
		if len(missing) == 0 {
			fmt.Fprintln(out, "  all required apps are present; an offline install can proceed")
		} else {
			fmt.Fprintf(out, "  %d required app(s) missing; an offline install would fail\n", len(missing))
		}
	}

	// Manifest-level dependencies that are not the resolved required_apps above.
	legacy := map[string]string{}
	for name, constraint := range meta.Dependencies {
		isResolved := false
		for _, r := range meta.RequiredApps {
			if name == r.Org+"/"+r.Name || name == r.Name {
				isResolved = true
			}
		}
		if !isResolved {
			legacy[name] = constraint
		}
	}
	if len(legacy) > 0 {
		fmt.Fprintln(out, "\nDeclared FPM package dependencies:")
		names := make([]string, 0, len(legacy))
		for name := range legacy {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Fprintf(out, "  %s %s\n", name, legacy[name])
		}
		fmt.Fprintln(out, "  (not resolved during install)")
	}

	if len(meta.AssetBundles) > 0 {
		fmt.Fprintf(out, "\nBuilt asset bundles (%d):\n", len(meta.AssetBundles))
		keys := make([]string, 0, len(meta.AssetBundles))
		for k := range meta.AssetBundles {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(out, "  %s -> %s\n", k, meta.AssetBundles[k])
		}
	}
}

// readLockFromArchive reads wheels/fpm-lock.txt from a package without extracting it.
func readLockFromArchive(fpmPath string) ([]wheels.Pin, error) {
	r, err := zip.OpenReader(fpmPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open FPM package %s: %w", fpmPath, err)
	}
	defer r.Close()
	want := wheels.DirName + "/" + wheels.LockFileName
	for _, f := range r.File {
		if f.Name != want {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("failed to open %s in %s: %w", want, fpmPath, err)
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			return nil, err
		}
		return wheels.ParseLock(string(data)), nil
	}
	return nil, nil
}

// resolveDepsTarget turns a user-supplied target into a path to an .fpm file. The target
// is either a path to a package, or an identifier resolved from the local app store.
func resolveDepsTarget(target string) (string, error) {
	// A path to an existing .fpm file is used directly.
	if strings.HasSuffix(strings.ToLower(target), ".fpm") {
		abs, err := filepath.Abs(target)
		if err != nil {
			return "", fmt.Errorf("failed to resolve path %s: %w", target, err)
		}
		if _, err := os.Stat(abs); err != nil {
			return "", fmt.Errorf("package file not found: %s", abs)
		}
		return abs, nil
	}

	org, appName, version, err := parseDepsIdentifier(target)
	if err != nil {
		return "", err
	}

	cfg, err := config.InitConfig()
	if err != nil {
		return "", fmt.Errorf("failed to initialize FPM configuration: %w", err)
	}

	if version == "" || version == "latest" {
		resolved, err := resolveLatestVersionFromLocalStore(cfg.AppsBasePath, org, appName)
		if err != nil {
			return "", fmt.Errorf("failed to resolve latest version for %s/%s: %w", org, appName, err)
		}
		if resolved == "" {
			return "", fmt.Errorf("no versions of %s/%s found in the local FPM app store at %s. "+
				"Install or package it first, or pass a path to an .fpm file",
				org, appName, cfg.AppsBasePath)
		}
		version = resolved
	}

	versionDir := filepath.Join(cfg.AppsBasePath, org, appName, version)
	fpmPath := filepath.Join(versionDir, fmt.Sprintf("_%s-%s.fpm", appName, version))
	if _, err := os.Stat(fpmPath); err != nil {
		return "", fmt.Errorf("package %s/%s version %s not found in the local FPM app store (expected %s)",
			org, appName, version, fpmPath)
	}
	return fpmPath, nil
}

// parseDepsIdentifier splits <org>/<app>[==<version>] into its parts.
func parseDepsIdentifier(identifier string) (org, appName, version string, err error) {
	parts := strings.Split(identifier, "/")
	if len(parts) != 2 {
		return "", "", "", fmt.Errorf("invalid package identifier %q. "+
			"Expected a path to an .fpm file, or <org>/<app>[==<version>]", identifier)
	}
	org = strings.TrimSpace(parts[0])

	appAndVersion := strings.SplitN(parts[1], "==", 2)
	appName = strings.TrimSpace(appAndVersion[0])
	if len(appAndVersion) == 2 {
		version = strings.TrimSpace(appAndVersion[1])
	}

	if org == "" || appName == "" {
		return "", "", "", fmt.Errorf("invalid package identifier %q: org and app name are both required", identifier)
	}
	return org, appName, version, nil
}

func init() {
	depsCmd.Flags().BoolVar(&depsCheck, "check", false, "Exit non-zero when a required app (transitively) is missing from the local FPM store")
	depsCmd.Flags().BoolVar(&depsJSON, "json", false, "Print the report as JSON")
	depsCmd.Flags().StringVar(&depsBenchPath, "bench-path", "", "Also accept required apps already present in this bench (installed outside fpm)")
	rootCmd.AddCommand(depsCmd)
}
