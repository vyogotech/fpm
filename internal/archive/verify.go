package archive

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"
)

// metadataFileName is excluded from the content checksum because it is the file
// that carries the checksum value itself.
const metadataFileName = "app_metadata.json"

// CalculateArchiveContentChecksum recomputes the content checksum of a .fpm archive
// directly from its entries. It mirrors utils.CalculateDirectoryChecksum applied to
// the staging directory the archive was built from: entries are sorted by relative
// path, each contributes its path to the hash, and regular files additionally
// contribute their content.
//
// This is deliberately distinct from utils.CalculateFileChecksum, which hashes the
// raw .fpm bytes. The two answer different questions: this one verifies the payload
// survived intact, the file hash verifies the transfer did.
func CalculateArchiveContentChecksum(fpmFilePath string) (string, error) {
	r, err := zip.OpenReader(fpmFilePath)
	if err != nil {
		return "", fmt.Errorf("failed to open FPM package %s: %w", fpmFilePath, err)
	}
	defer r.Close()

	type entry struct {
		relPath string
		file    *zip.File // nil for directory entries
	}

	var entries []entry
	for _, f := range r.File {
		relPath := strings.TrimSuffix(f.Name, "/")
		// Skip the archive root and the metadata file holding this checksum.
		if relPath == "" || relPath == metadataFileName {
			continue
		}
		if strings.HasSuffix(f.Name, "/") {
			entries = append(entries, entry{relPath: relPath})
			continue
		}
		entries = append(entries, entry{relPath: relPath, file: f})
	}

	// Sort for a stable hash, matching CalculateDirectoryChecksum.
	sort.Slice(entries, func(i, j int) bool { return entries[i].relPath < entries[j].relPath })

	hash := sha256.New()
	for _, e := range entries {
		if _, err := hash.Write([]byte(e.relPath)); err != nil {
			return "", err
		}
		if e.file == nil { // Directories contribute their path only.
			continue
		}

		rc, err := e.file.Open()
		if err != nil {
			return "", fmt.Errorf("failed to open %s in FPM package: %w", e.relPath, err)
		}
		_, copyErr := io.Copy(hash, rc)
		closeErr := rc.Close()
		if copyErr != nil {
			return "", fmt.Errorf("failed to read %s from FPM package: %w", e.relPath, copyErr)
		}
		if closeErr != nil {
			return "", fmt.Errorf("failed to close %s in FPM package: %w", e.relPath, closeErr)
		}
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

// VerifyArchiveContentChecksum compares the checksum recorded in the archive's
// app_metadata.json against one recomputed from the archive's actual contents,
// detecting tampering with the payload between packaging and publishing.
// An archive with no recorded checksum is reported as unverifiable, not as valid.
func VerifyArchiveContentChecksum(fpmFilePath string, recordedChecksum string) error {
	if recordedChecksum == "" {
		return fmt.Errorf("no content checksum recorded in %s", metadataFileName)
	}

	actual, err := CalculateArchiveContentChecksum(fpmFilePath)
	if err != nil {
		return err
	}
	if actual != recordedChecksum {
		return fmt.Errorf("content checksum mismatch: %s records %s but archive contents hash to %s",
			metadataFileName, recordedChecksum, actual)
	}
	return nil
}
