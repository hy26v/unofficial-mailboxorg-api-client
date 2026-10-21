// ABOUTME: Local session store: persists the session id and cookies from a mailbox.org
// ABOUTME: login between CLI invocations, in a gitignored local file.
package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/fsutil"
)

const maxSessionSize = 1 << 20

// Session holds a mailbox.org session id and the cookies that must accompany it.
// Username is the normalized account (see NormalizeAccount) whose credential login
// created the session. Authenticate uses it to refuse autologin for another account.
type Session struct {
	ID       string
	Cookies  []*http.Cookie
	Username string
}

// NormalizeAccount returns the comparison form of a mailbox address: surrounding
// whitespace removed and letters lowercased. Mailbox addresses are case-insensitive for
// login, so "User@Example.org" and "user@example.org" identify one account.
func NormalizeAccount(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

// String redacts the session id and cookie values, for safe logging/debug output.
func (s Session) String() string {
	if s.ID == "" {
		return "Session{empty}"
	}
	return "Session{ID: [REDACTED], Cookies: [REDACTED]}"
}

// GoString implements fmt.GoStringer so %#v redacts the session id and cookie values too.
func (s Session) GoString() string {
	return s.String()
}

type persistedCookie struct {
	Name    string     `json:"name"`
	Value   string     `json:"value"`
	Expires *time.Time `json:"expires,omitempty"`
}

type persistedSession struct {
	ID       string            `json:"id"`
	Cookies  []persistedCookie `json:"cookies"`
	Username string            `json:"username,omitempty"`
}

// SessionStore persists a Session to a local file.
type SessionStore struct {
	Path string
}

// cookieExpiredAt applies Set-Cookie precedence: a negative Max-Age deletes a cookie,
// while a positive Max-Age describes a fresh lifetime and takes precedence over Expires.
// Loaded cookies have MaxAge zero and retain their persisted absolute expiry.
func cookieExpiredAt(cookie *http.Cookie, now time.Time) bool {
	if cookie.MaxAge < 0 {
		return true
	}
	if cookie.MaxAge > 0 {
		return false
	}
	return !cookie.Expires.IsZero() && !cookie.Expires.After(now)
}

// Save writes the session to the store's file.
func (s *SessionStore) Save(sess Session) error {
	p := persistedSession{ID: sess.ID, Username: NormalizeAccount(sess.Username)}
	now := time.Now()
	for _, c := range sess.Cookies {
		if c == nil || cookieExpiredAt(c, now) {
			continue
		}
		if err := c.Valid(); err != nil {
			return fmt.Errorf("cannot persist invalid session cookie: %w", err)
		}
		pc := persistedCookie{Name: c.Name, Value: c.Value}
		if c.MaxAge > 0 {
			const maxDuration = time.Duration(1<<63 - 1)
			lifetime := maxDuration
			if int64(c.MaxAge) <= int64(maxDuration/time.Second) {
				lifetime = time.Duration(c.MaxAge) * time.Second
			}
			exp := now.Add(lifetime)
			pc.Expires = &exp
		} else if !c.Expires.IsZero() {
			exp := c.Expires
			pc.Expires = &exp
		}
		p.Cookies = append(p.Cookies, pc)
	}

	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}

	// Write a fresh 0600 file and rename it into place rather than writing through the
	// existing path. os.WriteFile applies its mode only when it creates the file, so
	// rewriting an already-permissive session file would leave the session id and cookies
	// readable by other users; the rename also keeps a concurrent reader from seeing a
	// half-written session.
	return fsutil.WriteFileAtomic(s.Path, data, 0o600)
}

// Clear removes the store's file, so a subsequent Load returns a zero-value Session. A
// missing file is not an error.
func (s *SessionStore) Clear() error {
	info, err := os.Lstat(s.Path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("session path is a directory: %s", s.Path)
	}
	return os.Remove(s.Path)
}

// Load reads the session from the store's file. A missing file yields a zero-value
// Session and no error, so that first-run has a clean fallback to full login.
func (s *SessionStore) Load() (Session, error) {
	file, err := os.Open(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return Session{}, nil
		}
		return Session{}, err
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxSessionSize+1))
	if err != nil {
		return Session{}, err
	}
	if len(data) > maxSessionSize {
		return Session{}, fmt.Errorf("session file exceeds the %d-byte limit: %s", maxSessionSize, s.Path)
	}

	var p *persistedSession
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		return Session{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Session{}, fmt.Errorf("session file contains multiple JSON values: %s", s.Path)
		}
		return Session{}, err
	}
	if p == nil {
		return Session{}, fmt.Errorf("session file contains null instead of an object: %s", s.Path)
	}

	sess := Session{ID: p.ID, Username: NormalizeAccount(p.Username)}
	now := time.Now()
	for _, pc := range p.Cookies {
		c := &http.Cookie{Name: pc.Name, Value: pc.Value}
		if pc.Expires != nil {
			c.Expires = *pc.Expires
		}
		if err := c.Valid(); err != nil {
			return Session{}, fmt.Errorf("session file contains an invalid cookie: %w", err)
		}
		if cookieExpiredAt(c, now) {
			continue
		}
		sess.Cookies = append(sess.Cookies, c)
	}
	return sess, nil
}
