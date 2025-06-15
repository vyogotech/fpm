package repository

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
	// "fpm/internal/config" // Not directly needed by these functions, URL is passed in
	// "fpm/internal/utils" // For checksum of uploaded file, if done client-side before upload
)

// FetchRemotePackageMetadata fetches the package-metadata.json from a remote repository.
func FetchRemotePackageMetadata(repoURL, groupID, artifactID string, client *http.Client) (*PackageMetadata, error) {
	if client == nil {
		client = &http.Client{Timeout: time.Second * 30}
	}
	userAgent := "fpm-client/0.1.0" // Consider making this global or configurable

	metadataPath := fmt.Sprintf("metadata/%s/%s/package-metadata.json", groupID, artifactID)
	fullMetadataURL, err := url.JoinPath(repoURL, metadataPath)
	if err != nil {
		return nil, fmt.Errorf("error constructing metadata URL for %s/%s on repo %s: %w", groupID, artifactID, repoURL, err)
	}

	req, err := http.NewRequest("GET", fullMetadataURL, nil)
	if err != nil {
		return nil, fmt.Errorf("error creating request for %s: %w", fullMetadataURL, err)
	}
	req.Header.Set("User-Agent", userAgent)

	fmt.Printf("Fetching remote metadata from %s...\n", fullMetadataURL)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch metadata %s: %w", fullMetadataURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// It's not an error for metadata to not exist, means package isn't there (yet)
		return nil, nil // Return nil metadata and nil error to indicate "not found"
	}
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		// resp.Body.Close() already deferred
		return nil, fmt.Errorf("failed to fetch metadata %s (status: %s). Response: %s", fullMetadataURL, resp.Status, string(bodyBytes))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		// resp.Body.Close() already deferred
		return nil, fmt.Errorf("failed to read response body from %s: %w", fullMetadataURL, err)
	}

	var pkgMeta PackageMetadata
	if err := json.Unmarshal(body, &pkgMeta); err != nil {
		return nil, fmt.Errorf("failed to parse package-metadata.json from %s: %w. Body: %s", fullMetadataURL, err, string(body))
	}
	return &pkgMeta, nil
}

// UploadHTTPFile uploads a single file using a specified HTTP method (e.g., "PUT" or "POST").
// For POST, it uses multipart/form-data and `fileFieldName` is used. `additionalFields` can add more multipart fields.
// For PUT, it sends raw file body and `contentTypeHeader` is used (e.g. "application/octet-stream").
func UploadHTTPFile(targetURL, localFilePath, httpMethod, contentTypeHeader string, client *http.Client, fileFieldName string, additionalFields map[string]string) error {
	if client == nil {
		client = &http.Client{Timeout: time.Second * 300} // Longer timeout for uploads
	}
	userAgent := "fpm-client/0.1.0"

	file, err := os.Open(localFilePath)
	if err != nil {
		return fmt.Errorf("failed to open file for upload %s: %w", localFilePath, err)
	}
	defer file.Close()

	var req *http.Request
	var reqBody io.Reader

	if httpMethod == http.MethodPost { // Typically for multipart
		bodyBuffer := &bytes.Buffer{}
		writer := multipart.NewWriter(bodyBuffer)

		if fileFieldName == "" {
			return fmt.Errorf("fileFieldName must be provided for POST multipart upload")
		}
		part, err := writer.CreateFormFile(fileFieldName, filepath.Base(localFilePath))
		if err != nil {
			return fmt.Errorf("failed to create form file for %s: %w", localFilePath, err)
		}
		if _, err = io.Copy(part, file); err != nil {
			return fmt.Errorf("failed to copy file content to multipart writer for %s: %w", localFilePath, err)
		}

		for key, val := range additionalFields {
			if err = writer.WriteField(key, val); err != nil {
				return fmt.Errorf("failed to write additional field %s: %w", key, err)
			}
		}

		err = writer.Close()
		if err != nil {
			return fmt.Errorf("failed to close multipart writer: %w", err)
		}
		reqBody = bodyBuffer
		req, err = http.NewRequest(httpMethod, targetURL, reqBody)
		if err != nil {
			return fmt.Errorf("failed to create POST request for %s: %w", targetURL, err)
		}
		req.Header.Set("Content-Type", writer.FormDataContentType())
	} else if httpMethod == http.MethodPut { // Typically for raw body
		fileInfo, statErr := file.Stat()
		if statErr != nil {
			return fmt.Errorf("failed to stat file %s: %w", localFilePath, statErr)
		}
		reqBody = file
		req, err = http.NewRequest(httpMethod, targetURL, reqBody)
		if err != nil {
			return fmt.Errorf("failed to create PUT request for %s: %w", targetURL, err)
		}
		if contentTypeHeader == "" {
			contentTypeHeader = "application/octet-stream" // Default for raw file PUT
		}
		req.Header.Set("Content-Type", contentTypeHeader)
		req.ContentLength = fileInfo.Size()
	} else {
		return fmt.Errorf("unsupported HTTP method for file upload: %s", httpMethod)
	}

	req.Header.Set("User-Agent", userAgent)

	fmt.Printf("Uploading %s to %s using %s...\n", filepath.Base(localFilePath), targetURL, httpMethod)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to upload file to %s: %w", targetURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusAccepted {
		respBodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("failed to upload file to %s (status: %s). Response: %s", targetURL, resp.Status, string(respBodyBytes))
	}

	fmt.Printf("File %s uploaded successfully to %s.\n", filepath.Base(localFilePath), targetURL)
	return nil
}


// UploadPackageMetadata uploads the package-metadata.json file to the repository using HTTP PUT.
func UploadPackageMetadata(repoBaseURL, groupID, artifactID string, metaToUpload *PackageMetadata, client *http.Client) error {
	if client == nil {
		client = &http.Client{Timeout: time.Second * 60}
	}
	userAgent := "fpm-client/0.1.0"

	jsonData, err := json.MarshalIndent(pkgMeta, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal package metadata for %s/%s: %w", pkgMeta.GroupID, pkgMeta.ArtifactID, err)
	}

	metadataPath := fmt.Sprintf("metadata/%s/%s/package-metadata.json", pkgMeta.GroupID, pkgMeta.ArtifactID)
	fullMetadataURL, err := url.JoinPath(repoURL, metadataPath)
	if err != nil {
		return fmt.Errorf("error constructing metadata upload URL for %s/%s on repo %s: %w", pkgMeta.GroupID, pkgMeta.ArtifactID, repoURL, err)
	}

	req, err := http.NewRequest(http.MethodPut, fullMetadataURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create PUT request for metadata %s: %w", fullMetadataURL, err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(jsonData)) // Set Content-Length for PUT

	fmt.Printf("Uploading metadata for %s/%s to %s...\n", groupID, artifactID, fullMetadataURL) // Use passed groupID, artifactID
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to upload metadata to %s: %w", fullMetadataURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusAccepted {
		respBodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("failed to upload metadata to %s (status: %s). Response: %s", fullMetadataURL, resp.Status, string(respBodyBytes))
	}

	fmt.Printf("Metadata for %s/%s uploaded successfully to %s.\n", pkgMeta.GroupID, pkgMeta.ArtifactID, fullMetadataURL)
	return nil
}
