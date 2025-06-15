package utils

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// CalculateDirectoryChecksum calculates a SHA256 checksum for the contents of a directory.
// It includes file names (relative paths) and file contents.
// Entries are sorted before hashing to ensure consistency.
// ignoreFileName specifies a file name to exclude from the checksum calculation if it's at the root of dirPath.
func CalculateDirectoryChecksum(dirPath string, ignoreFileName string) (string, error) {
	hash := sha256.New()
	var paths []string

	err := filepath.WalkDir(dirPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(dirPath, path)
		if err != nil {
			return err
		}

		// Skip the root directory itself
		if relPath == "." {
			return nil
		}

		// Skip the ignoreFileName if it's at the root of dirPath
		if ignoreFileName != "" && relPath == ignoreFileName && filepath.Dir(relPath) == "." {
			if d.IsDir() { // If the ignored item is a directory, skip the whole directory
				return filepath.SkipDir
			}
			return nil // If it's a file, just skip this entry
		}

		paths = append(paths, relPath)
		return nil
	})

	if err != nil {
		return "", err
	}

	// Sort paths for consistent hash calculation
	sort.Strings(paths)

	for _, relPath := range paths {
		fullPath := filepath.Join(dirPath, relPath)
		// Use Lstat to handle symlinks as symlinks (hash their path and target, not content)
		info, statErr := os.Lstat(fullPath)
		if statErr != nil {
			// File might have been removed/altered between WalkDir and Lstat
			if os.IsNotExist(statErr) { // If file gone, may contribute to inconsistency, error out
				return "", statErr
			}
			// Other stat errors are also problematic
			return "", statErr
		}

		// Add relative path to hash (ensures file name changes and moves affect checksum)
		// Use ToSlash for consistent path separators across OS
		_, err = hash.Write([]byte(filepath.ToSlash(relPath)))
		if err != nil {
			return "", err
		}

		// If it's a symbolic link, hash the link's target path
		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(fullPath)
			if err != nil {
				return "", err
			}
			_, err = hash.Write([]byte(linkTarget))
			if err != nil {
				return "", err
			}
		} else if !info.IsDir() { // Only hash content for regular files
			file, err := os.Open(fullPath)
			if err != nil {
				if os.IsNotExist(err) { // File gone, error out
					return "", err
				}
				return "", err // Other open errors
			}

			// This structure ensures file is closed before checking io.Copy error
			var copyErr error
			func() {
				defer file.Close()
				_, copyErr = io.Copy(hash, file)
			}() // Note: self-invoking function to manage defer scope

			if copyErr != nil {
				return "", copyErr
			}
		}
		// Directories contribute their path (already added) and the paths of their contents (also added via sorted paths list)
		// Their actual content (i.e., list of files) is implicitly part of the sorted `paths` list.
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

// CalculateFileChecksum calculates the SHA256 checksum of a single file.
func CalculateFileChecksum(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to open file %s for checksum: %w", filePath, err)
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("failed to copy file content for checksum (%s): %w", filePath, err)
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}
