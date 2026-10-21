// ABOUTME: Contract tests for redirect policy, immutable query parameters, timestamp
// ABOUTME: responses, and expired session-cookie handling.
package client

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func redirectRequest(t *testing.T, rawURL string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	return req
}

func cloneRequestValues(values url.Values) url.Values {
	cloned := make(url.Values, len(values))
	for key, entries := range values {
		cloned[key] = append([]string(nil), entries...)
	}
	return cloned
}

func TestNew_PreservesInjectedRedirectPolicyWithoutMutatingCaller(t *testing.T) {
	sentinel := errors.New("caller redirect policy")
	calls := 0
	httpClient := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			calls++
			return sentinel
		},
	}

	c := New(httpClient, "https://office.example.org/ajax", "mbx", "0.1.0", &SessionStore{Path: t.TempDir() + "/session.json"})

	// New must not replace policy on a client the caller may also use elsewhere.
	err := httpClient.CheckRedirect(
		redirectRequest(t, "https://elsewhere.example.org/next"),
		[]*http.Request{redirectRequest(t, "https://office.example.org/start")},
	)
	if !errors.Is(err, sentinel) {
		t.Fatalf("caller's CheckRedirect error = %v, want original sentinel", err)
	}

	// The private client still enforces the caller's policy after its own origin checks.
	err = c.http.CheckRedirect(
		redirectRequest(t, "https://office.example.org/next"),
		[]*http.Request{redirectRequest(t, "https://office.example.org/start")},
	)
	if !errors.Is(err, sentinel) {
		t.Fatalf("wrapped CheckRedirect error = %v, want original sentinel", err)
	}
	if calls != 2 {
		t.Fatalf("caller redirect policy ran %d times, want 2", calls)
	}
}

func TestNew_RetainsDefaultTenRedirectLimit(t *testing.T) {
	c := New(&http.Client{}, "https://office.example.org/ajax", "mbx", "0.1.0", &SessionStore{Path: t.TempDir() + "/session.json"})
	via := make([]*http.Request, 10)
	for i := range via {
		via[i] = redirectRequest(t, "https://office.example.org/previous")
	}

	if err := c.http.CheckRedirect(redirectRequest(t, "https://office.example.org/next"), via[:9]); err != nil {
		t.Fatalf("ninth same-origin redirect was refused: %v", err)
	}
	if err := c.http.CheckRedirect(redirectRequest(t, "https://office.example.org/next"), via); err == nil {
		t.Fatal("tenth redirect was allowed, want the net/http default limit retained")
	}
}

func TestNew_AllowsCaseEquivalentRedirectHost(t *testing.T) {
	c := New(&http.Client{}, "https://office.example.org/ajax", "mbx", "0.1.0", &SessionStore{Path: t.TempDir() + "/session.json"})

	err := c.http.CheckRedirect(
		redirectRequest(t, "https://OFFICE.EXAMPLE.ORG/next"),
		[]*http.Request{redirectRequest(t, "https://office.example.org/start")},
	)
	if err != nil {
		t.Fatalf("case-equivalent same-origin redirect was refused: %v", err)
	}
}

func TestRequestVariants_DoNotMutateCallerQueryParameters(t *testing.T) {
	type requestCase struct {
		action   string
		response any
		call     func(*Client, url.Values) error
	}
	tests := map[string]requestCase{
		"Do": {
			action: "get", response: map[string]any{"data": map[string]any{}},
			call: func(c *Client, q url.Values) error {
				_, err := c.Do(context.Background(), http.MethodGet, "/resource", "get", Session{ID: "sess-1"}, q)
				return err
			},
		},
		"DoWithTimestamp": {
			action: "get", response: map[string]any{"data": map[string]any{}, "timestamp": 1},
			call: func(c *Client, q url.Values) error {
				_, _, err := c.DoWithTimestamp(context.Background(), http.MethodGet, "/resource", "get", Session{ID: "sess-1"}, q)
				return err
			},
		},
		"DoRaw": {
			action: "document", response: []byte("raw"),
			call: func(c *Client, q url.Values) error {
				_, err := c.DoRaw(context.Background(), http.MethodGet, "/resource", "document", Session{ID: "sess-1"}, q)
				return err
			},
		},
		"DoRawBody": {
			action: "new", response: map[string]any{"data": map[string]any{}},
			call: func(c *Client, q url.Values) error {
				_, err := c.DoRawBody(context.Background(), http.MethodPut, "/resource", "new", Session{ID: "sess-1"}, q, "text/plain", []byte("body"))
				return err
			},
		},
		"DoMultipart": {
			action: "new", response: map[string]any{"data": map[string]any{}},
			call: func(c *Client, q url.Values) error {
				_, err := c.DoMultipart(context.Background(), "/resource", "new", Session{ID: "sess-1"}, q, map[string]any{}, "file.txt", "text/plain", strings.NewReader("file"))
				return err
			},
		},
		"DoMultipartCallback": {
			action: "new", response: map[string]any{"data": map[string]any{}},
			call: func(c *Client, q url.Values) error {
				_, err := c.DoMultipartCallback(context.Background(), "/resource", "new", Session{ID: "sess-1"}, q, map[string]any{}, nil)
				return err
			},
		},
		"DoJSON": {
			action: "search", response: map[string]any{"data": []any{}},
			call: func(c *Client, q url.Values) error {
				_, err := c.DoJSON(context.Background(), http.MethodPut, "/resource", "search", Session{ID: "sess-1"}, q, map[string]any{})
				return err
			},
		},
		"DoJSONRaw without action": {
			action: "", response: []any{},
			call: func(c *Client, q url.Values) error {
				_, err := c.DoJSONRaw(context.Background(), http.MethodPut, "/resource", "", Session{ID: "sess-1"}, q, []any{})
				return err
			},
		},
		"DoJSONWithTimestamp": {
			action: "new", response: map[string]any{"data": map[string]any{}, "timestamp": 1},
			call: func(c *Client, q url.Values) error {
				_, _, err := c.DoJSONWithTimestamp(context.Background(), http.MethodPut, "/resource", "new", Session{ID: "sess-1"}, q, map[string]any{})
				return err
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/resource", tt.action, testutil.Response{Body: tt.response})
			c := newTestClient(t, srv)

			params := url.Values{
				"caller":  {"one", "two"},
				"action":  {"caller-action"},
				"session": {"caller-session"},
			}
			want := cloneRequestValues(params)
			if err := tt.call(c, params); err != nil {
				t.Fatalf("%s failed: %v", name, err)
			}
			if !reflect.DeepEqual(params, want) {
				t.Fatalf("caller params mutated to %v, want %v", params, want)
			}

			requests := srv.Requests()
			if len(requests) != 1 {
				t.Fatalf("server saw %d requests, want 1", len(requests))
			}
			if got := requests[0].URL.Query().Get("action"); got != tt.action {
				t.Errorf("request action = %q, want %q", got, tt.action)
			}
		})
	}
}

func TestEnvelopeRequestVariants_AcceptDocumentedEmptySuccessEnvelope(t *testing.T) {
	type requestCase struct {
		action string
		call   func(*Client) error
	}
	tests := map[string]requestCase{
		"Do": {
			action: "get",
			call: func(c *Client) error {
				_, err := c.Do(context.Background(), http.MethodGet, "/resource", "get", Session{ID: "sess-1"}, nil)
				return err
			},
		},
		"DoRawBody": {
			action: "new",
			call: func(c *Client) error {
				_, err := c.DoRawBody(context.Background(), http.MethodPut, "/resource", "new", Session{ID: "sess-1"}, nil, "text/plain", []byte("body"))
				return err
			},
		},
		"DoMultipart": {
			action: "new",
			call: func(c *Client) error {
				_, err := c.DoMultipart(context.Background(), "/resource", "new", Session{ID: "sess-1"}, nil, map[string]any{}, "file.txt", "text/plain", strings.NewReader("file"))
				return err
			},
		},
		"DoMultipartCallback": {
			action: "new",
			call: func(c *Client) error {
				_, err := c.DoMultipartCallback(context.Background(), "/resource", "new", Session{ID: "sess-1"}, nil, map[string]any{}, nil)
				return err
			},
		},
		"DoJSON": {
			action: "search",
			call: func(c *Client) error {
				_, err := c.DoJSON(context.Background(), http.MethodPut, "/resource", "search", Session{ID: "sess-1"}, nil, map[string]any{})
				return err
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/resource", tt.action, testutil.Response{Body: map[string]any{}})

			if err := tt.call(newTestClient(t, srv)); err != nil {
				t.Fatalf("%s rejected an empty success envelope: %v", name, err)
			}
		})
	}
}

func TestTimestampRequestVariants_RejectMissingTimestamp(t *testing.T) {
	tests := map[string]func(*Client) error{
		"DoWithTimestamp": func(c *Client) error {
			_, _, err := c.DoWithTimestamp(context.Background(), http.MethodGet, "/resource", "get", Session{ID: "sess-1"}, nil)
			return err
		},
		"DoJSONWithTimestamp": func(c *Client) error {
			_, _, err := c.DoJSONWithTimestamp(context.Background(), http.MethodPut, "/resource", "get", Session{ID: "sess-1"}, nil, map[string]any{})
			return err
		},
	}

	for name, call := range tests {
		t.Run(name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/resource", "get", testutil.Response{Body: map[string]any{"data": map[string]any{}}})

			err := call(newTestClient(t, srv))
			var cerr *cliErrors.Error
			if !errors.As(err, &cerr) || cerr.Code != "invalid_response" {
				t.Fatalf("%s error = %v, want invalid_response", name, err)
			}
		})
	}
}

func TestMergeCookies_RemovesExpiredAndDeletedCookies(t *testing.T) {
	now := time.Now()
	old := []*http.Cookie{
		{Name: "deleted", Value: "old"},
		{Name: "expired", Value: "old", Expires: now.Add(-time.Minute)},
		{Name: "kept", Value: "old"},
	}
	updated := []*http.Cookie{
		{Name: "deleted", Value: "", MaxAge: -1},
		{Name: "kept", Value: "new"},
	}

	got := mergeCookies(old, updated)
	if len(got) != 1 || got[0].Name != "kept" || got[0].Value != "new" {
		t.Fatalf("mergeCookies() = %+v, want only refreshed kept cookie", got)
	}
}

func TestSessionStore_DropsExpiredCookiesAndPersistsMaxAgeExpiry(t *testing.T) {
	store := &SessionStore{Path: t.TempDir() + "/session.json"}
	before := time.Now()
	err := store.Save(Session{
		ID: "sess-1",
		Cookies: []*http.Cookie{
			{Name: "expired", Value: "old", Expires: before.Add(-time.Minute)},
			{Name: "deleted", Value: "", MaxAge: -1},
			{Name: "bounded", Value: "fresh", MaxAge: 60},
		},
	})
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(got.Cookies) != 1 || got.Cookies[0].Name != "bounded" {
		t.Fatalf("loaded cookies = %+v, want only bounded cookie", got.Cookies)
	}
	if got.Cookies[0].Expires.Before(before.Add(50*time.Second)) ||
		got.Cookies[0].Expires.After(time.Now().Add(70*time.Second)) {
		t.Fatalf("bounded expiry = %v, want roughly one minute from save", got.Cookies[0].Expires)
	}
}

func TestAuthenticate_StoredIDWithoutUsableCookiesSkipsAutologin(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "autologin", testutil.Response{
		Body: map[string]any{"session": "unexpected-auto"},
	})
	srv.Handle("/ajax/login", "login", testutil.Response{
		Body: map[string]any{"session": "credential-session"},
	})

	c := newTestClient(t, srv)
	if err := c.store.Save(Session{
		ID: "expired-session",
		Cookies: []*http.Cookie{{
			Name:    "expired",
			Value:   "old",
			Expires: time.Now().Add(-time.Minute),
		}},
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	sess, err := c.Authenticate(context.Background(), "user@example.org", "password")
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if sess.ID != "credential-session" {
		t.Fatalf("session id = %q, want credential login", sess.ID)
	}

	requests := srv.Requests()
	if len(requests) != 1 || requests[0].URL.Query().Get("action") != "login" {
		t.Fatalf("requests = %+v, want only credential login", requests)
	}
}

func TestAuthenticate_FullLoginDoesNotReturnExpiredCookies(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{
		Body: map[string]any{"session": "credential-session"},
		Cookies: []*http.Cookie{
			{Name: "active", Value: "fresh"},
			{Name: "expired", Value: "stale", Expires: time.Now().Add(-time.Minute)},
			{Name: "deleted", Value: "", MaxAge: -1},
		},
	})

	c := newTestClient(t, srv)
	sess, err := c.Authenticate(context.Background(), "user@example.org", "password")
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if len(sess.Cookies) != 1 || sess.Cookies[0].Name != "active" {
		t.Fatalf("returned cookies = %+v, want only the active cookie", sess.Cookies)
	}
}
