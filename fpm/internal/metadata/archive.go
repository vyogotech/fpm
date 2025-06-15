package metadata

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
)

// ReadMetadataFromFPMArchive reads the app_metadata.json file from the root of an FPM archive.
func ReadMetadataFromFPMArchive(fpmFilePath string) (*AppMetadata, error) {
	r, err := zip.OpenReader(fpmFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open FPM package %s: %w", fpmFilePath, err)
	}
	defer r.Close()

	var metaFile *zip.File
	for _, f := range r.File {
		// app_metadata.json should be at the root of the zip archive
		if f.Name == "app_metadata.json" {
			metaFile = f
			break
		}
	}

	if metaFile == nil {
		return nil, fmt.Errorf("app_metadata.json not found in FPM package %s", fpmFilePath)
	}

	rc, err := metaFile.Open()
	if err != nil {
		return nil, fmt.Errorf("failed to open app_metadata.json in FPM package: %w", err)
	}
	defer rc.Close()

	metaBytes, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("failed to read app_metadata.json from FPM package: %w", err)
	}

	var appMeta AppMetadata // AppMetadata is defined in metadata.go in the same package
	if err := json.Unmarshal(metaBytes, &appMeta); err != nil {
		return nil, fmt.Errorf("failed to parse app_metadata.json from FPM package (%s): %w", fpmFilePath, err)
	}
	return &appMeta, nil
}
