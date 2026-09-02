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
	"time"

	"fpm/internal/config"
	"fpm/internal/metadata"
	"fpm/internal/repository"
	"fpm/internal/resolver"
	"fpm/internal/semver"
	"fpm/internal/wheels"

	"github.com/spf13/cobra"
)

var (
	depsCheck     bool
	depsJSON      bool
	depsBenchPath string
	depsRepoName  string
	depsNoRemote  bool
)

// InstallPlanItem represents one app in the installation plan and what action will be taken.
type InstallPlanItem struct {
	Org             string `json:"org,omitempty"`
	AppName         string `json:"app_name"`
	Version         string `json:"version,omitempty"`
	Identifier      string `json:"identifier"`
	RequiredBy      string `json:"required_by"`
	Status          string `json:"status"` // "will_install", "will_fetch_and_install", "in_local_store", "already_in_bench", "missing"
	Action          string `json:"action"` // "install", "fetch_and_install", "skip_already_in_bench"
	Source          string `json:"source"` // "local-store", "repo:<name>", "bench:<path>"
	StorePath       string `json:"store_path,omitempty"`
	ProvidedByBench bool   `json:"provided_by_bench,omitempty"`
}

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
	InstallPlan        []InstallPlanItem `json:"install_plan"`
	InstallQueue       []string          `json:"install_queue"` // ordered identifiers of apps that will be installed into bench
}

// DepsRequiredApp is one entry of the transitive required-app closure.
type DepsRequiredApp struct {
	Org             string `json:"org,omitempty"`
	Name            string `json:"name"`
	Version         string `json:"version,omitempty"`
	Requirement     string `json:"requirement,omitempty"`
	RequiredBy      string `json:"required_by"`
	Present         bool   `json:"present_in_local_store"`
	StorePath       string `json:"store_path,omitempty"`
	ProvidedByBench bool   `json:"provided_by_bench,omitempty"`
	Source          string `json:"source,omitempty"`
}

var depsCmd = &cobra.Command{
	Use:   "deps <package>",
	Short: "Inspect dependencies and list apps that will be installed into the bench",
	Long: `Shows the Python dependencies a package declares and bundles, and the Frappe apps
it requires (hooks.py required_apps) with their transitive closure and installation order.

The package may be:
  - A path to a local .fpm file (e.g. ./hrms-15.2.0.fpm)
  - A local app store identifier (e.g. frappe/hrms==15.2.0)
  - A remote repository package identifier (e.g. frappe/hrms or frappe/hrms==15.2.0)

When --bench-path is provided, fpm deps inspects the bench to report which dependencies
are already satisfied by the bench and which will be freshly installed.

With --json, fpm deps emits a structured JSON object containing the complete dependency
graph, install queue, and action per application for CI/CD and automation tooling.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		defer func() {
			depsCheck = false
			depsJSON = false
			depsBenchPath = ""
			depsRepoName = ""
			depsNoRemote = false
		}()

		cfg, err := config.InitConfig()
		if err != nil {
			return fmt.Errorf("failed to initialize FPM configuration: %w", err)
		}

		targetArg := args[0]
		meta, fpmPath, sourceDesc, req, bundled, pins, err := resolveDepsPackage(targetArg, cfg, depsRepoName, !depsNoRemote)
		if err != nil {
			return err
		}

		packageID := fmt.Sprintf("%s/%s==%s", meta.Org, meta.AppName, meta.PackageVersion)

		// Resolve transitive closure across bench, local store, and remote repos
		closure, missing, planItems, installQueue, err := buildInstallPlan(cfg, meta, packageID, depsBenchPath, depsRepoName, !depsNoRemote)
		if err != nil {
			return err
		}

		if depsJSON {
			report := DepsReport{
				Org:                meta.Org,
				App:                meta.AppName,
				Version:            meta.PackageVersion,
				PackageType:        meta.PackageType,
				Source:             sourceDesc,
				CommitSHA:          meta.CommitSHA,
				PythonDependencies: nonNil(req.Specs),
				BundledWheels:      nonNil(bundled),
				WheelPlatform:      meta.WheelPlatform,
				WheelPythonVersion: meta.WheelPythonVersion,
				RequiredApps:       []DepsRequiredApp{},
				MissingRequired:    []string{},
				AllRequiredPresent: len(missing) == 0,
				Dependencies:       meta.Dependencies,
				AssetBundles:       meta.AssetBundles,
				InstallPlan:        planItems,
				InstallQueue:       installQueue,
			}
			for _, p := range pins {
				report.Pins = append(report.Pins, p.String())
			}
			for _, e := range closure {
				report.RequiredApps = append(report.RequiredApps, DepsRequiredApp{
					Org:             e.App.Org,
					Name:            e.App.Name,
					Version:         e.App.Version,
					Requirement:     e.App.Requirement,
					RequiredBy:      e.RequiredBy,
					Present:         e.Present && !e.ProvidedByBench,
					StorePath:       e.StorePath,
					ProvidedByBench: e.ProvidedByBench,
				})
			}
			for _, m := range missing {
				report.MissingRequired = append(report.MissingRequired, m.App.Identifier())
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			if err := enc.Encode(report); err != nil {
				return err
			}
		} else {
			printDeps(cmd.OutOrStdout(), fpmPath, sourceDesc, meta, req, bundled, pins, closure, missing, planItems, installQueue, depsBenchPath)
		}

		if depsCheck && len(missing) > 0 {
			return resolver.MissingError(packageID, missing)
		}
		return nil
	},
}

func resolveDepsPackage(target string, cfg *config.FPMConfig, repoName string, allowRemote bool) (
	meta *metadata.AppMetadata, fpmPath string, sourceDesc string, req wheels.Requirements, bundled []string, pins []wheels.Pin, err error,
) {
	// 1. Direct path to .fpm file
	if strings.HasSuffix(strings.ToLower(target), ".fpm") {
		abs, err := filepath.Abs(target)
		if err != nil {
			return nil, "", "", req, nil, nil, fmt.Errorf("failed to resolve path %s: %w", target, err)
		}
		if _, err := os.Stat(abs); err != nil {
			return nil, "", "", req, nil, nil, fmt.Errorf("package file not found: %s", abs)
		}
		meta, err := metadata.ReadMetadataFromFPMArchive(abs)
		if err != nil {
			return nil, "", "", req, nil, nil, fmt.Errorf("failed to read metadata from %s: %w", abs, err)
		}
		req, bundled, _ := wheels.CollectFromArchive(abs)
		pins, _ := readLockFromArchive(abs)
		return meta, abs, abs, req, bundled, pins, nil
	}

	// 2. Package identifier <org>/<app>[==<version>]
	org, appName, version, err := parseDepsIdentifier(target)
	if err != nil {
		return nil, "", "", req, nil, nil, err
	}

	// Check local store first
	targetVersion := version
	if targetVersion == "" || targetVersion == "latest" {
		if resolved, err := resolveLatestVersionFromLocalStore(cfg.AppsBasePath, org, appName); err == nil && resolved != "" {
			targetVersion = resolved
		}
	}

	if targetVersion != "" && targetVersion != "latest" {
		versionDir := filepath.Join(cfg.AppsBasePath, org, appName, targetVersion)
		localFPMPath := filepath.Join(versionDir, fmt.Sprintf("_%s-%s.fpm", appName, targetVersion))
		if _, err := os.Stat(localFPMPath); err == nil {
			meta, err := metadata.ReadMetadataFromFPMArchive(localFPMPath)
			if err == nil {
				req, bundled, _ := wheels.CollectFromArchive(localFPMPath)
				pins, _ := readLockFromArchive(localFPMPath)
				return meta, localFPMPath, fmt.Sprintf("local-store:%s", localFPMPath), req, bundled, pins, nil
			}
		}
		if meta, err := metadata.LoadAppMetadata(versionDir); err == nil {
			return meta, "", fmt.Sprintf("local-store:%s", versionDir), req, bundled, pins, nil
		}
	}

	// 3. Remote repository query if not found locally
	if allowRemote {
		repos := config.ListRepositories(cfg)
		if repoName != "" {
			r, ok := config.GetRepository(cfg, repoName)
			if !ok {
				return nil, "", "", req, nil, nil, fmt.Errorf("repository %q is not configured", repoName)
			}
			repos = []config.RepositoryConfig{r}
		}

		for _, repo := range repos {
			client, cerr := resolver.NewHTTPClient(repo, 2*time.Second)
			if cerr != nil {
				continue
			}
			pkgMeta, found, err := repository.FetchRemotePackageMetadataForRepo(repo, org, appName, client)
			if err != nil || !found || pkgMeta == nil {
				continue
			}

			resolvedVer := version
			if resolvedVer == "" || resolvedVer == "latest" {
				resolvedVer = pkgMeta.LatestVersion
			}
			if resolvedVer == "" {
				continue
			}

			vMeta, exists := pkgMeta.Versions[resolvedVer]
			if !exists {
				continue
			}

			appMeta := &metadata.AppMetadata{
				Org:                 org,
				AppName:             appName,
				PackageVersion:      resolvedVer,
				PackageType:         vMeta.PackageType,
				CommitSHA:           vMeta.CommitSHA,
				GitRef:              vMeta.GitRef,
				WheelPlatform:       vMeta.WheelPlatform,
				WheelPythonVersion:  vMeta.WheelPythonVersion,
				FrappeCompatibility: vMeta.FrappeCompatibility,
				Description:         vMeta.Notes,
				RequiredApps:        make([]metadata.RequiredApp, len(vMeta.RequiredApps)),
				Dependencies:        make(map[string]string),
			}
			for i, r := range vMeta.RequiredApps {
				appMeta.RequiredApps[i] = metadata.RequiredApp{
					Org:         r.Org,
					Name:        r.AppName,
					Version:     r.Version,
					VersionSpec: r.VersionSpec,
				}
			}
			for _, d := range vMeta.Dependencies {
				appMeta.Dependencies[d.AppName] = d.VersionConstraint
			}

			source := fmt.Sprintf("repo:%s (%s)", repo.Name, repo.URL)
			return appMeta, "", source, req, bundled, pins, nil
		}
	}

	return nil, "", "", req, nil, nil, fmt.Errorf("package %s/%s version %q not found in local FPM store or configured repositories",
		org, appName, version)
}

func buildInstallPlan(
	cfg *config.FPMConfig,
	rootMeta *metadata.AppMetadata,
	rootID string,
	benchPath string,
	repoName string,
	allowRemote bool,
) (closure []resolver.ClosureEntry, missing []resolver.Missing, planItems []InstallPlanItem, installQueue []string, err error) {
	benchApps := map[string]string{}
	if benchPath != "" {
		if data, err := os.ReadFile(filepath.Join(benchPath, "sites", "apps.txt")); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if line = strings.TrimSpace(line); line != "" {
					ver, _ := resolver.BenchAppVersion(benchPath, line)
					benchApps[line] = ver
				}
			}
		}
	}

	visited := map[string]bool{}

	var walk func(list []metadata.RequiredApp, by string, depth int) error
	walk = func(list []metadata.RequiredApp, by string, depth int) error {
		if depth > 32 {
			return fmt.Errorf("dependency chain deeper than 32 levels from %s", rootID)
		}
		for _, reqApp := range list {
			if reqApp.Name == resolver.FrappeAppName {
				continue
			}
			key := reqApp.Org + "/" + reqApp.Name
			if visited[key] {
				continue
			}
			visited[key] = true

			org := reqApp.Org
			if org == "" {
				orgs := resolver.StoreOrgs(cfg.AppsBasePath, reqApp.Name)
				if len(orgs) == 1 {
					org = orgs[0]
				}
			}

			// A requirement with a constraint takes any store version that satisfies
			// it, preferring the version the package was built against.
			available := resolver.StoreVersions(cfg.AppsBasePath, org, reqApp.Name)
			reqVer := reqApp.Version
			switch {
			case reqApp.VersionSpec != "":
				if reqVer == "" || !resolver.InStore(cfg.AppsBasePath, org, reqApp.Name, reqVer) {
					if selected := reqApp.Constraint().Select(available); selected != "" {
						reqVer = selected
					}
				}
			case reqVer == "":
				reqVer = semver.Latest(available)
			}

			identifier := fmt.Sprintf("%s/%s==%s", org, reqApp.Name, reqVer)
			if org == "" {
				identifier = fmt.Sprintf("%s==%s", reqApp.Name, reqVer)
			}

			// 1. Check Bench
			if benchVer, inBench := benchApps[reqApp.Name]; inBench {
				if benchVer == "" || reqApp.Accepts(benchVer) {
					item := InstallPlanItem{
						Org:             org,
						AppName:         reqApp.Name,
						Version:         benchVer,
						Identifier:      identifier,
						RequiredBy:      by,
						Status:          "already_in_bench",
						Action:          "skip_already_in_bench",
						Source:          fmt.Sprintf("bench:%s", benchPath),
						ProvidedByBench: true,
					}
					planItems = append(planItems, item)
					closure = append(closure, resolver.ClosureEntry{
						App:             metadata.RequiredApp{Name: reqApp.Name, Org: org, Version: benchVer, Requirement: reqApp.Requirement},
						RequiredBy:      by,
						Present:         true,
						ProvidedByBench: true,
					})
					continue
				}
			}

			// 2. Check Local Store
			inStore := org != "" && reqVer != "" && resolver.InStore(cfg.AppsBasePath, org, reqApp.Name, reqVer)
			if inStore {
				storePath := filepath.Join(cfg.AppsBasePath, org, reqApp.Name, reqVer)
				depMeta, _ := metadata.LoadAppMetadata(storePath)
				if depMeta != nil {
					if err := walk(depMeta.RequiredApps, identifier, depth+1); err != nil {
						return err
					}
				}

				item := InstallPlanItem{
					Org:        org,
					AppName:    reqApp.Name,
					Version:    reqVer,
					Identifier: identifier,
					RequiredBy: by,
					Status:     "in_local_store",
					Action:     "install",
					Source:     "local-store",
					StorePath:  storePath,
				}
				planItems = append(planItems, item)
				installQueue = append(installQueue, identifier)
				closure = append(closure, resolver.ClosureEntry{
					App:        metadata.RequiredApp{Name: reqApp.Name, Org: org, Version: reqVer, Requirement: reqApp.Requirement},
					RequiredBy: by,
					Present:    true,
					StorePath:  storePath,
				})
				continue
			}

			// 3. Check Remote Repositories
			if allowRemote {
				foundRepo := ""
				var depMeta *metadata.AppMetadata
				repos := config.ListRepositories(cfg)
				if repoName != "" {
					if r, ok := config.GetRepository(cfg, repoName); ok {
						repos = []config.RepositoryConfig{r}
					}
				}

				for _, repo := range repos {
					client, cerr := resolver.NewHTTPClient(repo, 2*time.Second)
					if cerr != nil {
						continue
					}
					pkgMeta, found, err := repository.FetchRemotePackageMetadataForRepo(repo, org, reqApp.Name, client)
					if err != nil || !found || pkgMeta == nil {
						continue
					}
					targetVer := reqVer
					if targetVer == "" || targetVer == "latest" {
						targetVer = pkgMeta.LatestVersion
					}
					if vMeta, ok := pkgMeta.Versions[targetVer]; ok {
						foundRepo = repo.Name
						depMeta = &metadata.AppMetadata{
							Org:            org,
							AppName:        reqApp.Name,
							PackageVersion: targetVer,
							RequiredApps:   make([]metadata.RequiredApp, len(vMeta.RequiredApps)),
						}
						for i, r := range vMeta.RequiredApps {
							depMeta.RequiredApps[i] = metadata.RequiredApp{
								Org:         r.Org,
								Name:        r.AppName,
								Version:     r.Version,
								VersionSpec: r.VersionSpec,
							}
						}
						break
					}
				}

				if foundRepo != "" && depMeta != nil {
					if err := walk(depMeta.RequiredApps, identifier, depth+1); err != nil {
						return err
					}

					item := InstallPlanItem{
						Org:        org,
						AppName:    reqApp.Name,
						Version:    depMeta.PackageVersion,
						Identifier: identifier,
						RequiredBy: by,
						Status:     "will_fetch_and_install",
						Action:     "fetch_and_install",
						Source:     fmt.Sprintf("repo:%s", foundRepo),
					}
					planItems = append(planItems, item)
					installQueue = append(installQueue, identifier)
					closure = append(closure, resolver.ClosureEntry{
						App:        metadata.RequiredApp{Name: reqApp.Name, Org: org, Version: depMeta.PackageVersion, Requirement: reqApp.Requirement},
						RequiredBy: by,
						Present:    false,
					})
					continue
				}
			}

			// Not found
			m := resolver.Missing{
				App:        metadata.RequiredApp{Name: reqApp.Name, Org: org, Version: reqVer, Requirement: reqApp.Requirement},
				RequiredBy: by,
				Reason:     "not found in bench, local store, or configured repositories",
			}
			missing = append(missing, m)
			planItems = append(planItems, InstallPlanItem{
				Org:        org,
				AppName:    reqApp.Name,
				Version:    reqVer,
				Identifier: identifier,
				RequiredBy: by,
				Status:     "missing",
				Action:     "missing",
			})
		}
		return nil
	}

	if err := walk(rootMeta.RequiredApps, rootID, 0); err != nil {
		return nil, nil, nil, nil, err
	}

	// Add the target root package to the end of the installation plan
	rootAction := "install"
	rootStatus := "will_install"
	if _, inBench := benchApps[rootMeta.AppName]; inBench {
		rootAction = "install (upgrade/reinstall in bench)"
	}

	rootPlanItem := InstallPlanItem{
		Org:        rootMeta.Org,
		AppName:    rootMeta.AppName,
		Version:    rootMeta.PackageVersion,
		Identifier: rootID,
		RequiredBy: "(target package)",
		Status:     rootStatus,
		Action:     rootAction,
		Source:     "target",
	}
	planItems = append(planItems, rootPlanItem)
	installQueue = append(installQueue, rootID)

	return closure, missing, planItems, installQueue, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func printDeps(
	out io.Writer,
	fpmPath string,
	sourceDesc string,
	meta *metadata.AppMetadata,
	req wheels.Requirements,
	bundled []string,
	pins []wheels.Pin,
	closure []resolver.ClosureEntry,
	missing []resolver.Missing,
	planItems []InstallPlanItem,
	installQueue []string,
	benchPath string,
) {
	fmt.Fprintf(out, "Target Package:  %s/%s\n", meta.Org, meta.AppName)
	fmt.Fprintf(out, "Version:         %s\n", meta.PackageVersion)
	if meta.PackageType != "" {
		fmt.Fprintf(out, "Type:            %s\n", meta.PackageType)
	}
	if meta.CommitSHA != "" {
		fmt.Fprintf(out, "Commit:          %s\n", meta.CommitSHA)
	}
	fmt.Fprintf(out, "Source:          %s\n", sourceDesc)

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

	// Required Frappe apps
	fmt.Fprintln(out, "\nRequired Frappe apps (hooks.py required_apps):")
	if len(meta.RequiredApps) == 0 {
		fmt.Fprintln(out, "  none (only frappe)")
	} else {
		for _, e := range closure {
			where := "present in local store"
			if e.ProvidedByBench {
				where = "provided by bench"
			} else if !e.Present {
				where = "will fetch from repository"
			}
			fmt.Fprintf(out, "  %s  [%s]  required by %s\n", e.App.Identifier(), where, e.RequiredBy)
		}
		for _, m := range missing {
			fmt.Fprintf(out, "  %s  [MISSING: %s]  required by %s\n", m.App.Identifier(), m.Reason, m.RequiredBy)
		}
	}

	// Installation Plan Summary
	fmt.Fprintln(out, "\n================================================================================")
	if benchPath != "" {
		fmt.Fprintf(out, "Installation Plan for bench %s:\n", benchPath)
	} else {
		fmt.Fprintln(out, "Installation Order & Planned Actions:")
	}
	fmt.Fprintln(out, "================================================================================")

	toInstallCount := 0
	for i, item := range planItems {
		switch item.Action {
		case "skip_already_in_bench":
			fmt.Fprintf(out, "  %d. %s -> SKIP (already present in bench)\n", i+1, item.Identifier)
		case "fetch_and_install":
			toInstallCount++
			fmt.Fprintf(out, "  %d. %s -> FETCH from %s and INSTALL into bench\n", i+1, item.Identifier, item.Source)
		default:
			toInstallCount++
			suffix := ""
			if item.RequiredBy == "(target package)" {
				suffix = " (target)"
			}
			fmt.Fprintf(out, "  %d. %s%s -> INSTALL into bench\n", i+1, item.Identifier, suffix)
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

	fmt.Fprintln(out, "--------------------------------------------------------------------------------")
	if len(missing) > 0 {
		fmt.Fprintf(out, "Status: %d missing dependency app(s) cannot be satisfied offline!\n", len(missing))
	} else {
		fmt.Fprintf(out, "Total apps to be installed / upgraded in bench: %d\n", toInstallCount)
	}
	fmt.Fprintln(out, "================================================================================")
}

func readLockFromArchive(fpmPath string) ([]wheels.Pin, error) {
	if fpmPath == "" {
		return nil, nil
	}
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

// resolveDepsTarget turns a user-supplied target into a path to an .fpm file.
func resolveDepsTarget(target string) (string, error) {
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
			return "", fmt.Errorf("no versions of %s/%s found in the local FPM app store at %s. Install or package it first, or pass a path to an .fpm file",
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

func init() {
	depsCmd.Flags().BoolVar(&depsCheck, "check", false, "Exit non-zero when a required app (transitively) is missing and cannot be satisfied")
	depsCmd.Flags().BoolVar(&depsJSON, "json", false, "Print the dependency report and installation plan as JSON")
	depsCmd.Flags().StringVar(&depsBenchPath, "bench-path", "", "Bench path to check which apps are already installed and satisfied")
	depsCmd.Flags().StringVar(&depsRepoName, "repo", "", "Limit remote repository resolution to a specific configured repository")
	depsCmd.Flags().BoolVar(&depsNoRemote, "no-remote", false, "Disable remote repository querying (local FPM store and bench only)")
	rootCmd.AddCommand(depsCmd)
}
