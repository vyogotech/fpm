// Package snapshot records the state of a Frappe bench and site before an fpm
// install starts. The rollback journal consults the snapshot so it never touches,
// removes, or uninstalls any app, symlink, or configuration that existed before
// the command ran.
package snapshot

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Snapshot captures the bench and site state prior to an installation session.
type Snapshot struct {
	BenchPath string
	SiteName  string

	// BenchApps maps appName -> symlink target (or "" if regular dir).
	BenchApps map[string]string

	// BenchAppVersions maps appName -> version declared in the module's __init__.py.
	BenchAppVersions map[string]string

	// AppsTxtExists reports whether sites/apps.txt existed before install.
	AppsTxtExists bool
	// AppsTxtContent is the raw content of sites/apps.txt before install.
	AppsTxtContent []byte
	// AppsTxtApps lists the app names parsed from sites/apps.txt before install.
	AppsTxtApps []string

	// AssetManifestLTRExists reports whether sites/assets/assets.json existed.
	AssetManifestLTRExists bool
	// AssetManifestLTR is the raw content of sites/assets/assets.json.
	AssetManifestLTR []byte

	// AssetManifestRTLExists reports whether sites/assets/assets-rtl.json existed.
	AssetManifestRTLExists bool
	// AssetManifestRTL is the raw content of sites/assets/assets-rtl.json.
	AssetManifestRTL []byte

	// SiteApps records apps that were already installed on siteName (if non-empty).
	SiteApps map[string]bool
}

var versionAssignment = regexp.MustCompile(`(?m)^\s*__version__\s*=\s*["']([^"']+)["']`)

// Take captures a snapshot of the bench and optional site at benchPath.
func Take(benchPath, siteName string) (*Snapshot, error) {
	absBench, err := filepath.Abs(benchPath)
	if err != nil {
		return nil, fmt.Errorf("failed to determine absolute path for bench '%s': %w", benchPath, err)
	}

	snap := &Snapshot{
		BenchPath:        absBench,
		SiteName:         siteName,
		BenchApps:        make(map[string]string),
		BenchAppVersions: make(map[string]string),
		SiteApps:         make(map[string]bool),
	}

	// 1. Scan <bench>/apps directory
	appsDir := filepath.Join(absBench, "apps")
	if entries, err := os.ReadDir(appsDir); err == nil {
		for _, e := range entries {
			appName := e.Name()
			appPath := filepath.Join(appsDir, appName)

			if linkTarget, lerr := os.Readlink(appPath); lerr == nil {
				snap.BenchApps[appName] = linkTarget
			} else {
				snap.BenchApps[appName] = ""
			}

			// Read version from <bench>/apps/<appName>/<appName>/__init__.py
			moduleInit := filepath.Join(appPath, appName, "__init__.py")
			if data, rerr := os.ReadFile(moduleInit); rerr == nil {
				if m := versionAssignment.FindSubmatch(data); m != nil {
					snap.BenchAppVersions[appName] = strings.TrimSpace(string(m[1]))
				}
			}
		}
	}

	// 2. Read sites/apps.txt
	appsTxtPath := filepath.Join(absBench, "sites", "apps.txt")
	if data, err := os.ReadFile(appsTxtPath); err == nil {
		snap.AppsTxtExists = true
		snap.AppsTxtContent = data
		for _, line := range strings.Split(string(data), "\n") {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				snap.AppsTxtApps = append(snap.AppsTxtApps, trimmed)
			}
		}
	}

	// 3. Read sites/assets/assets.json and assets-rtl.json
	ltrPath := filepath.Join(absBench, "sites", "assets", "assets.json")
	if data, err := os.ReadFile(ltrPath); err == nil {
		snap.AssetManifestLTRExists = true
		snap.AssetManifestLTR = data
	}

	rtlPath := filepath.Join(absBench, "sites", "assets", "assets-rtl.json")
	if data, err := os.ReadFile(rtlPath); err == nil {
		snap.AssetManifestRTLExists = true
		snap.AssetManifestRTL = data
	}

	return snap, nil
}

// WasPresentInBench reports whether appName was already present in <bench>/apps
// before this install session began.
func (s *Snapshot) WasPresentInBench(appName string) bool {
	if s == nil {
		return false
	}
	_, exists := s.BenchApps[appName]
	return exists
}

// PreExistingVersion returns the version of appName that existed in the bench
// before this install session, or "" if absent / undeclared.
func (s *Snapshot) PreExistingVersion(appName string) string {
	if s == nil {
		return ""
	}
	return s.BenchAppVersions[appName]
}

// WasInAppsTxt reports whether appName was listed in sites/apps.txt before this install session.
func (s *Snapshot) WasInAppsTxt(appName string) bool {
	if s == nil {
		return false
	}
	for _, a := range s.AppsTxtApps {
		if a == appName {
			return true
		}
	}
	return false
}

// WasInstalledOnSite reports whether appName was already installed on the site.
func (s *Snapshot) WasInstalledOnSite(appName string) bool {
	if s == nil {
		return false
	}
	return s.SiteApps[appName]
}

// RestoreAppsTxt writes sites/apps.txt back to its pre-install state.
func (s *Snapshot) RestoreAppsTxt() error {
	if s == nil {
		return nil
	}
	appsTxtPath := filepath.Join(s.BenchPath, "sites", "apps.txt")
	if !s.AppsTxtExists {
		if err := os.Remove(appsTxtPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove created %s: %w", appsTxtPath, err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(appsTxtPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(appsTxtPath, s.AppsTxtContent, 0o644)
}

// RestoreAssetManifests writes assets.json and assets-rtl.json back to their pre-install state.
func (s *Snapshot) RestoreAssetManifests() error {
	if s == nil {
		return nil
	}
	assetsDir := filepath.Join(s.BenchPath, "sites", "assets")
	ltrPath := filepath.Join(assetsDir, "assets.json")
	if !s.AssetManifestLTRExists {
		_ = os.Remove(ltrPath)
	} else if err := os.WriteFile(ltrPath, s.AssetManifestLTR, 0o644); err != nil {
		return err
	}

	rtlPath := filepath.Join(assetsDir, "assets-rtl.json")
	if !s.AssetManifestRTLExists {
		_ = os.Remove(rtlPath)
	} else if err := os.WriteFile(rtlPath, s.AssetManifestRTL, 0o644); err != nil {
		return err
	}
	return nil
}

// EqualAppsTxt reports whether the current content matches the snapshot.
func (s *Snapshot) EqualAppsTxt(content []byte) bool {
	if s == nil {
		return false
	}
	return bytes.Equal(s.AppsTxtContent, content)
}
