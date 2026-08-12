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
// A repository that records no checksum is rejected rather than trusted. Accepting an
// unverifiable package would let any repository skip verification simply by omitting the
// field, which is a weaker guarantee than having no verification at all: the client would
// report success while checking nothing.
func verifyFPMFileOrRemove(filePath, expectedChecksum, packageDescription string) error {
	if expectedChecksum == "" {
		os.Remove(filePath)
		return fmt.Errorf("repository metadata records no checksum for %s, so the package "+
			"cannot be verified. Republish it with a current version of fpm to record one",
			packageDescription)
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
