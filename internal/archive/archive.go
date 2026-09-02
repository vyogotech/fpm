package archive

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"strings"

	"fpm/internal/metadata" // Import the metadata package
	"fpm/internal/utils"    // Import for checksum calculation
	"fpm/internal/wheels"   // Import for vendoring Python dependencies

	"github.com/sabhiram/go-gitignore" // For .fpmignore
)

var defaultIgnorePatterns = []string{
	".git/",
	"*.pyc",
	"__pycache__/",
	// Node packages are a build-time input for producing JS/CSS (installed by
	// `fpm package --bench-path` when the app has a package.json); the package ships
	// the built output under <app>/public/dist instead.
	"node_modules/",
	// vite-plugin-pwa writes its dev-server service worker here. It is a dev
	// artifact, listed in crm's own .gitignore, and never served from a package.
	// The real frontend output lives in <app>/public/frontend and is not ignored.
	"dev-dist/",
	".DS_Store",
	"*.swp",
	"*.swo",
	"*.bak",
	"*.tmp",
	".idea/",
	".vscode/",
	"*.log",
	// Repository furniture, not app content: CI workflows, issue templates and the
	// screenshots a README embeds. drive ships 7.2 MB of them, which a bench neither
	// serves nor imports — and which pushed its artifact over a registry's upload
	// limit. The app's own LICENSE and README stay; these never run anywhere.
	".github/",
	".gitlab/",
	".circleci/",
	".gitattributes",
	".editorconfig",
	".pre-commit-config.yaml",
}

var productionExclusionPatterns = []string{
	"/.git/",       // Matches .git directory only at the root of the app source path
	"__pycache__/", // Matches __pycache__ directories anywhere
	"*.pyc",        // Matches .pyc files anywhere
	"tests/",       // Matches directories named "tests" anywhere and their contents
	"test_*",       // Matches files or directories starting with "test_" anywhere
}

// Options controls optional packaging behaviour. The zero value packages the app
// exactly as before wheel vendoring existed.
type Options struct {
	// BundleDeps bundles the app's Python dependencies into the package so that
	// installing it does not require network access.
	BundleDeps bool
	// WheelTarget is the platform and interpreter to vendor wheels for. The zero
	// value vendors for the packaging host.
	WheelTarget wheels.Target
	// DependencyOverrides replace requirements the app declares, as full specifiers
	// ("pycrdt>=0.14.4"). They are applied to the staged copy — never to the source
	// tree — before wheels are vendored, so the package ships the replacement and the
	// wheels beside it agree with it.
	DependencyOverrides []string
	// bundle performs the dependency bundling, defaulting to wheels.Bundle. Tests
	// substitute it to exercise the staging order without requiring pip or network.
	bundle bundleFunc
}

// bundleFunc resolves the dependencies declared in appDir into destDir for the given
// target, reporting what was bundled.
type bundleFunc func(appDir, destDir string, target wheels.Target) (wheels.Result, error)

// CreateFPMArchive creates an .fpm package from the app source.
// appSourcePath: Path to the Frappe app's source directory.
// outputPath: Directory where the .fpm file should be saved.
// meta: The AppMetadata for the package.
// version: The specific version string for this package.
// opts: Optional packaging behaviour; omit for the default packaging path.
func CreateFPMArchive(appSourcePath string, outputPath string, meta *metadata.AppMetadata, version string, opts ...Options) error {
	var options Options
	if len(opts) > 0 {
		options = opts[0]
	}
	if meta == nil {
		return errors.New("metadata cannot be nil")
	}
	if meta.PackageName == "" {
		return errors.New("package name in metadata cannot be empty")
	}
	if version == "" {
		return errors.New("version cannot be empty")
	}

	// Ensure appSourcePath is absolute and clean
	absAppSourcePath, err := filepath.Abs(appSourcePath)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for app source: %w", err)
	}

	// Create a temporary staging directory
	stagingDir, err := os.MkdirTemp("", "fpm-staging-"+meta.PackageName+"-")
	if err != nil {
		return fmt.Errorf("failed to create staging directory: %w", err)
	}
	defer os.RemoveAll(stagingDir)

	// --- Prepare .fpmignore ---
	ignoreFilePath := filepath.Join(absAppSourcePath, ".fpmignore")
	var ignorer *ignore.GitIgnore // Declare ignorer once

	// Start with default ignore patterns
	combinedIgnorePatterns := make([]string, len(defaultIgnorePatterns))
	copy(combinedIgnorePatterns, defaultIgnorePatterns)

	// Add patterns from .fpmignore if it exists
	// Use a new variable for os.Stat error to avoid shadowing function-scoped err
	if _, statErr := os.Stat(ignoreFilePath); statErr == nil {
		fpmIgnoreBytes, readErr := os.ReadFile(ignoreFilePath) // Use new variable for ReadFile error
		if readErr != nil {
			return fmt.Errorf("failed to read .fpmignore file %s: %w", ignoreFilePath, readErr)
		}
		fpmIgnoreLines := strings.Split(string(fpmIgnoreBytes), "\n")
		for _, line := range fpmIgnoreLines {
			trimmedLine := strings.TrimSpace(line)
			if trimmedLine != "" && !strings.HasPrefix(trimmedLine, "#") { // Ignore empty lines and comments
				// Check for duplicates before appending, though CompileIgnoreLines might handle it
				alreadyExists := false
				for _, existingPattern := range combinedIgnorePatterns {
					if trimmedLine == existingPattern {
						alreadyExists = true
						break
					}
				}
				if !alreadyExists {
					combinedIgnorePatterns = append(combinedIgnorePatterns, trimmedLine)
				}
			}
		}
	}

	// If package type is "prod", add production-specific exclusion patterns
	if meta.PackageType == "prod" {
		for _, prodPattern := range productionExclusionPatterns {
			alreadyExists := false
			for _, existingPattern := range combinedIgnorePatterns {
				if prodPattern == existingPattern {
					alreadyExists = true
					break
				}
			}
			if !alreadyExists {
				combinedIgnorePatterns = append(combinedIgnorePatterns, prodPattern)
			}
		}
	}

	ignorer = ignore.CompileIgnoreLines(combinedIgnorePatterns...)

	// --- Copy app source files ---
	// appSourceStagePath := filepath.Join(stagingDir, "app_source") // No longer using app_source intermediate dir
	// if err := os.MkdirAll(appSourceStagePath, 0755); err != nil { // Not needed anymore
	// 	return fmt.Errorf("failed to create app_source in staging: %w", err)
	// }

	// This is the main WalkDir for copying app source files
	err = filepath.WalkDir(absAppSourcePath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(absAppSourcePath, path)
		if err != nil {
			return fmt.Errorf("failed to get relative path for %s: %w", path, err)
		}

		// Skip root
		if relPath == "." {
			return nil
		}

		// Skip files/dirs that are handled separately or should not be in app_source
		// These checks are for items at the root of absAppSourcePath
		if filepath.Dir(relPath) == "." { // Check if it's a root item
			switch relPath {
			case "compiled_assets", "requirements.txt", "package.json", "install_hooks.py", "app_metadata.json", ".fpmignore":
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil // Skip this file
			}
		}

		// Check against ignorer (relative to appSourcePath)
		// go-gitignore expects paths relative to the .fpmignore file's location (absAppSourcePath)
		// The app's main module directory (meta.AppName) should not be skipped by top-level rules like "test*"
		// Its contents will be evaluated individually.
		if relPath != meta.AppName && ignorer.MatchesPath(ignoreMatchPath(relPath, d.IsDir())) { // This ignorer now includes prod patterns if applicable
			if d.IsDir() {
				return filepath.SkipDir // Skip ignored directories
			}
			return nil // Skip ignored files
		}

		// Determine target path based on new structure
		// If relPath is part of the app module (e.g., meta.AppName/somefile.py), it goes to stagingDir/meta.AppName/somefile.py
		// If relPath is a root file/dir (e.g., assets/icon.png), it goes to stagingDir/assets/icon.png
		targetPath := filepath.Join(stagingDir, relPath) // All files/dirs are now relative to stagingDir root

		if skip, err := skipSymlink(path, d); err != nil {
			return err
		} else if skip {
			return nil
		}

		if d.IsDir() {
			// Special handling for the app module directory itself (meta.AppName)
			// It should be created directly in stagingDir.
			// Other directories are also created directly in stagingDir.
			return os.MkdirAll(targetPath, 0755) // Use fixed permissions for staging directories
		}

		return copyFile(path, targetPath) // copyFile will handle file permissions
	})
	if err != nil {
		return fmt.Errorf("failed to walk and copy app source directory: %w", err)
	}

	// --- Copy other standard files (requirements.txt, package.json, install_hooks.py) ---
	otherFiles := []string{"requirements.txt", "package.json", "install_hooks.py"}
	for _, fName := range otherFiles {
		srcFile := filepath.Join(absAppSourcePath, fName)
		if _, err := os.Stat(srcFile); err == nil { // if file exists
			if err := copyFile(srcFile, filepath.Join(stagingDir, fName)); err != nil {
				return fmt.Errorf("failed to copy %s: %w", fName, err)
			}
		}
	}

	// --- Handle compiled_assets ---
	compiledAssetsPath := filepath.Join(absAppSourcePath, "compiled_assets")
	if _, err := os.Stat(compiledAssetsPath); err == nil { // if dir exists
		stagedCompiledAssetsPath := filepath.Join(stagingDir, "compiled_assets") // Directly into stagingDir
		// The ignorer passed to copyDir should be the potentially combined one
		if err := copyDir(compiledAssetsPath, stagedCompiledAssetsPath, ignorer, absAppSourcePath); err != nil {
			return fmt.Errorf("failed to copy compiled_assets: %w", err)
		}
	}

	// --- Vendor Python wheels ---
	// Runs before the checksum below so vendored wheels are covered by the integrity
	// hash like every other staged file.
	if options.BundleDeps {
		bundleInto := options.bundle
		if bundleInto == nil {
			bundleInto = wheels.Bundle
		}
		// An upstream pin that cannot be satisfied for the target is replaced here,
		// in the staged copy: `fpm install` runs pip against the manifest the package
		// ships, so overriding only what pip downloads would produce a package whose
		// own manifest rejects its vendored wheels.
		if len(options.DependencyOverrides) > 0 {
			applied, overrideErr := wheels.ApplyOverrides(stagingDir, options.DependencyOverrides)
			if overrideErr != nil {
				return overrideErr
			}
			meta.DependencyOverrides = applied
			for _, o := range applied {
				fmt.Printf("Overriding declared dependency %s\n", o)
			}
		}

		// Read manifests from the staging directory rather than the source tree, so
		// bundling reflects exactly what the package ships.
		wheelsStagePath := filepath.Join(stagingDir, wheels.DirName)
		vendored, vendorErr := bundleInto(stagingDir, wheelsStagePath, options.WheelTarget)
		if vendorErr != nil {
			return fmt.Errorf("failed to vendor wheels: %w", vendorErr)
		}
		if vendored.Bundled {
			meta.WheelPlatform = options.WheelTarget.Tag()
			meta.WheelPythonVersion = options.WheelTarget.PythonVersion
		}
	}

	// --- Stage app icon into package root ---
	if err := stageAppIcon(absAppSourcePath, stagingDir, meta); err != nil {
		return fmt.Errorf("failed to stage app icon: %w", err)
	}

	// --- Calculate checksum over the fully staged payload ---
	// This must run after every file that ends up in the archive has been staged
	// (app source, requirements.txt, package.json, install_hooks.py, compiled_assets),
	// otherwise those files would be excluded from the integrity hash.
	// app_metadata.json is excluded because it carries this checksum itself.
	checksum, checksumErr := utils.CalculateDirectoryChecksum(stagingDir, "app_metadata.json")
	if checksumErr != nil {
		return fmt.Errorf("failed to calculate content checksum for stagingDir '%s': %w", stagingDir, checksumErr)
	}
	meta.ContentChecksum = checksum

	// --- Save app_metadata.json ---
	// The 'meta' object now includes PackageName, PackageVersion, potentially AppName, Org,
	// SourceControlURL, PackageType, and the newly added ContentChecksum.
	if err := metadata.SaveAppMetadata(stagingDir, meta); err != nil { // Save at the root of staging
		return fmt.Errorf("failed to save app_metadata.json: %w", err)
	}

	// --- Create the .fpm ZIP archive ---
	outputFilename := fmt.Sprintf("%s-%s.fpm", meta.PackageName, version)
	outputFilePath := filepath.Join(outputPath, outputFilename)

	// Ensure output directory exists
	if err := os.MkdirAll(outputPath, 0755); err != nil {
		return fmt.Errorf("failed to create output directory %s: %w", outputPath, err)
	}

	archiveFile, err := os.Create(outputFilePath)
	if err != nil {
		return fmt.Errorf("failed to create archive file %s: %w", outputFilePath, err)
	}
	defer archiveFile.Close()

	zipWriter := zip.NewWriter(archiveFile)
	defer zipWriter.Close()

	err = filepath.WalkDir(stagingDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == stagingDir { // Skip root of staging dir itself
			return nil
		}

		relPath, err := filepath.Rel(stagingDir, path)
		if err != nil {
			return fmt.Errorf("failed to get relative path for %s in staging: %w", path, err)
		}

		// Normalize path separators for zip file
		zipPath := filepath.ToSlash(relPath)

		if d.IsDir() {
			_, err = zipWriter.Create(zipPath + "/")
			return err
		}

		fileToZip, err := os.Open(path)
		if err != nil {
			return err
		}
		defer fileToZip.Close()

		info, err := d.Info()
		if err != nil {
			return err
		}

		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = zipPath       // Ensure correct name in archive
		header.Method = zip.Deflate // Use compression

		writer, err := zipWriter.CreateHeader(header)
		if err != nil {
			return err
		}
		_, err = io.Copy(writer, fileToZip)
		return err
	})

	if err != nil {
		// Attempt to remove partially created archive on error
		os.Remove(outputFilePath)
		return fmt.Errorf("failed to create zip archive: %w", err)
	}

	return nil
}

// ignoreMatchPath renders a path the way gitignore-style directory patterns expect
// it: a directory is matched as "name/", so a pattern such as "node_modules/" or
// "tests/" excludes the directory itself (and the walk skips it) rather than only
// the files inside it, which would leave empty directory entries in the archive.
func ignoreMatchPath(relPath string, isDir bool) string {
	p := filepath.ToSlash(relPath)
	if isDir && !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}

// copyFile copies a single file from src to dst
func copyFile(src, dst string) error {
	sourceFileStat, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !sourceFileStat.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", src)
	}

	source, err := os.Open(src)
	if err != nil {
		return err
	}
	defer source.Close()

	destination, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destination.Close()

	if _, err := io.Copy(destination, source); err != nil {
		return err
	}
	// Set standard permissions for staged files
	return os.Chmod(dst, 0644)
}

// skipSymlink decides what to do with a symlink met during staging. A link to
// a regular file is followed and copied like any file. A dangling link or a
// link to a directory is skipped with a warning: apps check in links to
// install-time artifacts (wiki's public/node_modules, for one), and a zip
// package can represent neither a broken link nor safely embed a foreign tree.
func skipSymlink(path string, d fs.DirEntry) (bool, error) {
	if d.Type()&fs.ModeSymlink == 0 {
		return false, nil
	}
	info, err := os.Stat(path) // follows the link
	if err != nil {
		fmt.Printf("Warning: skipping dangling symlink %s\n", path)
		return true, nil
	}
	if info.IsDir() {
		fmt.Printf("Warning: skipping directory symlink %s\n", path)
		return true, nil
	}
	return false, nil
}

// copyDir recursively copies a directory from src to dst, respecting ignore rules
// ignorer and ignoreRootPath are used for .fpmignore checks
func copyDir(srcDir, dstDir string, ignorer *ignore.GitIgnore, ignoreRootPath string) error { // Changed gitignore to ignore
	return filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relPathFromSrcRoot, err := filepath.Rel(srcDir, path)
		if err != nil {
			return fmt.Errorf("failed to get relative path for %s from %s: %w", path, srcDir, err)
		}

		// For ignore checks, we need the path relative to where .fpmignore would be (appSourcePath)
		pathRelativeToIgnoreRoot, err := filepath.Rel(ignoreRootPath, path)
		if err != nil {
			// This might happen if compiled_assets is outside appSourcePath, handle as needed
			// For now, assume it's inside or at same level and ignore check won't apply if outside
		}

		if relPathFromSrcRoot == "." { // Skip the root itself for processing, but ensure dstDir is created
			return os.MkdirAll(dstDir, 0755)
		}

		// Check against ignorer if pathRelativeToIgnoreRoot is valid
		if ignorer != nil && pathRelativeToIgnoreRoot != "" && ignorer.MatchesPath(ignoreMatchPath(pathRelativeToIgnoreRoot, d.IsDir())) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		targetPath := filepath.Join(dstDir, relPathFromSrcRoot)

		if skip, err := skipSymlink(path, d); err != nil {
			return err
		} else if skip {
			return nil
		}

		if d.IsDir() {
			return os.MkdirAll(targetPath, 0755) // Use fixed permissions for staging directories
		}
		return copyFile(path, targetPath) // copyFile will handle file permissions
	})
}

// stageAppIcon finds and copies the app's icon into the root of the staging directory
// as icon.<ext> (e.g. icon.svg, icon.png), and records the filename in meta.IconFile.
func stageAppIcon(absAppSourcePath, stagingDir string, meta *metadata.AppMetadata) error {
	if meta == nil {
		return nil
	}

	var candidatePaths []string

	// 1. If meta.Icon or meta.IconFile is specified, check if it resolves to a physical file
	for _, iconRef := range []string{meta.IconFile, meta.Icon} {
		if iconRef == "" {
			continue
		}
		// Strip web asset prefix like /assets/<appName>/
		assetPrefix := "/assets/" + meta.AppName + "/"
		cleanRef := iconRef
		if strings.HasPrefix(cleanRef, assetPrefix) {
			cleanRef = strings.TrimPrefix(cleanRef, assetPrefix)
			candidatePaths = append(candidatePaths,
				filepath.Join(absAppSourcePath, meta.AppName, "public", cleanRef),
				filepath.Join(absAppSourcePath, "public", cleanRef),
				filepath.Join(absAppSourcePath, meta.AppName, cleanRef),
			)
		} else if strings.HasPrefix(cleanRef, "/") {
			cleanRef = strings.TrimPrefix(cleanRef, "/")
			candidatePaths = append(candidatePaths,
				filepath.Join(absAppSourcePath, meta.AppName, "public", cleanRef),
				filepath.Join(absAppSourcePath, "public", cleanRef),
				filepath.Join(absAppSourcePath, cleanRef),
			)
		} else {
			candidatePaths = append(candidatePaths,
				filepath.Join(absAppSourcePath, cleanRef),
				filepath.Join(absAppSourcePath, meta.AppName, cleanRef),
				filepath.Join(absAppSourcePath, meta.AppName, "public", cleanRef),
				filepath.Join(absAppSourcePath, meta.AppName, "public", "images", cleanRef),
			)
		}
	}

	// 2. Standard filesystem icon/logo candidate paths
	appName := meta.AppName
	standardPaths := []string{
		filepath.Join(absAppSourcePath, appName, "public", "images", appName+".svg"),
		filepath.Join(absAppSourcePath, appName, "public", "images", appName+".png"),
		filepath.Join(absAppSourcePath, appName, "public", "images", appName+"-logo.svg"),
		filepath.Join(absAppSourcePath, appName, "public", "images", appName+"-logo.png"),
		filepath.Join(absAppSourcePath, appName, "public", "images", "logo.svg"),
		filepath.Join(absAppSourcePath, appName, "public", "images", "logo.png"),
		filepath.Join(absAppSourcePath, appName, "public", "images", "icon.svg"),
		filepath.Join(absAppSourcePath, appName, "public", "images", "icon.png"),
		filepath.Join(absAppSourcePath, appName, "public", "icon.svg"),
		filepath.Join(absAppSourcePath, appName, "public", "icon.png"),
		filepath.Join(absAppSourcePath, appName, "public", "logo.svg"),
		filepath.Join(absAppSourcePath, appName, "public", "logo.png"),
		filepath.Join(absAppSourcePath, "icon.svg"),
		filepath.Join(absAppSourcePath, "icon.png"),
		filepath.Join(absAppSourcePath, "logo.svg"),
		filepath.Join(absAppSourcePath, "logo.png"),
	}
	candidatePaths = append(candidatePaths, standardPaths...)

	for _, p := range candidatePaths {
		info, err := os.Stat(p)
		if err == nil && !info.IsDir() {
			ext := strings.ToLower(filepath.Ext(p))
			if ext == ".svg" || ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".webp" || ext == ".ico" {
				iconFilename := "icon" + ext
				destPath := filepath.Join(stagingDir, iconFilename)
				if err := copyFile(p, destPath); err != nil {
					return fmt.Errorf("failed to copy icon file to staging root: %w", err)
				}
				meta.IconFile = iconFilename
				if meta.Icon == "" {
					meta.Icon = iconFilename
				}
				return nil
			}
		}
	}
	return nil
}
