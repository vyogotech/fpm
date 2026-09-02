package repository

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// A registry behind a CDN refuses a body larger than the CDN's per-request limit —
// 100 MB on Cloudflare's Free and Pro plans, which is below what a package carrying
// vendored wheels and a compiled frontend now weighs. The limit is per *request*, so
// an artifact is uploaded as several of them and the registry assembles the object.
//
// Nothing about this changes when a version becomes visible. A consumer discovers a
// version by reading package-metadata.json, which is written only after the upload
// completes and is small enough to always be one atomic request. Parts in flight are
// referenced by nothing, and an upload that never completes leaves the key exactly as
// it was.
const (
	// MultipartThreshold is the size above which an upload is split. Chosen below the
	// 100 MB CDN limit with room for headers and encoding overhead.
	MultipartThreshold = 90 << 20
	// MultipartPartSize is the size of every part but the last. R2 requires uniform
	// parts, so this is a fixed slice rather than something adapted per file.
	MultipartPartSize = 50 << 20
)

// multipartActions are the query parameters a registry supporting split uploads
// answers on the artifact's own URL.
const (
	actionCreate     = "mpu-create"
	actionUploadPart = "mpu-uploadpart"
	actionComplete   = "mpu-complete"
	actionAbort      = "mpu-abort"
)

// UploadedPart identifies one uploaded part when completing an upload.
type UploadedPart struct {
	PartNumber int    `json:"partNumber"`
	ETag       string `json:"etag"`
}

// ErrMultipartUnsupported reports a registry that does not implement split uploads,
// so the caller can fall back to a single request.
type ErrMultipartUnsupported struct{ Status int }

func (e *ErrMultipartUnsupported) Error() string {
	return fmt.Sprintf("registry does not support split uploads (status %d)", e.Status)
}

// UploadHTTPFileMultipart uploads a file as several requests, none of which carries
// more than MultipartPartSize bytes.
//
// It returns ErrMultipartUnsupported without having uploaded anything when the
// registry does not implement the protocol, which is what lets a client talk to an
// older registry. Any failure after the upload is created aborts it, so a registry is
// not left holding parts of an artifact that will never be completed.
func UploadHTTPFileMultipart(targetURL, localFilePath string, client *http.Client, headers map[string]string) error {
	if client == nil {
		client = &http.Client{Timeout: uploadTimeout}
	}
	file, err := os.Open(localFilePath)
	if err != nil {
		return fmt.Errorf("failed to open file for upload %s: %w", localFilePath, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat %s: %w", localFilePath, err)
	}

	uploadID, err := createMultipart(targetURL, client, headers)
	if err != nil {
		return err
	}

	parts, err := uploadParts(targetURL, uploadID, file, info.Size(), client, headers)
	if err != nil {
		abortMultipart(targetURL, uploadID, client, headers)
		return err
	}
	if err := completeMultipart(targetURL, uploadID, parts, client, headers); err != nil {
		abortMultipart(targetURL, uploadID, client, headers)
		return err
	}
	fmt.Printf("Uploaded %s in %d parts.\n", localFilePath, len(parts))
	return nil
}

func createMultipart(targetURL string, client *http.Client, headers map[string]string) (string, error) {
	resp, err := doMultipart(http.MethodPost, targetURL, actionCreate, nil, nil, client, headers)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	// A registry that has never heard of the protocol answers the unknown action the
	// way it answers any unknown request; that is a fallback, not a failure.
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed ||
		resp.StatusCode == http.StatusNotImplemented {
		return "", &ErrMultipartUnsupported{Status: resp.StatusCode}
	}
	if err := statusError(resp, targetURL); err != nil {
		return "", err
	}
	var created struct {
		UploadID string `json:"uploadId"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&created); err != nil {
		return "", fmt.Errorf("could not read the upload id from %s: %w", targetURL, err)
	}
	if created.UploadID == "" {
		return "", &ErrMultipartUnsupported{Status: resp.StatusCode}
	}
	return created.UploadID, nil
}

func uploadParts(targetURL, uploadID string, file io.Reader, size int64, client *http.Client, headers map[string]string) ([]UploadedPart, error) {
	var parts []UploadedPart
	buf := make([]byte, MultipartPartSize)
	for number := 1; ; number++ {
		n, readErr := io.ReadFull(file, buf)
		if n == 0 {
			if readErr == io.EOF {
				break
			}
			if readErr != nil && readErr != io.ErrUnexpectedEOF {
				return nil, fmt.Errorf("failed to read part %d: %w", number, readErr)
			}
			break
		}
		fmt.Printf("  part %d (%.1f MB of %.1f MB)\n", number, float64(n)/(1<<20), float64(size)/(1<<20))
		query := url.Values{"uploadId": {uploadID}, "partNumber": {fmt.Sprint(number)}}
		resp, err := doMultipart(http.MethodPut, targetURL, actionUploadPart, query, bytes.NewReader(buf[:n]), client, headers)
		if err != nil {
			return nil, err
		}
		if err := statusError(resp, targetURL); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("part %d failed: %w", number, err)
		}
		var part UploadedPart
		decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&part)
		resp.Body.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("could not read the receipt for part %d: %w", number, decodeErr)
		}
		if part.ETag == "" {
			return nil, fmt.Errorf("part %d was accepted without an etag, so the upload cannot be completed", number)
		}
		part.PartNumber = number
		parts = append(parts, part)

		if readErr == io.ErrUnexpectedEOF || readErr == io.EOF {
			break
		}
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("refusing to complete an upload with no parts")
	}
	return parts, nil
}

func completeMultipart(targetURL, uploadID string, parts []UploadedPart, client *http.Client, headers map[string]string) error {
	body, err := json.Marshal(parts)
	if err != nil {
		return err
	}
	resp, err := doMultipart(http.MethodPost, targetURL, actionComplete,
		url.Values{"uploadId": {uploadID}}, bytes.NewReader(body), client, headers)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return statusError(resp, targetURL)
}

// abortMultipart is best effort: the registry expires an unfinished upload on its own,
// and the artifact key is untouched either way, so a failure here must not mask the
// failure that caused it.
func abortMultipart(targetURL, uploadID string, client *http.Client, headers map[string]string) {
	resp, err := doMultipart(http.MethodDelete, targetURL, actionAbort,
		url.Values{"uploadId": {uploadID}}, nil, client, headers)
	if err == nil {
		resp.Body.Close()
	}
}

func doMultipart(method, targetURL, action string, query url.Values, body io.Reader, client *http.Client, headers map[string]string) (*http.Response, error) {
	if query == nil {
		query = url.Values{}
	}
	query.Set("action", action)
	sep := "?"
	if strings.Contains(targetURL, "?") {
		sep = "&"
	}
	req, err := http.NewRequest(method, targetURL+sep+query.Encode(), body)
	if err != nil {
		return nil, fmt.Errorf("failed to create the %s request for %s: %w", action, targetURL, err)
	}
	req.Header.Set("User-Agent", clientUserAgent)
	req.Header.Set("Content-Type", "application/octet-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s request to %s failed: %w", action, targetURL, err)
	}
	return resp, nil
}

// statusError turns a non-success response into an HTTPStatusError, leaving the body
// readable for the caller when it succeeds.
func statusError(resp *http.Response, targetURL string) error {
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusAccepted, http.StatusNoContent:
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	return &HTTPStatusError{
		URL:        targetURL,
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Body:       strings.TrimSpace(string(body)),
	}
}
