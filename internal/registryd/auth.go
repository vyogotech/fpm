package registryd

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"sync"
)

// Publisher is an authenticated identity and the organisations it may write to.
//
// Org scoping is the point. The WebDAV registry authenticated with a single
// shared htpasswd entry, so anyone who could publish at all could overwrite
// anyone else's packages — including `frappe/erpnext`. In an open ecosystem
// that is not a hardening detail, it is the difference between a registry
// third parties can be invited to and one they cannot.
type Publisher struct {
	Name string   `json:"name"`
	Orgs []string `json:"orgs"`
	// TokenSHA256 is the hex sha256 of the bearer token. The plaintext token is
	// shown to the publisher once, at issue time, and never stored — so a leak
	// of this file does not hand over the ability to publish.
	TokenSHA256 string `json:"token_sha256"`
}

// CanPublishTo reports whether this publisher owns an organisation.
func (p Publisher) CanPublishTo(org string) bool {
	for _, owned := range p.Orgs {
		if strings.EqualFold(owned, org) {
			return true
		}
		if owned == "*" {
			// Reserved for an administrative credential used to bootstrap or
			// repair the registry, never issued to a third party.
			return true
		}
	}
	return false
}

// Authenticator resolves a request credential to a publisher.
type Authenticator interface {
	// Authenticate returns the publisher for a token, or false. It must not
	// distinguish "unknown token" from "malformed token" to the caller: both
	// are 401, and saying which is which tells an attacker whether a token
	// exists.
	Authenticate(token string) (Publisher, bool)
}

// TokenAuthenticator checks bearer tokens against hashed records.
type TokenAuthenticator struct {
	mu         sync.RWMutex
	publishers []Publisher
}

// NewTokenAuthenticator builds an authenticator from publisher records.
func NewTokenAuthenticator(publishers []Publisher) *TokenAuthenticator {
	return &TokenAuthenticator{publishers: publishers}
}

// LoadTokenAuthenticator reads publisher records from a JSON file.
//
// A missing file yields an authenticator with no publishers, which refuses
// everything. That is deliberately the safe direction: a registry that has
// lost its publisher list should stop accepting writes, not start accepting
// all of them.
func LoadTokenAuthenticator(path string) (*TokenAuthenticator, error) {
	if path == "" {
		return NewTokenAuthenticator(nil), nil
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return NewTokenAuthenticator(nil), nil
	}
	if err != nil {
		return nil, err
	}

	var publishers []Publisher
	if err := json.Unmarshal(data, &publishers); err != nil {
		return nil, err
	}
	return NewTokenAuthenticator(publishers), nil
}

// HashToken returns the storable form of a token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Authenticate resolves a bearer token.
func (a *TokenAuthenticator) Authenticate(token string) (Publisher, bool) {
	if token == "" {
		return Publisher{}, false
	}
	hashed := HashToken(token)

	a.mu.RLock()
	defer a.mu.RUnlock()

	for _, publisher := range a.publishers {
		// Constant time: a byte-by-byte comparison that exits early leaks how
		// much of a guessed token was correct.
		if subtle.ConstantTimeCompare([]byte(publisher.TokenSHA256), []byte(hashed)) == 1 {
			return publisher, true
		}
	}
	return Publisher{}, false
}

// Add registers a publisher at runtime, used by tests and by the issue command.
func (a *TokenAuthenticator) Add(publisher Publisher) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.publishers = append(a.publishers, publisher)
}

// credentialFrom extracts a bearer token from a request header value.
//
// Basic is accepted alongside Bearer so that an existing `fpm repo add
// --username` configuration keeps working: the client sends its password as
// the Basic password, and that string is treated as the token. Without this,
// moving a repository onto the service would require every publisher to
// reconfigure at the same moment.
func credentialFrom(header string) string {
	if header == "" {
		return ""
	}
	switch {
	case strings.HasPrefix(header, "Bearer "):
		return strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	case strings.HasPrefix(header, "Basic "):
		return basicPassword(strings.TrimSpace(strings.TrimPrefix(header, "Basic ")))
	default:
		return ""
	}
}
