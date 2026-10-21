// ABOUTME: Tests for the OX middleware client: full login, autologin, authenticate
// ABOUTME: (autologin-first with fallback), authenticated requests, and error mapping.
package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func newTestClient(t *testing.T, srv *testutil.Server) *Client {
	t.Helper()
	return New(srv.Client(), srv.URL+"/ajax", "mbx", "0.1.0", &SessionStore{Path: t.TempDir() + "/session.json"})
}

func TestLogin_Success_ParsesSessionAndCookies(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	srv.Handle("/ajax/login", "login", testutil.Response{
		Body: map[string]any{
			"session":    "sess-1",
			"user":       "fixture-user@example.invalid",
			"user_id":    42,
			"context_id": 1,
			"locale":     "en_US",
		},
		Cookies: []*http.Cookie{
			{Name: "JSESSIONID", Value: "j1"},
			{Name: "open-xchange-secret-1", Value: "s1"},
		},
	})

	c := newTestClient(t, srv)
	sess, err := c.Login(context.Background(), "fixture-user@example.invalid", "pw")
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}
	if sess.ID != "sess-1" {
		t.Errorf("session id = %q, want sess-1", sess.ID)
	}
	if len(sess.Cookies) < 2 {
		t.Errorf("cookies = %+v, want at least 2", sess.Cookies)
	}

	reqs := srv.Requests()
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	req := reqs[0]
	if req.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", req.Method)
	}
	req.ParseForm()
	if req.PostForm.Get("name") != "fixture-user@example.invalid" {
		t.Errorf("form name = %q", req.PostForm.Get("name"))
	}
	if req.PostForm.Get("password") != "pw" {
		t.Errorf("form password = %q", req.PostForm.Get("password"))
	}
	if req.PostForm.Get("client") != "mbx" {
		t.Errorf("form client = %q", req.PostForm.Get("client"))
	}
	if req.PostForm.Get("staySignedIn") != "true" {
		t.Errorf("form staySignedIn = %q, want true", req.PostForm.Get("staySignedIn"))
	}
}

func TestLogin_BadCredentials_ReturnsStructuredError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	srv.Handle("/ajax/login", "login", testutil.Response{
		Body: map[string]any{
			"error":        "The user name or password is incorrect.",
			"error_params": []string{},
			"categories":   "USER_INPUT",
			"category":     1,
			"code":         "LGI-0006",
			"error_id":     "req-1",
			"error_desc":   "Invalid credentials.",
		},
	})

	c := newTestClient(t, srv)
	_, err := c.Login(context.Background(), "bad", "creds")
	if err == nil {
		t.Fatal("expected error for bad credentials")
	}
	if !strings.Contains(err.Error(), "incorrect") {
		t.Errorf("err = %v, want mention of incorrect credentials", err)
	}
}

func TestAutologin_UsesStoredCookiesNoCredentials(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	srv.Handle("/ajax/login", "autologin", testutil.Response{
		Body: map[string]any{"session": "sess-2", "user": "fixture-user@example.invalid"},
	})

	c := newTestClient(t, srv)
	c.store.Save(Session{ID: "old", Cookies: []*http.Cookie{{Name: "JSESSIONID", Value: "j1"}}})

	sess, err := c.Autologin(context.Background())
	if err != nil {
		t.Fatalf("Autologin failed: %v", err)
	}
	if sess.ID != "sess-2" {
		t.Errorf("session id = %q, want sess-2", sess.ID)
	}

	reqs := srv.Requests()
	req := reqs[0]
	if req.Method != http.MethodGet {
		t.Errorf("method = %s, want GET", req.Method)
	}
	var found bool
	for _, ck := range req.Cookies() {
		if ck.Name == "JSESSIONID" && ck.Value == "j1" {
			found = true
		}
	}
	if !found {
		t.Errorf("stored cookie not sent on autologin request, got %v", req.Cookies())
	}
}

func TestAutologin_ReturnsUnchangedStoredCookies(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	srv.Handle("/ajax/login", "autologin", testutil.Response{
		Body: map[string]any{"session": "sess-2"},
		Cookies: []*http.Cookie{
			{Name: "open-xchange-public-session-1", Value: "refreshed"},
		},
	})

	c := newTestClient(t, srv)
	if err := c.store.Save(Session{Cookies: []*http.Cookie{
		{Name: "JSESSIONID", Value: "j1"},
		{Name: "open-xchange-public-session-1", Value: "stale"},
	}}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	sess, err := c.Autologin(context.Background())
	if err != nil {
		t.Fatalf("Autologin() error = %v", err)
	}
	byName := make(map[string]string)
	for _, cookie := range sess.Cookies {
		byName[cookie.Name] = cookie.Value
	}
	if byName["JSESSIONID"] != "j1" || byName["open-xchange-public-session-1"] != "refreshed" {
		t.Fatalf("returned cookies = %v, want unchanged JSESSIONID and refreshed public session", byName)
	}
}

func TestAuthenticate_AutologinFirstWhenSessionStored(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	srv.Handle("/ajax/login", "autologin", testutil.Response{
		Body: map[string]any{"session": "sess-auto"},
	})
	srv.Handle("/ajax/login", "login", testutil.Response{
		Body: map[string]any{"session": "sess-full"},
	})

	c := newTestClient(t, srv)
	c.store.Save(Session{ID: "old", Username: "fixture-user@example.invalid", Cookies: []*http.Cookie{{Name: "JSESSIONID", Value: "j1"}}})

	// A stored session must be sufficient: normal commands do not retain or re-request the
	// password supplied to the earlier explicit login.
	sess, err := c.Authenticate(context.Background(), "fixture-user@example.invalid", "")
	if err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}
	if sess.ID != "sess-auto" {
		t.Errorf("session id = %q, want sess-auto (autologin should be tried first)", sess.ID)
	}
}

func TestAuthenticate_FallsBackToFullLoginWhenAutologinFails(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	srv.Handle("/ajax/login", "autologin", testutil.Response{
		Body: map[string]any{
			"error": "no session", "code": "SES-0203", "categories": "TRY_AGAIN",
		},
	})
	srv.Handle("/ajax/login", "login", testutil.Response{
		Body: map[string]any{"session": "sess-full"},
	})

	c := newTestClient(t, srv)
	c.store.Save(Session{ID: "old", Username: "fixture-user@example.invalid", Cookies: []*http.Cookie{{Name: "JSESSIONID", Value: "j1"}}})

	sess, err := c.Authenticate(context.Background(), "fixture-user@example.invalid", "pw")
	if err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}
	if sess.ID != "sess-full" {
		t.Errorf("session id = %q, want sess-full (fallback)", sess.ID)
	}
}

func TestAuthenticate_FallsBackToFullLoginAfterUnauthorizedAutologin(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	srv.Handle("/ajax/login", "autologin", testutil.Response{
		Status: http.StatusUnauthorized,
		Body:   []byte("stored session expired"),
	})
	srv.Handle("/ajax/login", "login", testutil.Response{
		Body: map[string]any{"session": "sess-full"},
	})

	c := newTestClient(t, srv)
	if err := c.store.Save(Session{ID: "old", Username: "fixture-user@example.invalid", Cookies: []*http.Cookie{{Name: "JSESSIONID", Value: "stale"}}}); err != nil {
		t.Fatalf("saving stale session: %v", err)
	}

	sess, err := c.Authenticate(context.Background(), "fixture-user@example.invalid", "pw")
	if err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}
	if sess.ID != "sess-full" {
		t.Fatalf("session id = %q, want credential-login session", sess.ID)
	}

	requests := srv.Requests()
	if len(requests) != 2 {
		t.Fatalf("request count = %d, want autologin then login", len(requests))
	}
	if first, second := requests[0].URL.Query().Get("action"), requests[1].URL.Query().Get("action"); first != "autologin" || second != "login" {
		t.Fatalf("request actions = %q, %q; want autologin, login", first, second)
	}
}

func TestAuthenticate_AutologinMergesNewCookiesIntoStoredSet(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	srv.Handle("/ajax/login", "autologin", testutil.Response{
		Body: map[string]any{"session": "sess-auto"},
		Cookies: []*http.Cookie{
			{Name: "open-xchange-public-session-1", Value: "refreshed"},
		},
	})

	c := newTestClient(t, srv)
	c.store.Save(Session{
		ID:       "old",
		Username: "fixture-user@example.invalid",
		Cookies: []*http.Cookie{
			{Name: "JSESSIONID", Value: "j1"},
			{Name: "open-xchange-secret-1", Value: "s1"},
			{Name: "open-xchange-public-session-1", Value: "stale"},
			{Name: "open-xchange-session-1", Value: "sess1"},
			{Name: "open-xchange-shard", Value: "shard1"},
		},
	})

	sess, err := c.Authenticate(context.Background(), "fixture-user@example.invalid", "pw")
	if err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}
	if sess.ID != "sess-auto" {
		t.Errorf("session id = %q, want sess-auto", sess.ID)
	}

	byName := make(map[string]string)
	for _, ck := range sess.Cookies {
		byName[ck.Name] = ck.Value
	}
	want := map[string]string{
		"JSESSIONID":                    "j1",
		"open-xchange-secret-1":         "s1",
		"open-xchange-public-session-1": "refreshed",
		"open-xchange-session-1":        "sess1",
		"open-xchange-shard":            "shard1",
	}
	for name, wantVal := range want {
		if got := byName[name]; got != wantVal {
			t.Errorf("cookie %q = %q, want %q", name, got, wantVal)
		}
	}

	persisted, err := c.store.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if len(persisted.Cookies) != 5 {
		t.Errorf("persisted cookies = %+v, want 5 (merged, not replaced)", persisted.Cookies)
	}
}

func TestAuthenticate_NoStoredSessionGoesStraightToFullLogin(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	var autologinCalled bool
	srv.Handle("/ajax/login", "login", testutil.Response{
		Body: map[string]any{"session": "sess-full"},
	})

	c := newTestClient(t, srv)
	sess, err := c.Authenticate(context.Background(), "fixture-user@example.invalid", "pw")
	if err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}
	if sess.ID != "sess-full" {
		t.Errorf("session id = %q, want sess-full", sess.ID)
	}
	for _, r := range srv.Requests() {
		if r.URL.Query().Get("action") == "autologin" {
			autologinCalled = true
		}
	}
	if autologinCalled {
		t.Error("autologin should not be called when no session is stored")
	}
}

func TestDo_AuthenticatedRequestIncludesSessionParam(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	srv.Handle("/ajax/user", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{"email1": "fixture-user@example.invalid"}},
	})

	c := newTestClient(t, srv)
	data, err := c.Do(context.Background(), http.MethodGet, "/user", "get", Session{ID: "sess-9"}, url.Values{})
	if err != nil {
		t.Fatalf("Do failed: %v", err)
	}
	if !strings.Contains(string(data), "fixture-user@example.invalid") {
		t.Errorf("data = %s", data)
	}

	req := srv.Requests()[0]
	if req.URL.Query().Get("session") != "sess-9" {
		t.Errorf("session query param = %q, want sess-9", req.URL.Query().Get("session"))
	}
}

func TestDoWithTimestamp_ParsesDataAndTimestamp(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	srv.Handle("/ajax/user", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{"email1": "fixture-user@example.invalid"}, "timestamp": 1234567890},
	})

	c := newTestClient(t, srv)
	data, timestamp, err := c.DoWithTimestamp(context.Background(), http.MethodGet, "/user", "get", Session{ID: "sess-9"}, url.Values{})
	if err != nil {
		t.Fatalf("DoWithTimestamp failed: %v", err)
	}
	if !strings.Contains(string(data), "fixture-user@example.invalid") {
		t.Errorf("data = %s", data)
	}
	if timestamp != 1234567890 {
		t.Errorf("timestamp = %d, want 1234567890", timestamp)
	}
}

func TestDoWithTimestamp_ErrorEnvelopeReturnsStructuredError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	srv.Handle("/ajax/user", "get", testutil.Response{
		Body: map[string]any{"error": "boom", "code": "SVL-0001", "categories": "USER_INPUT"},
	})

	c := newTestClient(t, srv)
	_, _, err := c.DoWithTimestamp(context.Background(), http.MethodGet, "/user", "get", Session{ID: "sess-9"}, url.Values{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDoJSON_SendsRequestBodyAsJSON(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	srv.Handle("/ajax/resource", "search", testutil.Response{
		Body: map[string]any{"data": []any{}},
	})

	c := newTestClient(t, srv)
	data, err := c.DoJSON(context.Background(), http.MethodPut, "/resource", "search", Session{ID: "sess-9"}, url.Values{}, map[string]any{"pattern": "*"})
	if err != nil {
		t.Fatalf("DoJSON failed: %v", err)
	}
	if string(data) != "[]" {
		t.Errorf("data = %s, want []", data)
	}

	req := srv.Requests()[0]
	if req.Header.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", req.Header.Get("Content-Type"))
	}
	body, _ := io.ReadAll(req.Body)
	if !strings.Contains(string(body), `"pattern":"*"`) {
		t.Errorf("body = %s, want to contain pattern", body)
	}
}

func TestDoJSONRaw_SendsBodyAndReturnsRawResponseWithoutUnwrapping(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	srv.Handle("/ajax/multiple", "", testutil.Response{
		Body: []any{map[string]any{"data": map[string]any{"quota": 1}}, map[string]any{"data": map[string]any{"user_id": 42}}},
	})

	c := newTestClient(t, srv)
	data, err := c.DoJSONRaw(context.Background(), http.MethodPut, "/multiple", "", Session{ID: "sess-9"}, nil, []map[string]any{
		{"module": "quota", "action": "filestore"},
		{"module": "user", "action": "get"},
	})
	if err != nil {
		t.Fatalf("DoJSONRaw failed: %v", err)
	}
	if !strings.Contains(string(data), `"quota":1`) || !strings.Contains(string(data), `"user_id":42`) {
		t.Errorf("data = %s, want raw array containing both sub-responses", data)
	}

	req := srv.Requests()[0]
	if req.Header.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", req.Header.Get("Content-Type"))
	}
	if req.URL.Query().Get("action") != "" {
		t.Errorf("action query = %q, want empty (multiple takes no action param)", req.URL.Query().Get("action"))
	}
	body, _ := io.ReadAll(req.Body)
	if !strings.Contains(string(body), `"module":"quota"`) {
		t.Errorf("body = %s, want to contain module", body)
	}
}

func TestDoJSONRaw_ErrorEnvelopeReturnsStructuredError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	srv.Handle("/ajax/multiple", "", testutil.Response{
		Body: map[string]any{"error": "boom", "code": "SVL-0001", "categories": "USER_INPUT"},
	})

	c := newTestClient(t, srv)
	_, err := c.DoJSONRaw(context.Background(), http.MethodPut, "/multiple", "", Session{ID: "sess-9"}, nil, []map[string]any{{"module": "quota", "action": "filestore"}})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDoJSONWithTimestamp_ParsesDataAndTimestamp(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	srv.Handle("/ajax/tasks", "new", testutil.Response{
		Body: map[string]any{"data": map[string]any{"id": 4}, "timestamp": 1784855706453},
	})

	c := newTestClient(t, srv)
	data, timestamp, err := c.DoJSONWithTimestamp(context.Background(), http.MethodPut, "/tasks", "new", Session{ID: "sess-9"}, url.Values{}, map[string]any{"title": "Buy milk"})
	if err != nil {
		t.Fatalf("DoJSONWithTimestamp failed: %v", err)
	}
	if !strings.Contains(string(data), `"id":4`) {
		t.Errorf("data = %s", data)
	}
	if timestamp != 1784855706453 {
		t.Errorf("timestamp = %d, want 1784855706453", timestamp)
	}

	req := srv.Requests()[0]
	if req.Header.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", req.Header.Get("Content-Type"))
	}
	body, _ := io.ReadAll(req.Body)
	if !strings.Contains(string(body), `"title":"Buy milk"`) {
		t.Errorf("body = %s, want to contain title", body)
	}
}

func TestDoJSONWithTimestamp_ErrorEnvelopeReturnsStructuredError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	srv.Handle("/ajax/tasks", "new", testutil.Response{
		Body: map[string]any{"error": "boom", "code": "SVL-0001", "categories": "USER_INPUT"},
	})

	c := newTestClient(t, srv)
	_, _, err := c.DoJSONWithTimestamp(context.Background(), http.MethodPut, "/tasks", "new", Session{ID: "sess-9"}, url.Values{}, map[string]any{"title": "Buy milk"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDoRawBody_SendsBodyWithContentTypeAndReturnsData(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	srv.Handle("/ajax/mail", "new", testutil.Response{
		Body: map[string]any{"data": map[string]any{"folder_id": "default0/Drafts", "id": "42"}},
	})

	c := newTestClient(t, srv)
	raw := []byte("Subject: hi\r\n\r\nbody text")
	data, err := c.DoRawBody(context.Background(), http.MethodPut, "/mail", "new", Session{ID: "sess-9"}, url.Values{"folder": {"default0/Drafts"}}, "application/octet-stream", raw)
	if err != nil {
		t.Fatalf("DoRawBody failed: %v", err)
	}
	if !strings.Contains(string(data), `"id":"42"`) {
		t.Errorf("data = %s, want id 42", data)
	}

	req := srv.Requests()[0]
	if req.Method != http.MethodPut {
		t.Errorf("method = %s, want PUT", req.Method)
	}
	if req.Header.Get("Content-Type") != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", req.Header.Get("Content-Type"))
	}
	if req.URL.Query().Get("folder") != "default0/Drafts" {
		t.Errorf("folder query = %q, want default0/Drafts", req.URL.Query().Get("folder"))
	}
	body, _ := io.ReadAll(req.Body)
	if string(body) != string(raw) {
		t.Errorf("body = %q, want %q", body, raw)
	}
}

func TestDoRawBody_ErrorEnvelopeReturnsStructuredError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	srv.Handle("/ajax/mail", "new", testutil.Response{
		Body: map[string]any{"error": "boom", "code": "SVL-0001", "categories": "USER_INPUT"},
	})

	c := newTestClient(t, srv)
	_, err := c.DoRawBody(context.Background(), http.MethodPut, "/mail", "new", Session{ID: "sess-9"}, url.Values{}, "application/octet-stream", []byte("x"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDoJSON_ErrorEnvelopeReturnsStructuredError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	srv.Handle("/ajax/resource", "search", testutil.Response{
		Body: map[string]any{
			"error": "server error", "code": "SVL-0011", "categories": "ERROR", "error_id": "req-3",
		},
	})

	c := newTestClient(t, srv)
	_, err := c.DoJSON(context.Background(), http.MethodPut, "/resource", "search", Session{ID: "sess-9"}, url.Values{}, map[string]any{"pattern": "*"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDo_ErrorEnvelopeReturnsStructuredError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	srv.Handle("/ajax/user", "get", testutil.Response{
		Body: map[string]any{
			"error": "Your session expired. Please login again.",
			"code":  "SES-0203", "categories": "TRY_AGAIN", "error_id": "req-2",
		},
	})

	c := newTestClient(t, srv)
	_, err := c.Do(context.Background(), http.MethodGet, "/user", "get", Session{ID: "sess-9"}, url.Values{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLogout_Success_EmptyBodySendsSessionAndCookies(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	srv.Handle("/ajax/login", "logout", testutil.Response{Body: []byte{}})

	c := newTestClient(t, srv)
	sess := Session{ID: "sess-1", Cookies: []*http.Cookie{{Name: "JSESSIONID", Value: "j1"}}}
	if err := c.Logout(context.Background(), sess); err != nil {
		t.Fatalf("Logout failed: %v", err)
	}

	reqs := srv.Requests()
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	req := reqs[0]
	if req.Method != http.MethodGet {
		t.Errorf("method = %s, want GET", req.Method)
	}
	if req.URL.Query().Get("session") != "sess-1" {
		t.Errorf("session param = %q, want sess-1", req.URL.Query().Get("session"))
	}
	found := false
	for _, ck := range req.Cookies() {
		if ck.Name == "JSESSIONID" && ck.Value == "j1" {
			found = true
		}
	}
	if !found {
		t.Errorf("cookies not sent, got %+v", req.Cookies())
	}
}

func TestLogout_ErrorEnvelopeReturnsStructuredError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	srv.Handle("/ajax/login", "logout", testutil.Response{
		Body: map[string]any{
			"error": "Your session expired. Please login again.",
			"code":  "SES-0203", "categories": "TRY_AGAIN", "error_id": "req-9",
		},
	})

	c := newTestClient(t, srv)
	err := c.Logout(context.Background(), Session{ID: "sess-9"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDoRaw_ReturnsRawBodyUnchanged(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	srv.Handle("/ajax/infostore", "document", testutil.Response{
		Body: []byte("hello file content"),
	})

	c := newTestClient(t, srv)
	data, err := c.DoRaw(context.Background(), http.MethodGet, "/infostore", "document", Session{ID: "sess-9"}, url.Values{"id": {"36/1"}})
	if err != nil {
		t.Fatalf("DoRaw failed: %v", err)
	}
	if string(data) != "hello file content" {
		t.Errorf("data = %q, want raw body", data)
	}

	req := srv.Requests()[0]
	if req.URL.Query().Get("session") != "sess-9" {
		t.Errorf("session query param = %q, want sess-9", req.URL.Query().Get("session"))
	}
}

func TestDoRaw_ErrorEnvelopeReturnsStructuredError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	srv.Handle("/ajax/infostore", "document", testutil.Response{
		Body: map[string]any{"error": "not found", "code": "FILE_STORAGE-0026", "categories": "USER_INPUT"},
	})

	c := newTestClient(t, srv)
	_, err := c.DoRaw(context.Background(), http.MethodGet, "/infostore", "document", Session{ID: "sess-9"}, url.Values{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDoMultipart_SendsJSONAndFilePartsAndParsesCallbackJSON(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	srv.Handle("/ajax/infostore", "new", testutil.Response{
		Body: []byte(`<!DOCTYPE html><html><head><script type="text/javascript">(parent["callback_new"])({"data":"36/2","timestamp":1784855719914})</script></head></html>`),
	})

	c := newTestClient(t, srv)
	meta := map[string]any{"folder_id": "36", "title": "test.txt", "filename": "test.txt"}
	data, err := c.DoMultipart(context.Background(), "/infostore", "new", Session{ID: "sess-9"}, url.Values{}, meta, "test.txt", "text/plain", strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("DoMultipart failed: %v", err)
	}
	if string(data) != `"36/2"` {
		t.Errorf("data = %s, want \"36/2\"", data)
	}

	req := srv.Requests()[0]
	if req.URL.Query().Get("session") != "sess-9" {
		t.Errorf("session query param = %q, want sess-9", req.URL.Query().Get("session"))
	}
	if !strings.Contains(req.Header.Get("Content-Type"), "multipart/form-data") {
		t.Errorf("content type = %q, want multipart/form-data", req.Header.Get("Content-Type"))
	}

	mr, err := req.MultipartReader()
	if err != nil {
		t.Fatalf("MultipartReader failed: %v", err)
	}
	var sawJSON, sawFile bool
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}
		body, _ := io.ReadAll(part)
		switch part.FormName() {
		case "json":
			sawJSON = true
			if !strings.Contains(string(body), `"folder_id":"36"`) {
				t.Errorf("json part = %s, want folder_id", body)
			}
		case "file":
			sawFile = true
			if string(body) != "hello" {
				t.Errorf("file part = %q, want hello", body)
			}
			if part.FileName() != "test.txt" {
				t.Errorf("filename = %q, want test.txt", part.FileName())
			}
		}
	}
	if !sawJSON || !sawFile {
		t.Errorf("sawJSON=%v sawFile=%v, want both parts present", sawJSON, sawFile)
	}
}

func TestDoMultipartCallback_SendsJSONAndOptionalFilePartsAndParsesCallbackJSON(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	srv.Handle("/ajax/mail", "new", testutil.Response{
		Body: []byte(`<!DOCTYPE html><html><head><script type="text/javascript">(parent["callback_new"])({"data":{"id":"123"},"timestamp":1784855719914})</script></head></html>`),
	})

	c := newTestClient(t, srv)
	meta := map[string]any{"subject": "hi", "to": [][2]any{{nil, "fixture-user@example.invalid"}}}
	attachments := []Attachment{
		{FieldName: "file_0", Filename: "a.txt", ContentType: "text/plain", Content: strings.NewReader("attachment body")},
	}
	data, err := c.DoMultipartCallback(context.Background(), "/mail", "new", Session{ID: "sess-9"}, url.Values{}, meta, attachments)
	if err != nil {
		t.Fatalf("DoMultipartCallback failed: %v", err)
	}
	if string(data) != `{"id":"123"}` {
		t.Errorf("data = %s, want {\"id\":\"123\"}", data)
	}

	req := srv.Requests()[0]
	if req.URL.Query().Get("session") != "sess-9" {
		t.Errorf("session query param = %q, want sess-9", req.URL.Query().Get("session"))
	}
	if !strings.Contains(req.Header.Get("Content-Type"), "multipart/form-data") {
		t.Errorf("content type = %q, want multipart/form-data", req.Header.Get("Content-Type"))
	}

	mr, err := req.MultipartReader()
	if err != nil {
		t.Fatalf("MultipartReader failed: %v", err)
	}
	var sawJSON, sawFile bool
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}
		body, _ := io.ReadAll(part)
		switch part.FormName() {
		case "json_0":
			sawJSON = true
			if !strings.Contains(string(body), `"subject":"hi"`) {
				t.Errorf("json_0 part = %s, want subject", body)
			}
		case "file_0":
			sawFile = true
			if string(body) != "attachment body" {
				t.Errorf("file_0 part = %q, want attachment body", body)
			}
			if part.FileName() != "a.txt" {
				t.Errorf("filename = %q, want a.txt", part.FileName())
			}
		}
	}
	if !sawJSON || !sawFile {
		t.Errorf("sawJSON=%v sawFile=%v, want both parts present", sawJSON, sawFile)
	}
}

func TestDoMultipartCallback_NoAttachmentsSendsOnlyJSONPart(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	srv.Handle("/ajax/mail", "new", testutil.Response{
		Body: []byte(`(parent["callback_new"])({"data":{"id":"124"}})`),
	})

	c := newTestClient(t, srv)
	meta := map[string]any{"subject": "hi"}
	data, err := c.DoMultipartCallback(context.Background(), "/mail", "new", Session{ID: "sess-9"}, url.Values{}, meta, nil)
	if err != nil {
		t.Fatalf("DoMultipartCallback failed: %v", err)
	}
	if string(data) != `{"id":"124"}` {
		t.Errorf("data = %s, want {\"id\":\"124\"}", data)
	}

	req := srv.Requests()[0]
	mr, err := req.MultipartReader()
	if err != nil {
		t.Fatalf("MultipartReader failed: %v", err)
	}
	var parts int
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}
		if part.FormName() != "json_0" {
			t.Errorf("unexpected part %q, want only json_0", part.FormName())
		}
		parts++
	}
	if parts != 1 {
		t.Errorf("parts = %d, want 1", parts)
	}
}

func TestDoMultipartCallback_ErrorEnvelopeReturnsStructuredError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	srv.Handle("/ajax/mail", "new", testutil.Response{
		Body: map[string]any{"error": "invalid recipient", "code": "MSG-0001", "categories": "USER_INPUT"},
	})

	c := newTestClient(t, srv)
	meta := map[string]any{"subject": "hi"}
	_, err := c.DoMultipartCallback(context.Background(), "/mail", "new", Session{ID: "sess-9"}, url.Values{}, meta, nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDoMultipart_ErrorEnvelopeReturnsStructuredError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	srv.Handle("/ajax/infostore", "new", testutil.Response{
		Body: map[string]any{"error": "no space", "code": "IFO-0100", "categories": "USER_INPUT"},
	})

	c := newTestClient(t, srv)
	meta := map[string]any{"folder_id": "36", "title": "test.txt"}
	_, err := c.DoMultipart(context.Background(), "/infostore", "new", Session{ID: "sess-9"}, url.Values{}, meta, "test.txt", "text/plain", strings.NewReader("hello"))
	if err == nil {
		t.Fatal("expected error")
	}
}

// TestDoRaw_JSONDocumentWithErrorKeyIsReturnedNotRejected is the regression test for a
// download that can never succeed: DoRaw returns a document body rather than a JSON
// envelope, so probing it for a top-level "error" key mistook any stored JSON file that
// happens to have one for an OX error and refused to hand it back.
func TestDoRaw_JSONDocumentWithErrorKeyIsReturnedNotRejected(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	document := []byte(`{"error":"disk full","retries":3}`)
	srv.Handle("/ajax/infostore", "document", testutil.Response{
		Body: document,
	})

	c := newTestClient(t, srv)
	got, err := c.DoRaw(context.Background(), http.MethodGet, "/infostore", "document", Session{ID: "sess-1"}, nil)
	if err != nil {
		t.Fatalf("DoRaw() error = %v, want the document body", err)
	}
	if string(got) != string(document) {
		t.Errorf("DoRaw() = %q, want %q", got, document)
	}
}

// TestDoRaw_ErrorEnvelopeWithFailureStatusIsStillMapped pins that a genuine OX failure is
// still reported, even though the body now has to carry the envelope's identifying fields
// to be treated as one.
func TestDoRaw_ErrorEnvelopeWithFailureStatusIsStillMapped(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/infostore", "document", testutil.Response{
		Status: http.StatusNotFound,
		Body:   map[string]any{"error": "object not found", "code": "IFO-0300", "categories": "ERROR"},
	})

	c := newTestClient(t, srv)
	_, err := c.DoRaw(context.Background(), http.MethodGet, "/infostore", "document", Session{ID: "sess-1"}, nil)
	if err == nil {
		t.Fatal("DoRaw() error = nil, want the OX error envelope mapped")
	}
	var cerr *cliErrors.Error
	if !errors.As(err, &cerr) || cerr.Code != "IFO-0300" {
		t.Fatalf("DoRaw() error = %v, want a structured IFO-0300 error", err)
	}
}

// TestDoRaw_JSONErrorEnvelopeIsStillMapped covers the ordinary failure shape: a JSON
// content type carrying an error envelope, returned with a 200 as the middleware does.
func TestDoRaw_JSONErrorEnvelopeIsStillMapped(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/infostore", "document", testutil.Response{
		Body: map[string]any{"error": "permission denied", "code": "IFO-0400", "categories": "PERMISSION"},
	})

	c := newTestClient(t, srv)
	_, err := c.DoRaw(context.Background(), http.MethodGet, "/infostore", "document", Session{ID: "sess-1"}, nil)
	if err == nil {
		t.Fatal("DoRaw() error = nil, want the OX error envelope mapped")
	}
}
