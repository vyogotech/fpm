package archive

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"fpm/internal/metadata" // Import the metadata package

	"github.com/sabhiram/go-gitignore" // For .fpmignore
)

var defaultIgnorePatterns = []string{
	".git/",
	"*.pyc",
	"__pycache__/",
	".DS_Store",
	"*.swp",
	"*.swo",
	"*.bak",
	"*.tmp",
	".idea/",
	".vscode/",
	"*.log",
}

// CreateFPMArchive creates an .fpm package from the app source.
// appSourcePath: Path to the Frappe app's source directory.
// outputPath: Directory where the .fpm file should be saved.
// meta: The AppMetadata for the package.
// version: The specific version string for this package.
func CreateFPMArchive(appSourcePath string, outputPath string, meta *metadata.AppMetadata, version string) error {
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
	var ignorer *ignore.GitIgnore // Changed gitignore to ignore
	if _, err := os.Stat(ignoreFilePath); err == nil {
		ignorer, err = ignore.CompileIgnoreFile(ignoreFilePath) // Changed gitignore to ignore
		if err != nil {
			return fmt.Errorf("failed to compile .fpmignore: %w", err)
		}
	} else {
		// Use default patterns if .fpmignore doesn't exist
		ignorer = ignore.CompileIgnoreLines(defaultIgnorePatterns...) // Changed gitignore to ignore
	}

	// --- Copy app source files ---
	appSourceStagePath := filepath.Join(stagingDir, "app_source")
	if err := os.MkdirAll(appSourceStagePath, 0755); err != nil {
		return fmt.Errorf("failed to create app_source in staging: %w", err)
	}

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
		if ignorer.MatchesPath(relPath) {
			if d.IsDir() {
				return filepath.SkipDir // Skip ignored directories
			}
			return nil // Skip ignored files
		}

		targetPath := filepath.Join(appSourceStagePath, relPath)
		if d.IsDir() {
			return os.MkdirAll(targetPath, 0755) // Use fixed permissions for staging directories
		}

		return copyFile(path, targetPath) // copyFile will handle file permissions
	})
	if err != nil {
		return fmt.Errorf("failed to walk and copy app source directory: %w", err)
	}


	// --- Save app_metadata.json ---
	// Ensure version in metadata is the one passed to this function
	meta.PackageVersion = version
	if err := metadata.SaveAppMetadata(stagingDir, meta); err != nil { // Save at the root of staging
		return fmt.Errorf("failed to save app_metadata.json: %w", err)
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
		stagedCompiledAssetsPath := filepath.Join(stagingDir, "compiled_assets")
		if err := copyDir(compiledAssetsPath, stagedCompiledAssetsPath, ignorer, absAppSourcePath); err != nil {
			return fmt.Errorf("failed to copy compiled_assets: %w", err)
		}
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
		header.Name = zipPath // Ensure correct name in archive
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
        if ignorer != nil && pathRelativeToIgnoreRoot != "" && ignorer.MatchesPath(pathRelativeToIgnoreRoot) {
            if d.IsDir() {
                return filepath.SkipDir
            }
            return nil
        }

        targetPath := filepath.Join(dstDir, relPathFromSrcRoot)

        if d.IsDir() {
            return os.MkdirAll(targetPath, 0755) // Use fixed permissions for staging directories
        }
        return copyFile(path, targetPath) // copyFile will handle file permissions
    })
}
