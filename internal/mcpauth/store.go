// ABOUTME: Persistent OAuth state for the remote MCP server: approved clients and token hashes.
// ABOUTME: Stores only SHA-256 hashes of secrets, in one owner-only JSON file written atomically.
package mcpauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/fsutil"
)

const (
	tokenKindAccess  = "access"
	tokenKindRefresh = "refresh"
)

type client struct {
	ID           string    `json:"id"`
	Name         string    `json:"name,omitempty"`
	RedirectURIs []string  `json:"redirect_uris"`
	AuthMethod   string    `json:"auth_method"`
	SecretHash   string    `json:"secret_hash,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	// ApprovedAt is set when the owner approves the client. Only approved clients are
	// stored; registered clients wait in memory until then.
	ApprovedAt time.Time `json:"approved_at,omitempty"`
}

type token struct {
	Kind      string    `json:"kind"`
	ClientID  string    `json:"client_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

type state struct {
	// Clients holds the approved clients.
	Clients map[string]*client `json:"clients"`
	// Tokens maps the SHA-256 hash of each token to its record.
	Tokens map[string]*token `json:"tokens"`
}

func newState() *state {
	return &state{Clients: map[string]*client{}, Tokens: map[string]*token{}}
}

func loadState(path string) (*state, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return newState(), nil
	}
	if err != nil {
		return nil, err
	}
	s := newState()
	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("read %s: %w; delete the file to reset all MCP clients", path, err)
	}
	if s.Clients == nil {
		s.Clients = map[string]*client{}
	}
	if s.Tokens == nil {
		s.Tokens = map[string]*token{}
	}
	return s, nil
}

func writeState(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, append(data, '\n'), 0o600)
}

// prune removes expired tokens, and approved clients that have had no token for a day.
func (s *state) prune(now time.Time) {
	used := map[string]bool{}
	for hash, t := range s.Tokens {
		if !now.Before(t.ExpiresAt) {
			delete(s.Tokens, hash)
			continue
		}
		used[t.ClientID] = true
	}
	for id, c := range s.Clients {
		if !used[id] && now.Sub(c.ApprovedAt) > 24*time.Hour {
			delete(s.Clients, id)
		}
	}
}

// randomToken returns 32 random bytes in unpadded base64url form.
func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("mcpauth: no random source: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
