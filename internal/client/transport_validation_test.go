// ABOUTME: Tests for auth fallback boundaries, nil-safe request inputs, and exact
// ABOUTME: multipart callback extraction.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestAuthenticate_DoesNotSendCredentialsAfterAutologinTransportFailure(t *testing.T) {
	store := &SessionStore{Path: t.TempDir() + "/session.json"}
	if err := store.Save(Session{ID: "old", Username: "user@example.org", Cookies: []*http.Cookie{{Name: "JSESSIONID", Value: "j1"}}}); err != nil {
		t.Fatalf("saving stored session: %v", err)
	}

	autologinErr := errors.New("autologin transport failed")
	loginErr := errors.New("credential login should not run")
	calls := 0
	httpClient := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return nil, autologinErr
		}
		return nil, loginErr
	})}
	c := New(httpClient, "https://office.example.org/ajax", "mbx", "0.1.0", store)

	_, err := c.Authenticate(context.Background(), "user@example.org", "secret")
	if !errors.Is(err, autologinErr) {
		t.Fatalf("Authenticate() error = %v, want original autologin transport error", err)
	}
	if calls != 1 {
		t.Errorf("HTTP requests = %d, want autologin only", calls)
	}
}

func TestAuthenticate_DoesNotSendCredentialsAfterAutologinServerFailure(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "autologin", testutil.Response{Body: map[string]any{
		"error": "service temporarily unavailable", "code": "SVL-0011", "categories": "TRY_AGAIN",
	}})
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "must-not-login"}})

	c := newTestClient(t, srv)
	if err := c.store.Save(Session{ID: "old", Username: "user@example.org", Cookies: []*http.Cookie{{Name: "JSESSIONID", Value: "j1"}}}); err != nil {
		t.Fatalf("saving stored session: %v", err)
	}

	_, err := c.Authenticate(context.Background(), "user@example.org", "secret")
	var cerr *cliErrors.Error
	if !errors.As(err, &cerr) || cerr.Code != "SVL-0011" {
		t.Fatalf("Authenticate() error = %v, want original SVL-0011 autologin failure", err)
	}
	for _, req := range srv.Requests() {
		if req.URL.Query().Get("action") == "login" {
			t.Fatalf("credential login ran after a non-session autologin failure: %s", req.URL)
		}
	}
}

func TestAuthenticate_MissingFallbackPasswordDoesNotAttemptCredentialLogin(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{
		"session": "must-not-login",
	}})

	c := newTestClient(t, srv)
	_, err := c.Authenticate(context.Background(), "user@example.org", "")
	var cerr *cliErrors.Error
	if !errors.As(err, &cerr) || cerr.Code != "auth_failed" || !strings.Contains(cerr.Message, "mbx login") {
		t.Fatalf("Authenticate() error = %v, want auth_failed login guidance", err)
	}
	if requests := srv.Requests(); len(requests) != 0 {
		t.Fatalf("HTTP requests = %d, want no empty credential login", len(requests))
	}
}

func TestAuthenticate_RejectedAutologinWithoutPasswordDoesNotAttemptCredentialLogin(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "autologin", testutil.Response{Body: map[string]any{
		"error": "stored session expired", "code": "SES-0203", "categories": "TRY_AGAIN",
	}})
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{
		"session": "must-not-login",
	}})

	c := newTestClient(t, srv)
	if err := c.store.Save(Session{ID: "old", Username: "user@example.org", Cookies: []*http.Cookie{{Name: "JSESSIONID", Value: "stale"}}}); err != nil {
		t.Fatalf("saving stale session: %v", err)
	}

	_, err := c.Authenticate(context.Background(), "user@example.org", "")
	var cerr *cliErrors.Error
	if !errors.As(err, &cerr) || cerr.Code != "auth_failed" || !strings.Contains(cerr.Message, "mbx login") {
		t.Fatalf("Authenticate() error = %v, want auth_failed login guidance", err)
	}
	requests := srv.Requests()
	if len(requests) != 1 || requests[0].URL.Query().Get("action") != "autologin" {
		t.Fatalf("requests = %+v, want autologin only", requests)
	}
}

func TestDo_SkipsNilSessionCookies(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/user", "get", testutil.Response{Body: map[string]any{"data": map[string]any{}}})
	c := newTestClient(t, srv)

	_, err := c.Do(context.Background(), http.MethodGet, "/user", "get", Session{
		ID: "sess-1",
		Cookies: []*http.Cookie{
			nil,
			{Name: "JSESSIONID", Value: "j1"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	req := srv.Requests()[0]
	cookie, err := req.Cookie("JSESSIONID")
	if err != nil || cookie.Value != "j1" {
		t.Errorf("JSESSIONID cookie = %+v, %v; want j1", cookie, err)
	}
}

func TestMultipartMethods_RejectNilContentReaders(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	c := newTestClient(t, srv)

	tests := map[string]func() error{
		"single file": func() error {
			_, err := c.DoMultipart(context.Background(), "/infostore", "new", Session{ID: "sess-1"}, url.Values{}, map[string]any{}, "file.txt", "text/plain", nil)
			return err
		},
		"callback attachment": func() error {
			_, err := c.DoMultipartCallback(context.Background(), "/mail", "new", Session{ID: "sess-1"}, url.Values{}, map[string]any{}, []Attachment{{
				FieldName: "file_0", Filename: "file.txt", ContentType: "text/plain", Content: nil,
			}})
			return err
		},
	}
	for name, call := range tests {
		t.Run(name, func(t *testing.T) {
			err := call()
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), "nil") {
				t.Fatalf("error = %v, want nil content reader error", err)
			}
		})
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("made %d requests, want validation before network", len(srv.Requests()))
	}
}

func TestExtractMultipartCallbackJSON_IgnoresTrailingHTMLBraces(t *testing.T) {
	body := []byte(`<!doctype html><script>(parent["callback_new"])({"data":{"id":"123"}})</script><style>body{color:red}</style>`)
	got := extractMultipartCallbackJSON(body)
	if !json.Valid(got) {
		t.Fatalf("extractMultipartCallbackJSON() = %q, want valid JSON", got)
	}
	if want := `{"data":{"id":"123"}}`; string(got) != want {
		t.Errorf("extractMultipartCallbackJSON() = %s, want %s", got, want)
	}
}

func TestDoMultipart_RejectsHTMLWithoutCallback(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/infostore", "new", testutil.Response{
		Body: []byte(`<!doctype html><style>body {}</style><h1>upstream error</h1>`),
	})
	c := newTestClient(t, srv)

	_, err := c.DoMultipart(
		context.Background(), "/infostore", "new", Session{ID: "sess-1"}, nil,
		map[string]any{"title": "file.txt"}, "file.txt", "text/plain", strings.NewReader("contents"),
	)
	if err == nil {
		t.Fatal("DoMultipart() error = nil, want HTML without a callback rejected")
	}
	var cerr *cliErrors.Error
	if !errors.As(err, &cerr) || cerr.Code != "invalid_response" {
		t.Fatalf("DoMultipart() error = %v, want structured invalid_response", err)
	}
}

func TestAuthenticate_LoginRateLimitDuringAutologinDoesNotSendCredentials(t *testing.T) {
	for _, code := range []string{"LGI-0026", "LGI-0027"} {
		t.Run(code, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "autologin", testutil.Response{Body: map[string]any{
				"error": "Too many login attempts", "code": code,
			}})
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "must-not-login"}})

			c := newTestClient(t, srv)
			if err := c.store.Save(Session{ID: "old", Username: "user@example.org", Cookies: []*http.Cookie{{Name: "JSESSIONID", Value: "j1"}}}); err != nil {
				t.Fatalf("saving stored session: %v", err)
			}

			_, err := c.Authenticate(context.Background(), "user@example.org", "secret")
			var cerr *cliErrors.Error
			if !errors.As(err, &cerr) || cerr.Code != code || !cerr.Retryable {
				t.Fatalf("Authenticate() error = %v, want retryable %s rate-limit error", err, code)
			}
			for _, req := range srv.Requests() {
				if req.URL.Query().Get("action") == "login" {
					t.Fatalf("credential login ran after an autologin rate limit: %s", req.URL)
				}
			}
		})
	}
}

// mailbox.org answers an expired stored cookie with SVL-0003, not an SES- code.
func TestAuthenticate_InvalidCookieAutologin(t *testing.T) {
	invalidCookie := map[string]any{
		"error": "The provided cookie is invalid. Please login.", "code": "SVL-0003", "categories": "TRY_AGAIN",
	}
	t.Run("falls back to a credential login", func(t *testing.T) {
		srv := testutil.NewServer()
		defer srv.Close()
		srv.Handle("/ajax/login", "autologin", testutil.Response{Body: invalidCookie})
		srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-full"}})
		c := newTestClient(t, srv)
		if err := c.store.Save(Session{ID: "old", Username: "user@example.org", Cookies: []*http.Cookie{{Name: "JSESSIONID", Value: "stale"}}}); err != nil {
			t.Fatalf("saving stale session: %v", err)
		}
		sess, err := c.Authenticate(context.Background(), "user@example.org", "pw")
		if err != nil || sess.ID != "sess-full" {
			t.Fatalf("Authenticate() = %q, %v; want the credential login session", sess.ID, err)
		}
	})
	t.Run("without a password asks for mbx login", func(t *testing.T) {
		srv := testutil.NewServer()
		defer srv.Close()
		srv.Handle("/ajax/login", "autologin", testutil.Response{Body: invalidCookie})
		c := newTestClient(t, srv)
		if err := c.store.Save(Session{ID: "old", Username: "user@example.org", Cookies: []*http.Cookie{{Name: "JSESSIONID", Value: "stale"}}}); err != nil {
			t.Fatalf("saving stale session: %v", err)
		}
		_, err := c.Authenticate(context.Background(), "user@example.org", "")
		var cerr *cliErrors.Error
		if !errors.As(err, &cerr) || cerr.Code != "auth_failed" || !strings.Contains(cerr.Message, "mbx login") {
			t.Fatalf("Authenticate() error = %v, want auth_failed login guidance", err)
		}
	})
}
