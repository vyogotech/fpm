package appstore

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"fpm/internal/config"
	"fpm/internal/metadata"
	"fpm/internal/utils"
)

// ManageAppInLocalStore takes an FPM package file, reads its metadata,
// extracts its contents to the appropriate location within the local FPM app store,
// and also stores the original .fpm file there (prefixed with "_").
// It returns the organization, app name, version derived from the package,
// the base path where the versioned app was installed, the path to the app module
// within that installation (which would be the symlink target for 'fpm install'),
// and any error encountered.
func ManageAppInLocalStore(fpmFilePath string, cfg *config.FPMConfig) (
	installedAppOrg string,
	installedAppName string,
	installedAppVersion string,
	baseInstallPath string,
	appModuleDirInStore string,
	err error,
) {
	// Read Metadata from the FPM archive
	appMeta, err := metadata.ReadMetadataFromFPMArchive(fpmFilePath)
	if err != nil {
		return "", "", "", "", "", fmt.Errorf("failed to read metadata from FPM package %s: %w", fpmFilePath, err)
	}

	if appMeta.Org == "" || appMeta.AppName == "" || appMeta.PackageVersion == "" {
		return "", "", "", "", "", fmt.Errorf("package metadata in %s is incomplete (missing Org, AppName, or PackageVersion)", fpmFilePath)
	}

	installedAppOrg = appMeta.Org
	installedAppName = appMeta.AppName
	installedAppVersion = appMeta.PackageVersion

	// Determine Paths
	baseInstallPath = filepath.Join(cfg.AppsBasePath, installedAppOrg, installedAppName, installedAppVersion)
	// The app module directory inside the FPM archive is named appMeta.AppName (new structure)
	// So, when extracted, it will be at baseInstallPath/appMeta.AppName
	appModuleDirInStore = filepath.Join(baseInstallPath, installedAppName)

	// Clean/Create Directory
	fmt.Printf("Cleaning up existing local installation directory (if any): %s\n", baseInstallPath)
	if err = os.RemoveAll(baseInstallPath); err != nil {
		return "", "", "", "", "", fmt.Errorf("failed to remove existing directory in local store %s: %w", baseInstallPath, err)
	}
	fmt.Printf("Creating local installation directory: %s\n", baseInstallPath)
	if err = os.MkdirAll(baseInstallPath, 0o755); err != nil {
		return "", "", "", "", "", fmt.Errorf("failed to create directory in local store %s: %w", baseInstallPath, err)
	}

	// Extract FPM Contents
	fmt.Printf("Extracting package %s to local store %s...\n", fpmFilePath, baseInstallPath)
	r, zipErr := zip.OpenReader(fpmFilePath)
	if zipErr != nil {
		return "", "", "", "", "", fmt.Errorf("failed to open FPM package %s for extraction: %w", fpmFilePath, zipErr)
	}
	defer r.Close()

	for _, f := range r.File {
		extractedFilePath := filepath.Join(baseInstallPath, f.Name)
		// Path traversal protection
		if !strings.HasPrefix(extractedFilePath, filepath.Clean(baseInstallPath)+string(os.PathSeparator)) {
			return "", "", "", "", "", fmt.Errorf("illegal file path in FPM archive: '%s' (targets outside '%s')", f.Name, baseInstallPath)
		}

		if f.FileInfo().IsDir() {
			if err = os.MkdirAll(extractedFilePath, 0o755); err != nil {
				return "", "", "", "", "", fmt.Errorf("failed to create directory structure %s during extraction: %w", extractedFilePath, err)
			}
			continue
		}

		// Create parent directory for the file
		if err = os.MkdirAll(filepath.Dir(extractedFilePath), 0o755); err != nil { // Ensure parent dir uses 0755
			return "", "", "", "", "", fmt.Errorf("failed to create parent directory for %s during extraction: %w", extractedFilePath, err)
		}

		outFile, openErr := os.OpenFile(extractedFilePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if openErr != nil {
			return "", "", "", "", "", fmt.Errorf("failed to open file for writing %s during extraction: %w", extractedFilePath, openErr)
		}

		rc, openErrInZip := f.Open()
		if openErrInZip != nil {
			outFile.Close() // Close outFile before returning
			return "", "", "", "", "", fmt.Errorf("failed to open file in zip %s during extraction: %w", f.Name, openErrInZip)
		}

		_, copyErr := io.Copy(outFile, rc)

		// Ensure both files are closed before checking for copy error
		closeErrRC := rc.Close()
		closeErrOutFile := outFile.Close()

		if copyErr != nil {
			return "", "", "", "", "", fmt.Errorf("failed to copy content of %s to %s during extraction: %w", f.Name, extractedFilePath, copyErr)
		}
		if closeErrRC != nil {
			return "", "", "", "", "", fmt.Errorf("failed to close zip entry %s after extraction: %w", f.Name, closeErrRC)
		}
		if closeErrOutFile != nil {
			return "", "", "", "", "", fmt.Errorf("failed to close output file %s after extraction: %w", extractedFilePath, closeErrOutFile)
		}
	}
	fmt.Printf("Successfully extracted package %s/%s version %s to local FPM store: %s\n", installedAppOrg, installedAppName, installedAppVersion, baseInstallPath)

	// Store Original FPM
	originalFPMFilename := filepath.Base(fpmFilePath)
	storedFPMName := "_" + originalFPMFilename
	destFPMPath := filepath.Join(baseInstallPath, storedFPMName)

	fmt.Printf("Storing original .fpm package from %s to %s\n", fpmFilePath, destFPMPath)
	if err = utils.CopyRegularFile(fpmFilePath, destFPMPath, 0o644); err != nil {
		// Log a warning but don't necessarily fail the whole operation if only this copy fails?
		// For now, let's make it a fatal error for this function.
		return "", "", "", "", "", fmt.Errorf("failed to store original .fpm package in local store at %s: %w", destFPMPath, err)
	}
	fmt.Printf("Successfully stored original .fpm package in local store: %s\n", destFPMPath)

	return installedAppOrg, installedAppName, installedAppVersion, baseInstallPath, appModuleDirInStore, nil
}
