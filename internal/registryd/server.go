package registryd

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"fpm/internal/archive"
	"fpm/internal/metadata"
	"fpm/internal/repository"
)

// MaxArtifactBytes bounds an upload. Artifacts bundle vendored Python wheels
// and are legitimately large, but an unbounded body from an unauthenticated
// caller is a denial-of-service primitive.
const MaxArtifactBytes = 1 << 30 // 1 GiB

// Server exposes the registry over HTTP.
//
// The route surface is deliberately identical to what nginx served, because
// the contract with every installed fpm binary is the URL, the method and the
// status code. Behaviour behind those three changes completely; the client
// cannot tell.
type Server struct {
	store *Store
	auth  Authenticator

	// onPublishersChanged persists the publisher list after an issuance, so a
	// restart does not forget a token that has already been handed out.
	onPublishersChanged func() error
}

// NewServer wires a store and an authenticator into a handler.
func NewServer(store *Store, auth Authenticator) *Server {
	return &Server{store: store, auth: auth}
}

// OnPublishersChanged registers a persistence callback for token issuance.
func (s *Server) OnPublishersChanged(fn func() error) {
	s.onPublishersChanged = fn
}

// Handler returns the router.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "healthy\n")
	})
	mux.HandleFunc("/admin/publishers", s.handleIssueToken)
	mux.HandleFunc("/", s.route)
	return withCORS(mux)
}

// withCORS applies the headers the browser-facing catalogue needs.
//
// Set once, at the top, rather than per route: in the nginx configuration
// these were declared per location, and a nested location that added its own
// header silently discarded them — so the two paths a browser most needed were
// the two that lost CORS.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")

		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Max-Age", "1728000")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	urlPath := path.Clean("/" + strings.TrimPrefix(r.URL.Path, "/"))

	switch r.Method {
	case http.MethodGet, http.MethodHead:
		s.handleGet(w, r, urlPath)
	case http.MethodPut:
		s.handlePut(w, r, urlPath)
	default:
		httpError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ── reads ───────────────────────────────────────────────────────────────────

// handleGet serves the document root. Reads are anonymous: requiring a
// credential would break every existing client, defeat CDN caching, and leak
// the credential on any cross-origin redirect — for no gain, since the
// artifacts are public.
func (s *Server) handleGet(w http.ResponseWriter, r *http.Request, urlPath string) {
	relPath := strings.TrimPrefix(urlPath, "/")
	if relPath == "" {
		httpError(w, http.StatusNotFound, "not found")
		return
	}

	if strings.HasSuffix(relPath, ".fpm") {
		data, err := s.store.ArtifactBytes(relPath)
		if err != nil {
			httpError(w, http.StatusNotFound, "not found")
			return
		}
		w.Header().Set("Content-Type", "application/x-fpm")
		w.Header().Set("Content-Disposition", "attachment")
		_, _ = w.Write(data)
		return
	}

	full := filepath.Join(s.store.Root(), filepath.FromSlash(relPath))
	data, err := os.ReadFile(full)
	if err != nil {
		// 404 on metadata is meaningful to the client — it means "not
		// published yet" — so it must stay a clean 404 rather than an error.
		httpError(w, http.StatusNotFound, "not found")
		return
	}
	if strings.HasSuffix(relPath, ".json") {
		w.Header().Set("Content-Type", "application/json")
	}
	_, _ = w.Write(data)
}

// ── writes ──────────────────────────────────────────────────────────────────

func (s *Server) handlePut(w http.ResponseWriter, r *http.Request, urlPath string) {
	publisher, ok := s.auth.Authenticate(credentialFrom(r.Header.Get("Authorization")))
	if !ok {
		// The realm header keeps `fpm` prompting for credentials exactly as it
		// did against nginx.
		w.Header().Set("WWW-Authenticate", `Basic realm="FPM Repository"`)
		httpError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	relPath := strings.TrimPrefix(urlPath, "/")

	switch {
	case strings.HasSuffix(relPath, ".fpm"):
		s.publishArtifact(w, r, relPath, publisher)
	case relPath == indexRelPath:
		// The client rewrites the index after publishing. The server derives it
		// from the packages that actually exist, so this is accepted and
		// discarded — refusing it would make `fpm publish` print a warning on
		// every successful publish.
		w.WriteHeader(http.StatusNoContent)
	case strings.HasPrefix(relPath, metadataDir+"/") && strings.HasSuffix(relPath, metadataFile):
		s.saveMetadata(w, r, relPath, publisher)
	default:
		httpError(w, http.StatusBadRequest, "unsupported path")
	}
}

// publishArtifact is the load-bearing path: it is the only place an artifact
// can enter the registry, and therefore the only place its integrity can be
// established.
func (s *Server) publishArtifact(w http.ResponseWriter, r *http.Request, relPath string, publisher Publisher) {
	org, appName, version, err := parseArtifactPath(relPath)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}

	if !publisher.CanPublishTo(org) {
		httpError(w, http.StatusForbidden,
			fmt.Sprintf("you do not have permission to publish to organisation %q", org))
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, MaxArtifactBytes+1))
	if err != nil {
		httpError(w, http.StatusBadRequest, "could not read the request body")
		return
	}
	if len(body) > MaxArtifactBytes {
		httpError(w, http.StatusRequestEntityTooLarge, "artifact is too large")
		return
	}
	if len(body) == 0 {
		httpError(w, http.StatusBadRequest, "empty artifact")
		return
	}

	manifest, err := readManifest(body)
	if err != nil {
		// Refusing here is what stops the registry serving something that is
		// not a package. The static registry stored whatever bytes arrived.
		httpError(w, http.StatusBadRequest, fmt.Sprintf("not a valid fpm package: %v", err))
		return
	}

	// The path is publisher-chosen and the manifest is inside the artifact, so
	// they must be made to agree. Otherwise a publisher could upload someone
	// else's package under their own org, or claim a version the package does
	// not declare — and every consumer resolving by coordinates would get it.
	if manifest.Org != "" && !strings.EqualFold(manifest.Org, org) {
		httpError(w, http.StatusBadRequest, fmt.Sprintf(
			"the package declares org %q but was uploaded to %q", manifest.Org, org))
		return
	}
	if manifest.AppName != "" && !strings.EqualFold(manifest.AppName, appName) {
		httpError(w, http.StatusBadRequest, fmt.Sprintf(
			"the package declares app %q but was uploaded to %q", manifest.AppName, appName))
		return
	}
	if manifest.PackageVersion != "" && manifest.PackageVersion != version {
		httpError(w, http.StatusBadRequest, fmt.Sprintf(
			"the package declares version %q but was uploaded as %q", manifest.PackageVersion, version))
		return
	}

	// Verified against the archive's own recorded checksum using the same
	// routine the client used to produce it, so the two cannot drift apart.
	if manifest.ContentChecksum != "" {
		if err := verifyContentChecksum(body, manifest.ContentChecksum); err != nil {
			httpError(w, http.StatusBadRequest, fmt.Sprintf("integrity check failed: %v", err))
			return
		}
	}

	sum := sha256.Sum256(body)
	err = s.store.Publish(PublishInput{
		Org:      org,
		AppName:  appName,
		Version:  version,
		Artifact: body,
		// The server's own hash of the bytes it received, never a value the
		// uploader supplied.
		Checksum: hex.EncodeToString(sum[:]),
		Manifest: manifest,
	})

	switch {
	case errors.Is(err, ErrVersionExists):
		httpError(w, http.StatusConflict, "this version has already been published")
	case err != nil:
		httpError(w, http.StatusInternalServerError, "could not store the package")
	default:
		w.WriteHeader(http.StatusCreated)
	}
}

func (s *Server) saveMetadata(w http.ResponseWriter, r *http.Request, relPath string, publisher Publisher) {
	parts := strings.Split(strings.TrimPrefix(relPath, metadataDir+"/"), "/")
	if len(parts) != 3 {
		httpError(w, http.StatusBadRequest, "unsupported metadata path")
		return
	}
	org, appName := parts[0], parts[1]

	if !publisher.CanPublishTo(org) {
		httpError(w, http.StatusForbidden,
			fmt.Sprintf("you do not have permission to publish to organisation %q", org))
		return
	}

	var incoming repository.PackageMetadata
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil || json.Unmarshal(body, &incoming) != nil {
		httpError(w, http.StatusBadRequest, "invalid metadata document")
		return
	}

	if err := s.store.SavePackageMetadata(org, appName, &incoming); err != nil {
		httpError(w, http.StatusInternalServerError, "could not store metadata")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── helpers ─────────────────────────────────────────────────────────────────

// parseArtifactPath splits <org>/<app>/<version>/<app>-<version>.fpm.
func parseArtifactPath(relPath string) (org, appName, version string, err error) {
	parts := strings.Split(relPath, "/")
	if len(parts) != 4 {
		return "", "", "", errors.New(
			"an artifact path must be <org>/<app>/<version>/<app>-<version>.fpm")
	}
	org, appName, version = parts[0], parts[1], parts[2]
	if org == "" || appName == "" || version == "" {
		return "", "", "", errors.New("the artifact path has an empty component")
	}

	expected := fmt.Sprintf("%s-%s.fpm", appName, version)
	if parts[3] != expected {
		return "", "", "", fmt.Errorf("the filename should be %q", expected)
	}
	return org, appName, version, nil
}

// readManifest extracts app_metadata.json from an artifact.
//
// Written to a temporary file so the existing archive readers can be reused.
// Reimplementing the extraction here would risk the server and the client
// disagreeing about what a package contains, which is precisely the class of
// bug this service exists to remove.
func readManifest(artifact []byte) (*metadata.AppMetadata, error) {
	temp, err := os.CreateTemp("", "fpm-upload-*.fpm")
	if err != nil {
		return nil, err
	}
	defer os.Remove(temp.Name())

	if _, err := temp.Write(artifact); err != nil {
		temp.Close()
		return nil, err
	}
	if err := temp.Close(); err != nil {
		return nil, err
	}
	return metadata.ReadMetadataFromFPMArchive(temp.Name())
}

// verifyContentChecksum re-runs the client's own integrity check server-side.
func verifyContentChecksum(artifact []byte, recorded string) error {
	temp, err := os.CreateTemp("", "fpm-verify-*.fpm")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())

	if _, err := temp.Write(artifact); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return archive.VerifyArchiveContentChecksum(temp.Name(), recorded)
}

// basicPassword pulls the password half out of a Basic credential.
func basicPassword(encoded string) string {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return ""
	}
	_, password, found := strings.Cut(string(raw), ":")
	if !found {
		return ""
	}
	return password
}

func httpError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
