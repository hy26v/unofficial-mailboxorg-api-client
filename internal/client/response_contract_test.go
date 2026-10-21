// ABOUTME: Contract tests for HTTP-status handling and mandatory session ids in client
// ABOUTME: responses, including retry classification and provider-envelope precedence.
package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func TestHTTPStatusError_Classification(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		wantError bool
		retryable bool
	}{
		{name: "ok", status: http.StatusOK},
		{name: "no content", status: http.StatusNoContent},
		{name: "redirect", status: http.StatusTemporaryRedirect, wantError: true},
		{name: "bad request", status: http.StatusBadRequest, wantError: true},
		{name: "request timeout", status: http.StatusRequestTimeout, wantError: true, retryable: true},
		{name: "rate limited", status: http.StatusTooManyRequests, wantError: true, retryable: true},
		{name: "server error", status: http.StatusServiceUnavailable, wantError: true, retryable: true},
		{name: "nonstandard status", status: 600, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := httpStatusError(tt.status)
			if !tt.wantError {
				if got != nil {
					t.Fatalf("httpStatusError(%d) = %v, want nil", tt.status, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("httpStatusError(%d) = nil, want error", tt.status)
			}
			if got.Code != "http_error" || got.HTTPStatus != tt.status || got.Retryable != tt.retryable {
				t.Errorf("httpStatusError(%d) = %+v, want code=http_error status=%d retryable=%v", tt.status, got, tt.status, tt.retryable)
			}
		})
	}
}

func TestDo_NonSuccessStatusRejectsSuccessShapedBody(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/user", "get", testutil.Response{
		Status: http.StatusServiceUnavailable,
		Body:   map[string]any{"data": map[string]any{"email1": "stale@example.com"}},
	})

	c := newTestClient(t, srv)
	_, err := c.Do(context.Background(), http.MethodGet, "/user", "get", Session{ID: "sess-1"}, nil)
	var cerr *cliErrors.Error
	if !errors.As(err, &cerr) {
		t.Fatalf("Do() error = %v, want structured error", err)
	}
	if cerr.Code != "http_error" || cerr.HTTPStatus != http.StatusServiceUnavailable || !cerr.Retryable {
		t.Errorf("Do() error = %+v, want retryable HTTP 503 error", cerr)
	}
}

func TestDo_PropagatesRetryAfterHeader(t *testing.T) {
	tests := []struct {
		name      string
		header    func() string
		body      string
		minMillis int
		maxMillis int
	}{
		{
			name:      "delay seconds",
			header:    func() string { return "3" },
			minMillis: 3000,
			maxMillis: 3000,
		},
		{
			name: "HTTP date",
			header: func() string {
				return time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)
			},
			minMillis: 25000,
			maxMillis: 30000,
		},
		{
			name:      "huge seconds are capped",
			header:    func() string { return "3153600000" },
			minMillis: 3_600_000,
			maxMillis: 3_600_000,
		},
		{
			name:      "provider envelope",
			header:    func() string { return "2" },
			body:      `{"error":"retry later","code":"SVL-0011","categories":"TRY_AGAIN"}`,
			minMillis: 2000,
			maxMillis: 2000,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", tt.header())
				w.WriteHeader(http.StatusTooManyRequests)
				body := tt.body
				if body == "" {
					body = "rate limited"
				}
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()

			c := New(srv.Client(), srv.URL, "mbx", "test", nil)
			_, err := c.Do(context.Background(), http.MethodGet, "/user", "get", Session{ID: "sess-1"}, nil)
			var cerr *cliErrors.Error
			if !errors.As(err, &cerr) {
				t.Fatalf("Do() error = %v, want structured error", err)
			}
			if cerr.RetryAfterMs < tt.minMillis || cerr.RetryAfterMs > tt.maxMillis {
				t.Errorf("RetryAfterMs = %d, want between %d and %d", cerr.RetryAfterMs, tt.minMillis, tt.maxMillis)
			}
		})
	}
}

func TestDo_NonSuccessProviderEnvelopeKeepsProviderError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/user", "get", testutil.Response{
		Status: http.StatusInternalServerError,
		Body: map[string]any{
			"error": "provider failure", "code": "SVL-0011", "categories": "ERROR",
		},
	})

	c := newTestClient(t, srv)
	_, err := c.Do(context.Background(), http.MethodGet, "/user", "get", Session{ID: "sess-1"}, nil)
	var cerr *cliErrors.Error
	if !errors.As(err, &cerr) {
		t.Fatalf("Do() error = %v, want structured error", err)
	}
	if cerr.Code != "SVL-0011" {
		t.Errorf("Do() error code = %q, want provider code SVL-0011", cerr.Code)
	}
}

func TestDo_NonSuccessMalformedErrorFieldKeepsHTTPError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/user", "get", testutil.Response{
		Status: http.StatusBadGateway,
		Body:   []byte(`{"error":7,"code":"BROKEN"}`),
	})

	c := newTestClient(t, srv)
	_, err := c.Do(context.Background(), http.MethodGet, "/user", "get", Session{ID: "sess-1"}, nil)
	var cerr *cliErrors.Error
	if !errors.As(err, &cerr) {
		t.Fatalf("Do() error = %v, want structured error", err)
	}
	if cerr.Code != "http_error" || cerr.HTTPStatus != http.StatusBadGateway || !cerr.Retryable {
		t.Errorf("Do() error = %+v, want retryable HTTP 502 error", cerr)
	}
}

func TestDoRaw_NonSuccessStatusRejectsArbitraryBody(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/infostore", "document", testutil.Response{
		Status: http.StatusNotFound,
		Body:   []byte("proxy-generated not found page"),
	})

	c := newTestClient(t, srv)
	_, err := c.DoRaw(context.Background(), http.MethodGet, "/infostore", "document", Session{ID: "sess-1"}, nil)
	var cerr *cliErrors.Error
	if !errors.As(err, &cerr) || cerr.Code != "http_error" || cerr.HTTPStatus != http.StatusNotFound {
		t.Fatalf("DoRaw() error = %v, want structured HTTP 404 error", err)
	}
}

func TestEveryRequestVariant_RejectsNonSuccessHTTPStatus(t *testing.T) {
	type requestCase struct {
		module string
		action string
		body   any
		call   func(*Client) error
	}
	tests := map[string]requestCase{
		"login": {
			module: "/ajax/login", action: "login", body: map[string]any{"session": "must-not-be-accepted"},
			call: func(c *Client) error {
				_, err := c.Login(context.Background(), "user@example.com", "password")
				return err
			},
		},
		"logout": {
			module: "/ajax/login", action: "logout", body: nil,
			call: func(c *Client) error {
				return c.Logout(context.Background(), Session{ID: "sess-1"})
			},
		},
		"Do": {
			module: "/ajax/user", action: "get", body: map[string]any{"data": map[string]any{}},
			call: func(c *Client) error {
				_, err := c.Do(context.Background(), http.MethodGet, "/user", "get", Session{ID: "sess-1"}, nil)
				return err
			},
		},
		"DoWithTimestamp": {
			module: "/ajax/user", action: "get", body: map[string]any{"data": map[string]any{}, "timestamp": 1},
			call: func(c *Client) error {
				_, _, err := c.DoWithTimestamp(context.Background(), http.MethodGet, "/user", "get", Session{ID: "sess-1"}, nil)
				return err
			},
		},
		"DoRaw": {
			module: "/ajax/infostore", action: "document", body: []byte("raw document"),
			call: func(c *Client) error {
				_, err := c.DoRaw(context.Background(), http.MethodGet, "/infostore", "document", Session{ID: "sess-1"}, nil)
				return err
			},
		},
		"DoRawBody": {
			module: "/ajax/mail", action: "new", body: map[string]any{"data": map[string]any{"id": "1"}},
			call: func(c *Client) error {
				_, err := c.DoRawBody(context.Background(), http.MethodPut, "/mail", "new", Session{ID: "sess-1"}, nil, "text/plain", []byte("message"))
				return err
			},
		},
		"DoMultipart": {
			module: "/ajax/infostore", action: "new", body: map[string]any{"data": map[string]any{"id": "1"}},
			call: func(c *Client) error {
				_, err := c.DoMultipart(context.Background(), "/infostore", "new", Session{ID: "sess-1"}, nil, map[string]any{}, "file.txt", "text/plain", strings.NewReader("file"))
				return err
			},
		},
		"DoMultipartCallback": {
			module: "/ajax/mail", action: "new",
			body: []byte(`<script>(parent["callback_new"])({"data":"default0/INBOX/1"})</script>`),
			call: func(c *Client) error {
				_, err := c.DoMultipartCallback(context.Background(), "/mail", "new", Session{ID: "sess-1"}, nil, map[string]any{}, nil)
				return err
			},
		},
		"DoJSON": {
			module: "/ajax/resource", action: "search", body: map[string]any{"data": []any{}},
			call: func(c *Client) error {
				_, err := c.DoJSON(context.Background(), http.MethodPut, "/resource", "search", Session{ID: "sess-1"}, nil, map[string]any{})
				return err
			},
		},
		"DoJSONRaw": {
			module: "/ajax/multiple", action: "", body: []any{},
			call: func(c *Client) error {
				_, err := c.DoJSONRaw(context.Background(), http.MethodPut, "/multiple", "", Session{ID: "sess-1"}, nil, []any{})
				return err
			},
		},
		"DoJSONWithTimestamp": {
			module: "/ajax/tasks", action: "new", body: map[string]any{"data": map[string]any{"id": 1}, "timestamp": 1},
			call: func(c *Client) error {
				_, _, err := c.DoJSONWithTimestamp(context.Background(), http.MethodPut, "/tasks", "new", Session{ID: "sess-1"}, nil, map[string]any{})
				return err
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle(tt.module, tt.action, testutil.Response{Status: http.StatusBadGateway, Body: tt.body})

			err := tt.call(newTestClient(t, srv))
			var cerr *cliErrors.Error
			if !errors.As(err, &cerr) {
				t.Fatalf("%s error = %v, want structured error", name, err)
			}
			if cerr.Code != "http_error" || cerr.HTTPStatus != http.StatusBadGateway || !cerr.Retryable {
				t.Errorf("%s error = %+v, want retryable HTTP 502 error", name, cerr)
			}
		})
	}
}

func TestEveryEnvelopeRequestVariant_RejectsMalformedErrorField(t *testing.T) {
	// DoRaw is intentionally absent: its response is arbitrary user-controlled document
	// content, so it uses a stricter error-envelope detector than these protocol endpoints.
	type requestCase struct {
		module string
		action string
		call   func(*Client) error
	}
	tests := map[string]requestCase{
		"login": {
			module: "/ajax/login", action: "login",
			call: func(c *Client) error {
				_, err := c.Login(context.Background(), "user@example.com", "password")
				return err
			},
		},
		"logout": {
			module: "/ajax/login", action: "logout",
			call: func(c *Client) error {
				return c.Logout(context.Background(), Session{ID: "sess-1"})
			},
		},
		"Do": {
			module: "/ajax/user", action: "get",
			call: func(c *Client) error {
				_, err := c.Do(context.Background(), http.MethodGet, "/user", "get", Session{ID: "sess-1"}, nil)
				return err
			},
		},
		"DoWithTimestamp": {
			module: "/ajax/user", action: "get",
			call: func(c *Client) error {
				_, _, err := c.DoWithTimestamp(context.Background(), http.MethodGet, "/user", "get", Session{ID: "sess-1"}, nil)
				return err
			},
		},
		"DoRawBody": {
			module: "/ajax/mail", action: "new",
			call: func(c *Client) error {
				_, err := c.DoRawBody(context.Background(), http.MethodPut, "/mail", "new", Session{ID: "sess-1"}, nil, "text/plain", []byte("message"))
				return err
			},
		},
		"DoMultipart": {
			module: "/ajax/infostore", action: "new",
			call: func(c *Client) error {
				_, err := c.DoMultipart(context.Background(), "/infostore", "new", Session{ID: "sess-1"}, nil, map[string]any{}, "file.txt", "text/plain", strings.NewReader("file"))
				return err
			},
		},
		"DoMultipartCallback": {
			module: "/ajax/mail", action: "new",
			call: func(c *Client) error {
				_, err := c.DoMultipartCallback(context.Background(), "/mail", "new", Session{ID: "sess-1"}, nil, map[string]any{}, nil)
				return err
			},
		},
		"DoJSON": {
			module: "/ajax/resource", action: "search",
			call: func(c *Client) error {
				_, err := c.DoJSON(context.Background(), http.MethodPut, "/resource", "search", Session{ID: "sess-1"}, nil, map[string]any{})
				return err
			},
		},
		"DoJSONRaw": {
			module: "/ajax/multiple", action: "",
			call: func(c *Client) error {
				_, err := c.DoJSONRaw(context.Background(), http.MethodPut, "/multiple", "", Session{ID: "sess-1"}, nil, []any{})
				return err
			},
		},
		"DoJSONWithTimestamp": {
			module: "/ajax/tasks", action: "new",
			call: func(c *Client) error {
				_, _, err := c.DoJSONWithTimestamp(context.Background(), http.MethodPut, "/tasks", "new", Session{ID: "sess-1"}, nil, map[string]any{})
				return err
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle(tt.module, tt.action, testutil.Response{
				Body: []byte(`{"error":7,"code":"BROKEN"}`),
			})

			err := tt.call(newTestClient(t, srv))
			var cerr *cliErrors.Error
			if !errors.As(err, &cerr) {
				t.Fatalf("%s error = %v, want structured error", name, err)
			}
			if cerr.Code != "invalid_response" || cerr.HTTPStatus != http.StatusOK || !strings.Contains(cerr.Message, `"error" field`) {
				t.Errorf("%s error = %+v, want invalid_response for malformed error field with HTTP 200", name, cerr)
			}
		})
	}
}

func TestLogin_RejectsMissingSessionID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "   "}})

	c := newTestClient(t, srv)
	_, err := c.Login(context.Background(), "user@example.com", "password")
	var cerr *cliErrors.Error
	if !errors.As(err, &cerr) {
		t.Fatalf("Login() error = %v, want structured error", err)
	}
	if cerr.Code != "invalid_response" || !strings.Contains(cerr.Message, "session id") {
		t.Errorf("Login() error = %+v, want invalid_response mentioning session id", cerr)
	}
}

func TestLogin_ClassifiesMalformedResponse(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: []byte(`{"session":`)})

	c := newTestClient(t, srv)
	_, err := c.Login(context.Background(), "user@example.com", "password")
	var cerr *cliErrors.Error
	if !errors.As(err, &cerr) {
		t.Fatalf("Login() error = %T %v, want structured error", err, err)
	}
	if cerr.Code != "invalid_response" || cerr.HTTPStatus != http.StatusOK {
		t.Errorf("Login() error = %+v, want invalid_response with HTTP 200", cerr)
	}
}
