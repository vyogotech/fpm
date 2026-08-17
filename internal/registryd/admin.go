package registryd

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// Publisher onboarding.
//
// Tokens can be issued from the command line, which is right for an operator
// bootstrapping a registry. It is not right for an open ecosystem: a developer
// who has just claimed an organisation should not wait on someone running a
// binary.
//
// So issuance is also an API — but a deliberately narrow one. It is reachable
// only with an administrative credential, and the caller is expected to be
// Cloud Manager, which owns organisation claims. Ownership lives there because
// that is where accounts, tenants and the moderation queue already live; this
// service only needs to know which orgs a token may write to, not who owns
// what or why.

// TokenIssuer creates publisher tokens.
type TokenIssuer interface {
	Issue(name string, orgs []string) (token string, err error)
}

// Issue registers a new publisher and returns its plaintext token.
//
// The plaintext is returned exactly once and never stored — only its hash is
// kept, so losing the publisher file does not leak the ability to publish.
func (a *TokenAuthenticator) Issue(name string, orgs []string) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := "fpm_" + hex.EncodeToString(buf)

	a.Add(Publisher{
		Name:        name,
		Orgs:        orgs,
		TokenSHA256: HashToken(token),
	})
	return token, nil
}

// Publishers returns a copy of the current records, for persistence.
func (a *TokenAuthenticator) Publishers() []Publisher {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]Publisher(nil), a.publishers...)
}

type issueRequest struct {
	Name string   `json:"name"`
	Orgs []string `json:"orgs"`
}

type issueResponse struct {
	Name  string   `json:"name"`
	Orgs  []string `json:"orgs"`
	Token string   `json:"token"`
}

// handleIssueToken serves POST /admin/publishers.
//
// Guarded by the wildcard organisation rather than a separate role: a
// credential that may already write to every org is the only one for which
// minting another publisher is not an escalation. A publisher scoped to one
// org must not be able to mint itself a token for another.
func (s *Server) handleIssueToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	publisher, ok := s.auth.Authenticate(credentialFrom(r.Header.Get("Authorization")))
	if !ok {
		w.Header().Set("WWW-Authenticate", `Bearer realm="FPM Registry"`)
		httpError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if !publisher.CanPublishTo("*") {
		httpError(w, http.StatusForbidden, "an administrative credential is required")
		return
	}

	issuer, ok := s.auth.(TokenIssuer)
	if !ok {
		httpError(w, http.StatusNotImplemented, "this registry cannot issue tokens")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		httpError(w, http.StatusBadRequest, "could not read the request body")
		return
	}

	var request issueRequest
	if err := json.Unmarshal(body, &request); err != nil {
		httpError(w, http.StatusBadRequest, "invalid request document")
		return
	}

	request.Name = strings.TrimSpace(request.Name)
	if request.Name == "" || len(request.Orgs) == 0 {
		httpError(w, http.StatusBadRequest, "a name and at least one organisation are required")
		return
	}
	for _, org := range request.Orgs {
		// A token scoped to every org is an administrative credential. Minting
		// one through this endpoint would let any admin caller quietly widen
		// the blast radius; those are issued from the command line only.
		if strings.TrimSpace(org) == "" || org == "*" {
			httpError(w, http.StatusBadRequest, "invalid organisation")
			return
		}
	}

	token, err := issuer.Issue(request.Name, request.Orgs)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "could not issue a token")
		return
	}

	if s.onPublishersChanged != nil {
		// Persisted by the caller, so a restart does not forget the publisher.
		if err := s.onPublishersChanged(); err != nil {
			httpError(w, http.StatusInternalServerError, "could not persist the publisher")
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(issueResponse{
		Name:  request.Name,
		Orgs:  request.Orgs,
		Token: token,
	})
}
