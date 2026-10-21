// ABOUTME: Tests for credential/cookie handling across redirects and context cancellation.
package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestLogin_DoesNotFollowCrossOriginRedirect ensures that if a login endpoint responds
// with a redirect to a different origin, the client does not follow it and thus never
// resends credentials/cookies to an untrusted host.
func TestLogin_DoesNotFollowCrossOriginRedirect(t *testing.T) {
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("credentials must not be forwarded to untrusted host, but got request: %s", r.URL)
		w.WriteHeader(http.StatusOK)
	}))
	defer evil.Close()

	main := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL+"/steal", http.StatusFound)
	}))
	defer main.Close()

	c := New(main.Client(), main.URL+"/ajax", "mbx", "0.1.0", &SessionStore{Path: t.TempDir() + "/session.json"})
	_, err := c.Login(context.Background(), "user", "pw")
	if err == nil {
		t.Fatal("expected an error when login redirects cross-origin, got nil")
	}
}

func TestLogin_RespectsContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Write([]byte(`{"session":"x"}`))
	}))
	defer srv.Close()

	c := New(srv.Client(), srv.URL+"/ajax", "mbx", "0.1.0", &SessionStore{Path: t.TempDir() + "/session.json"})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := c.Login(ctx, "user", "pw")
	if err == nil {
		t.Fatal("expected context deadline exceeded error, got nil")
	}
}

// TestRedirect_RefusesToLeaveHTTPS is the regression test for a same-host transport
// downgrade. The redirect guard compared only the host, so an https response redirecting to
// http on the same host was followed. Go forwards the Cookie header on a same-host redirect,
// and the session id travels in the query string, so following such a redirect puts both on
// the wire in cleartext. A host and its default ports make this reachable without any port
// change: https://host and http://host share a URL host.
func TestRedirect_RefusesToLeaveHTTPS(t *testing.T) {
	httpClient := &http.Client{}
	c := New(httpClient, "https://office.example.org/ajax", "mbx", "0.1.0", &SessionStore{Path: t.TempDir() + "/session.json"})

	request := func(t *testing.T, raw string) *http.Request {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, raw, nil)
		if err != nil {
			t.Fatalf("building request %s: %v", raw, err)
		}
		return req
	}

	for _, tc := range []struct {
		name    string
		from    string
		to      string
		wantErr bool
	}{
		{
			name: "https to http on the same host",
			from: "https://office.example.org/ajax/login?action=login",
			to:   "http://office.example.org/ajax/login?action=login",
			// The session cookie and session id would be sent in cleartext.
			wantErr: true,
		},
		{
			name:    "https to another path on the same host",
			from:    "https://office.example.org/ajax/login?action=login",
			to:      "https://office.example.org/ajax/login2",
			wantErr: false,
		},
		{
			name:    "http upgrading to https",
			from:    "http://office.example.org/ajax/login?action=login",
			to:      "https://office.example.org/ajax/login",
			wantErr: false,
		},
		{
			name:    "https to a different host",
			from:    "https://office.example.org/ajax/login?action=login",
			to:      "https://evil.example/steal",
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			via := []*http.Request{request(t, tc.from)}
			err := c.http.CheckRedirect(request(t, tc.to), via)
			if tc.wantErr && err == nil {
				t.Fatalf("redirect %s -> %s was allowed, want it refused", tc.from, tc.to)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("redirect %s -> %s was refused: %v", tc.from, tc.to, err)
			}
		})
	}
}

// TestLogin_FollowsSameOriginRedirect pins that the tightened guard still lets an ordinary
// same-origin redirect through, so the check rejects downgrades rather than redirects.
func TestLogin_FollowsSameOriginRedirect(t *testing.T) {
	var served int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served++
		if r.URL.Path == "/ajax/login" {
			http.Redirect(w, r, "/ajax/login-moved", http.StatusFound)
			return
		}
		w.Write([]byte(`{"session":"sess-1"}`))
	}))
	defer srv.Close()

	c := New(srv.Client(), srv.URL+"/ajax", "mbx", "0.1.0", &SessionStore{Path: t.TempDir() + "/session.json"})
	sess, err := c.Login(context.Background(), "user", "pw")
	if err != nil {
		t.Fatalf("same-origin redirect was not followed: %v", err)
	}
	if sess.ID != "sess-1" {
		t.Errorf("session id = %q, want sess-1", sess.ID)
	}
	if served != 2 {
		t.Errorf("server saw %d requests, want 2 (original plus redirect)", served)
	}
}
