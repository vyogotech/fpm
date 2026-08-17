package registryd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

const adminToken = "tok_admin_root"

func newServerWithAdmin(t *testing.T) *httptest.Server {
	t.Helper()

	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("creating the store: %v", err)
	}

	auth := NewTokenAuthenticator([]Publisher{
		{Name: "acme", Orgs: []string{"acme"}, TokenSHA256: HashToken(publisherToken)},
		// "*" is the administrative credential: the only identity for which
		// minting another publisher is not an escalation.
		{Name: "root", Orgs: []string{"*"}, TokenSHA256: HashToken(adminToken)},
	})

	server := httptest.NewServer(NewServer(store, auth).Handler())
	t.Cleanup(server.Close)
	return server
}

func postJSON(t *testing.T, server *httptest.Server, path, token string, body any) *http.Response {
	t.Helper()

	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encoding the request: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, server.URL+path, bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("issuing the request: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestAdminCanIssueAPublisherToken(t *testing.T) {
	server := newServerWithAdmin(t)

	resp := postJSON(t, server, "/admin/publishers", adminToken,
		map[string]any{"name": "newco", "orgs": []string{"newco"}})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("got status %d, want 201", resp.StatusCode)
	}

	issued := decode[issueResponse](t, resp)
	if issued.Token == "" {
		t.Fatal("no token was returned")
	}

	// The issued token must actually work, and only for its own org.
	artifact := buildArtifact(t, "newco", "widget", "1.0.0", nil)
	if got := put(t, server, "/"+ArtifactPath("newco", "widget", "1.0.0"), issued.Token, artifact); got.StatusCode != http.StatusCreated {
		t.Errorf("publishing with the issued token: got %d, want 201", got.StatusCode)
	}

	other := buildArtifact(t, "acme", "widget", "1.0.0", nil)
	if got := put(t, server, "/"+ArtifactPath("acme", "widget", "1.0.0"), issued.Token, other); got.StatusCode != http.StatusForbidden {
		t.Errorf("cross-org publish with the issued token: got %d, want 403", got.StatusCode)
	}
}

func TestIssuingRequiresAnAdministrativeCredential(t *testing.T) {
	server := newServerWithAdmin(t)

	// A scoped publisher must not be able to widen its own reach by minting a
	// second token for an org it does not own.
	resp := postJSON(t, server, "/admin/publishers", publisherToken,
		map[string]any{"name": "escalation", "orgs": []string{"frappe"}})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("got status %d, want 403", resp.StatusCode)
	}
}

func TestIssuingRefusesAnonymousCallers(t *testing.T) {
	server := newServerWithAdmin(t)

	resp := postJSON(t, server, "/admin/publishers", "",
		map[string]any{"name": "anon", "orgs": []string{"anon"}})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("got status %d, want 401", resp.StatusCode)
	}
}

func TestAdminCannotMintAnotherWildcardCredential(t *testing.T) {
	// Administrative credentials are issued from the command line only.
	// Allowing them here would let one compromised admin token silently
	// manufacture more of itself.
	server := newServerWithAdmin(t)

	resp := postJSON(t, server, "/admin/publishers", adminToken,
		map[string]any{"name": "sneaky", "orgs": []string{"*"}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", resp.StatusCode)
	}
}

func TestIssuingValidatesItsInput(t *testing.T) {
	server := newServerWithAdmin(t)

	for _, body := range []map[string]any{
		{"name": "", "orgs": []string{"newco"}},
		{"name": "newco", "orgs": []string{}},
		{"name": "newco", "orgs": []string{"  "}},
	} {
		resp := postJSON(t, server, "/admin/publishers", adminToken, body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %v: got status %d, want 400", body, resp.StatusCode)
		}
	}
}

func TestIssuedPublishersArePersisted(t *testing.T) {
	// A token handed to a publisher that a restart forgets is worse than no
	// token at all: it fails later, in their pipeline, for no visible reason.
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("creating the store: %v", err)
	}

	auth := NewTokenAuthenticator([]Publisher{
		{Name: "root", Orgs: []string{"*"}, TokenSHA256: HashToken(adminToken)},
	})

	saved := 0
	handler := NewServer(store, auth)
	handler.OnPublishersChanged(func() error {
		saved++
		return nil
	})

	server := httptest.NewServer(handler.Handler())
	t.Cleanup(server.Close)

	postJSON(t, server, "/admin/publishers", adminToken,
		map[string]any{"name": "newco", "orgs": []string{"newco"}})

	if saved != 1 {
		t.Errorf("persistence callback ran %d times, want 1", saved)
	}
}
