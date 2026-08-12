package repository

import (
	"fmt"
	"os"

	"fpm/internal/utils"
)

// verifyFPMFileOrRemove checks a .fpm file against the SHA-256 recorded in the
// repository's package metadata, which covers the raw archive bytes and so detects a
// corrupted or substituted download.
//
// On mismatch, or when the file cannot be hashed, the offending file is removed before
// the error is returned so a bad artifact is never left behind in the cache for a later
// run to pick up.
//
// When the repository records no checksum the file cannot be verified at all. That is
// reported as a warning rather than an error, since packages published before checksums
// were recorded would otherwise become uninstallable.
func verifyFPMFileOrRemove(filePath, expectedChecksum, packageDescription string) error {
	if expectedChecksum == "" {
		fmt.Fprintf(os.Stderr,
			"Warning: repository metadata records no checksum for %s; the integrity of %s cannot be verified.\n",
			packageDescription, filePath)
		return nil
	}

	actualChecksum, err := utils.CalculateFileChecksum(filePath)
	if err != nil {
		os.Remove(filePath)
		return fmt.Errorf("failed to calculate checksum for %s: %w", filePath, err)
	}

	if actualChecksum != expectedChecksum {
		os.Remove(filePath)
		return fmt.Errorf("checksum mismatch for %s: repository metadata records %s but the file hashes to %s",
			packageDescription, expectedChecksum, actualChecksum)
	}

	return nil
}
