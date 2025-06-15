package utils

import (
	"fmt"
	"io"
	"os"
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

	// Check if source is actually a regular file (optional, but good practice)
	// srcInfo, err := srcFile.Stat()
	// if err != nil {
	// 	return fmt.Errorf("failed to stat source file %s: %w", src, err)
	// }
	// if !srcInfo.Mode().IsRegular() {
	// 	return fmt.Errorf("source %s is not a regular file", src)
	// }

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
