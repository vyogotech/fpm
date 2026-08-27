package utils

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// CopyRegularFile copies a single regular file from src to dst.
// It creates dst if it does not exist, or truncates it if it does.
// The permission bits of the destination file are set to 'perm'.
func CopyRegularFile(src, dst string, perm os.FileMode) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open source file %s: %w", src, err)
	}
	defer srcFile.Close()

	dstFile, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("failed to create/truncate destination file %s: %w", dst, err)
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	if err != nil {
		return fmt.Errorf("failed to copy content from %s to %s: %w", src, dst, err)
	}
	return nil
}

// CopyDirectory recursively copies the directory tree from src to dst.
func CopyDirectory(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return fmt.Errorf("failed to get relative path for %s from %s: %w", path, src, err)
		}
		dstPath := filepath.Join(dst, relPath)
		if info.IsDir() {
			return os.MkdirAll(dstPath, info.Mode())
		}
		if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
			return fmt.Errorf("failed to create directory for %s: %w", dstPath, err)
		}
		return CopyRegularFile(path, dstPath, info.Mode())
	})
}

// CopyTree copies a source checkout to dst, preserving symlinks and file modes and
// skipping any directory whose base name is in skipDirs, at any depth.
//
// This is what staging an app checkout somewhere else needs, and it is deliberately
// not CopyDirectory: a checkout contains symlinks that must stay symlinks, and
// directories (.git, node_modules) that must not be copied at all.
func CopyTree(src, dst string, skipDirs map[string]bool) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		if d.IsDir() && skipDirs[d.Name()] {
			return filepath.SkipDir
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		default:
			return CopyRegularFile(p, target, info.Mode().Perm()|0o600)
		}
	})
}
