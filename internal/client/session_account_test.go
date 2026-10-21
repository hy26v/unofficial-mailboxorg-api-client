// ABOUTME: Tests that a stored session is bound to the account that created it.
// ABOUTME: Covers login/autologin recording and Authenticate refusing cross-account reuse.
package client

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func newAccountTestServer(t *testing.T) *testutil.Server {
	t.Helper()
	srv := testutil.NewServer()
	t.Cleanup(srv.Close)
	srv.Handle("/ajax/login", "autologin", testutil.Response{Body: map[string]any{"session": "sess-auto"}})
	srv.Handle("/ajax/login", "login", testutil.Response{
		Body:    map[string]any{"session": "sess-full"},
		Cookies: []*http.Cookie{{Name: "JSESSIONID", Value: "fresh"}},
	})
	return srv
}

func requestActions(srv *testutil.Server) []string {
	var actions []string
	for _, req := range srv.Requests() {
		actions = append(actions, req.URL.Query().Get("action"))
	}
	return actions
}

func TestLogin_RecordsNormalizedAccountAndAutologinKeepsIt(t *testing.T) {
	srv := newAccountTestServer(t)
	c := newTestClient(t, srv)

	sess, err := c.Login(context.Background(), "  User@Example.ORG ", "pw")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if sess.Username != "user@example.org" {
		t.Fatalf("login session account = %q, want normalized user@example.org", sess.Username)
	}
	if err := c.store.Save(sess); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, err := c.store.Load()
	if err != nil || loaded.Username != "user@example.org" {
		t.Fatalf("loaded account = %q, %v; want user@example.org", loaded.Username, err)
	}

	refreshed, err := c.Autologin(context.Background())
	if err != nil {
		t.Fatalf("Autologin() error = %v", err)
	}
	if refreshed.Username != "user@example.org" {
		t.Fatalf("autologin session account = %q, want the stored account", refreshed.Username)
	}
}

func TestAuthenticate_SameAccountDifferentCaseUsesAutologin(t *testing.T) {
	srv := newAccountTestServer(t)
	c := newTestClient(t, srv)
	if err := c.store.Save(Session{ID: "old", Username: "user@example.org", Cookies: []*http.Cookie{{Name: "JSESSIONID", Value: "j1"}}}); err != nil {
		t.Fatal(err)
	}

	sess, err := c.Authenticate(context.Background(), " USER@example.org", "")
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if sess.ID != "sess-auto" || sess.Username != "user@example.org" {
		t.Fatalf("session = %q/%q, want autologin for the same account", sess.ID, sess.Username)
	}
}

func TestAuthenticate_OtherAccountSessionIsReplacedByCredentialLogin(t *testing.T) {
	srv := newAccountTestServer(t)
	c := newTestClient(t, srv)
	if err := c.store.Save(Session{ID: "old", Username: "first@example.org", Cookies: []*http.Cookie{{Name: "JSESSIONID", Value: "first-account"}}}); err != nil {
		t.Fatal(err)
	}

	sess, err := c.Authenticate(context.Background(), "second@example.org", "pw")
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if got := requestActions(srv); len(got) != 1 || got[0] != "login" {
		t.Fatalf("request actions = %v, want only a credential login (no autologin with another account's cookies)", got)
	}
	for _, cookie := range srv.Requests()[0].Cookies() {
		if cookie.Value == "first-account" {
			t.Fatalf("credential login sent the other account's cookie %q", cookie.Name)
		}
	}
	if sess.ID != "sess-full" || sess.Username != "second@example.org" {
		t.Fatalf("session = %q/%q, want credential session for second@example.org", sess.ID, sess.Username)
	}
	persisted, err := c.store.Load()
	if err != nil || persisted.Username != "second@example.org" || persisted.ID != "sess-full" {
		t.Fatalf("persisted session = %q/%q, %v; want replacement for second@example.org", persisted.ID, persisted.Username, err)
	}
}

func TestAuthenticate_OtherAccountSessionWithoutPasswordFailsWithLoginGuidance(t *testing.T) {
	srv := newAccountTestServer(t)
	c := newTestClient(t, srv)
	if err := c.store.Save(Session{ID: "old", Username: "first@example.org", Cookies: []*http.Cookie{{Name: "JSESSIONID", Value: "j1"}}}); err != nil {
		t.Fatal(err)
	}

	_, err := c.Authenticate(context.Background(), "second@example.org", "")
	var cerr *cliErrors.Error
	if !errors.As(err, &cerr) || cerr.Code != "auth_failed" ||
		!strings.Contains(cerr.Message, "different account") || !strings.Contains(cerr.Message, "mbx login") {
		t.Fatalf("Authenticate() error = %v, want auth_failed naming the account mismatch and mbx login", err)
	}
	if strings.Contains(cerr.Message, "first@example.org") {
		t.Fatalf("error message discloses the stored account: %q", cerr.Message)
	}
	if got := srv.Requests(); len(got) != 0 {
		t.Fatalf("HTTP requests = %d, want none", len(got))
	}
}

func TestAuthenticate_LegacySessionWithoutAccountIsNotUsedForAutologin(t *testing.T) {
	srv := newAccountTestServer(t)
	c := newTestClient(t, srv)
	legacy := `{"id":"old","cookies":[{"name":"JSESSIONID","value":"legacy"}]}`
	if err := os.WriteFile(c.store.Path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := c.Authenticate(context.Background(), "user@example.org", "")
	var cerr *cliErrors.Error
	if !errors.As(err, &cerr) || cerr.Code != "auth_failed" || !strings.Contains(cerr.Message, "mbx login") {
		t.Fatalf("Authenticate() error = %v, want auth_failed login guidance for a legacy session", err)
	}
	if got := srv.Requests(); len(got) != 0 {
		t.Fatalf("HTTP requests = %d, want none", len(got))
	}

	sess, err := c.Authenticate(context.Background(), "user@example.org", "pw")
	if err != nil {
		t.Fatalf("Authenticate() with password error = %v", err)
	}
	if got := requestActions(srv); len(got) != 1 || got[0] != "login" {
		t.Fatalf("request actions = %v, want a credential login instead of legacy autologin", got)
	}
	if sess.Username != "user@example.org" {
		t.Fatalf("session account = %q, want user@example.org", sess.Username)
	}
}
