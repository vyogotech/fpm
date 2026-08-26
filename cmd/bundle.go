package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"fpm/internal/appstore"
	"fpm/internal/config"
	"fpm/internal/metadata"
	"fpm/internal/repository"
	"fpm/internal/resolver"
	"fpm/internal/utils"

	"github.com/spf13/cobra"
)

// BundleManifestName is the file that marks a directory as a dependency-closure
// bundle and lists its packages in install order.
const BundleManifestName = "fpm-bundle.json"

// BundleEntry is one package in a bundle.
type BundleEntry struct {
	Org     string `json:"org"`
	App     string `json:"app"`
	Version string `json:"version"`
	// File is the package's filename inside the bundle directory.
	File       string `json:"file"`
	RequiredBy string `json:"required_by,omitempty"`
	CommitSHA  string `json:"commit_sha,omitempty"`
	// ProvidedBy is "bench" for a requirement satisfied by an app already in the
	// bench the bundle was made against: it is listed for completeness but no
	// package file is shipped, and the target bench must have it too.
	ProvidedBy string `json:"provided_by,omitempty"`
}

// Identifier renders org/app==version.
func (e BundleEntry) Identifier() string { return e.Org + "/" + e.App + "==" + e.Version }

// BundleManifest describes a bundle: the package it was made for and every package
// an offline bench needs, each exactly once, deepest dependency first.
type BundleManifest struct {
	Root         BundleEntry   `json:"root"`
	InstallOrder []BundleEntry `json:"install_order"`
	CreatedBy    string        `json:"created_by"`
}

var (
	bundleOutput    string
	bundleRemote    bool
	bundleRepo      string
	bundleBenchPath string
)

var bundleCmd = &cobra.Command{
	Use:   "bundle <org>/<app>[==<version>] | <file.fpm>",
	Short: "Export a package together with every Frappe app it transitively requires",
	Long: `Writes a directory holding the package and the packages of all its required_apps,
transitively — each exactly once, so a dependency shared by several apps (erpnext under
hrms and a custom app, say) is not duplicated — plus ` + BundleManifestName + ` listing them
in install order. Copy the directory to an offline bench and run

    fpm install <directory> --bench-path <bench> [--site <site>]

which installs every package in that order without any network access.

Required apps are taken from the local FPM store. With --remote, ones that are not
there yet are fetched from the configured repositories first (this is the online half
of the workflow; the install half never fetches).`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		rootFpm, err := resolveDepsTarget(args[0])
		if err != nil {
			return err
		}
		cfg, err := config.InitConfig()
		if err != nil {
			return fmt.Errorf("failed to initialize FPM configuration: %w", err)
		}
		outDir := bundleOutput
		if outDir == "" {
			meta, err := metadata.ReadMetadataFromFPMArchive(rootFpm)
			if err != nil {
				return err
			}
			outDir = fmt.Sprintf("%s-%s-bundle", meta.AppName, meta.PackageVersion)
		}
		manifest, err := exportBundle(rootFpm, outDir, cfg, bundleRemote, bundleRepo, bundleBenchPath)
		if err != nil {
			return err
		}
		printBundle(cmd, outDir, manifest)
		return nil
	},
}

func printBundle(cmd *cobra.Command, outDir string, m *BundleManifest) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Bundle written to %s (%d package(s), install order):\n", outDir, len(m.InstallOrder))
	for i, e := range m.InstallOrder {
		by := ""
		if e.RequiredBy != "" {
			by = "  (required by " + e.RequiredBy + ")"
		}
		file := e.File
		if e.ProvidedBy == "bench" {
			file = "provided by the bench, not shipped"
		}
		fmt.Fprintf(out, "  %d. %s  %s%s\n", i+1, e.Identifier(), file, by)
	}
	fmt.Fprintf(out, "Install on the target bench with: fpm install %s --bench-path <bench> --site <site>\n", outDir)
}

// exportBundle copies rootFpm and the packages of its transitive required_apps into
// outDir and writes the manifest. Every required package must be in the local store;
// with remote, missing ones are fetched from repositories into the store first.
func exportBundle(rootFpm, outDir string, cfg *config.FPMConfig, remote bool, repoName, benchPath string) (*BundleManifest, error) {
	meta, err := metadata.ReadMetadataFromFPMArchive(rootFpm)
	if err != nil {
		return nil, err
	}
	if meta.Org == "" || meta.AppName == "" || meta.PackageVersion == "" {
		return nil, fmt.Errorf("package metadata in %s is incomplete (missing org, app_name or package_version)", rootFpm)
	}
	rootID := fmt.Sprintf("%s/%s==%s", meta.Org, meta.AppName, meta.PackageVersion)

	var closure []resolver.ClosureEntry
	for attempt := 0; ; attempt++ {
		var missing []resolver.Missing
		closure, missing, err = resolver.CheckClosure(cfg.AppsBasePath, benchPath, meta.RequiredApps, rootID)
		if err != nil {
			return nil, err
		}
		if len(missing) == 0 {
			break
		}
		if !remote || attempt >= 32 {
			return nil, resolver.MissingError(rootID, missing)
		}
		for _, m := range missing {
			if err := fetchIntoStore(cfg, m.App, repoName); err != nil {
				return nil, fmt.Errorf("%w: %s (required by %s): %v", resolver.ErrMissing, m.App.Identifier(), m.RequiredBy, err)
			}
		}
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create bundle directory %s: %w", outDir, err)
	}
	manifest := &BundleManifest{CreatedBy: "fpm " + VersionString()}
	for _, e := range closure {
		if e.ProvidedByBench {
			manifest.InstallOrder = append(manifest.InstallOrder, BundleEntry{
				Org: e.App.Org, App: e.App.Name, Version: e.App.Version, RequiredBy: e.RequiredBy, ProvidedBy: "bench",
			})
			continue
		}
		storedFpm := filepath.Join(e.StorePath, fmt.Sprintf("_%s-%s.fpm", e.App.Name, e.App.Version))
		if _, err := os.Stat(storedFpm); err != nil {
			return nil, fmt.Errorf("package file for %s not found in the local store (expected %s)", e.App.Identifier(), storedFpm)
		}
		entry, err := copyIntoBundle(storedFpm, outDir, e.App.Org, e.App.Name, e.App.Version, e.RequiredBy)
		if err != nil {
			return nil, err
		}
		manifest.InstallOrder = append(manifest.InstallOrder, entry)
	}
	root, err := copyIntoBundle(rootFpm, outDir, meta.Org, meta.AppName, meta.PackageVersion, "")
	if err != nil {
		return nil, err
	}
	root.CommitSHA = meta.CommitSHA
	manifest.Root = root
	manifest.InstallOrder = append(manifest.InstallOrder, root)

	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(outDir, BundleManifestName), data, 0o644); err != nil {
		return nil, fmt.Errorf("failed to write %s: %w", BundleManifestName, err)
	}
	return manifest, nil
}

func copyIntoBundle(src, outDir, org, app, version, requiredBy string) (BundleEntry, error) {
	file := fmt.Sprintf("%s-%s.fpm", app, version)
	if err := utils.CopyRegularFile(src, filepath.Join(outDir, file), 0o644); err != nil {
		return BundleEntry{}, fmt.Errorf("failed to copy %s into bundle: %w", src, err)
	}
	entry := BundleEntry{Org: org, App: app, Version: version, File: file, RequiredBy: requiredBy}
	if m, err := metadata.ReadMetadataFromFPMArchive(src); err == nil {
		entry.CommitSHA = m.CommitSHA
	}
	return entry, nil
}

// fetchIntoStore downloads a required package from the configured repositories (or
// one named repository) and extracts it into the local store.
func fetchIntoStore(cfg *config.FPMConfig, app metadata.RequiredApp, repoName string) error {
	if app.Org == "" {
		return fmt.Errorf("cannot fetch an unqualified requirement; the pin has no org")
	}
	version := app.Version
	if version == "" {
		version = "latest"
	}
	var downloaded *repository.DownloadedPackageInfo
	var err error
	if repoName != "" {
		repo, ok := config.GetRepository(cfg, repoName)
		if !ok {
			return fmt.Errorf("repository %q is not configured", repoName)
		}
		client, cerr := resolver.NewHTTPClient(repo, 0)
		if cerr != nil {
			return cerr
		}
		downloaded, err = repository.FindPackageInSpecificRepo(repo.Name, repo.URL, app.Org, app.Name, version, client)
	} else {
		downloaded, err = repository.FindPackageInRepos(cfg, app.Org, app.Name, version)
	}
	if err != nil {
		return err
	}
	fmt.Printf("Fetched %s from repository %s; adding to local store.\n", app.Identifier(), downloaded.RepositoryName)
	_, _, _, _, _, err = appstore.ManageAppInLocalStore(downloaded.LocalPath, cfg)
	return err
}

// isBundleDir reports whether path is a bundle directory.
func isBundleDir(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	_, err = os.Stat(filepath.Join(path, BundleManifestName))
	return err == nil
}

// readBundleManifest loads a bundle directory's manifest.
func readBundleManifest(dir string) (*BundleManifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, BundleManifestName))
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", filepath.Join(dir, BundleManifestName), err)
	}
	var m BundleManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", filepath.Join(dir, BundleManifestName), err)
	}
	if len(m.InstallOrder) == 0 {
		return nil, fmt.Errorf("%s lists no packages", filepath.Join(dir, BundleManifestName))
	}
	return &m, nil
}

// installBundle installs every package of a bundle, in the manifest's order. Each
// step goes through the normal single-package install, including its offline
// pre-checks, which pass precisely because dependencies come first.
func installBundle(cmd *cobra.Command, dir, benchPath, siteName string, cfg *config.FPMConfig) error {
	m, err := readBundleManifest(dir)
	if err != nil {
		return err
	}
	fmt.Printf("Installing bundle %s: %d package(s) in dependency order\n", dir, len(m.InstallOrder))
	for i, e := range m.InstallOrder {
		if e.ProvidedBy == "bench" {
			// Not shipped: the bundle was made against a bench that already had it,
			// and this bench must too.
			if v, present := resolver.BenchAppVersion(benchPath, e.App); !present {
				return fmt.Errorf("%w: bundle entry %s is expected to be provided by the bench, but %s has no such app",
					resolver.ErrMissing, e.Identifier(), benchPath)
			} else if e.Version != "" && v != e.Version {
				return fmt.Errorf("%w: bundle entry %s is expected from the bench, but %s has %s version %q",
					resolver.ErrMissing, e.Identifier(), benchPath, e.App, v)
			}
			fmt.Printf("\n=== [%d/%d] %s — provided by the bench, not reinstalled ===\n", i+1, len(m.InstallOrder), e.Identifier())
			continue
		}
		file := filepath.Join(dir, e.File)
		if _, err := os.Stat(file); err != nil {
			return fmt.Errorf("bundle entry %s: package file %s is missing", e.Identifier(), file)
		}
		fmt.Printf("\n=== [%d/%d] %s ===\n", i+1, len(m.InstallOrder), e.Identifier())
		if err := installOne(cmd, file, benchPath, siteName, cfg); err != nil {
			return fmt.Errorf("bundle install stopped at %s (%d of %d): %w", e.Identifier(), i+1, len(m.InstallOrder), err)
		}
	}
	fmt.Printf("\nBundle %s installed: %d package(s).\n", dir, len(m.InstallOrder))
	return nil
}

func init() {
	bundleCmd.Flags().StringVarP(&bundleOutput, "output", "o", "", "Directory to write the bundle to (default: <app>-<version>-bundle)")
	bundleCmd.Flags().BoolVar(&bundleRemote, "remote", false, "Fetch required packages missing from the local store from the configured repositories")
	bundleCmd.Flags().StringVar(&bundleRepo, "repo", "", "Only fetch from this configured repository (implies --remote)")
	bundleCmd.Flags().StringVar(&bundleBenchPath, "bench-path", "", "Treat required apps already present in this bench as provided by the target image/bench: listed in the manifest, not shipped")
	bundleCmd.PreRun = func(cmd *cobra.Command, args []string) {
		if bundleRepo != "" {
			bundleRemote = true
		}
	}
	rootCmd.AddCommand(bundleCmd)
}
