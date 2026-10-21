// ABOUTME: Tests the shared response size limits of the client request pipeline.
// ABOUTME: Oversized JSON and document bodies fail with response_too_large, not memory growth.
package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
)

func newLimitTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := New(srv.Client(), srv.URL+"/ajax", "mbx", "0.1.0", &SessionStore{Path: t.TempDir() + "/session.json"})
	c.maxJSONBytes = 64
	c.maxDocumentBytes = 128
	return c
}

func wantTooLarge(t *testing.T, err error) {
	t.Helper()
	var cerr *cliErrors.Error
	if !errors.As(err, &cerr) || cerr.Code != "response_too_large" || !strings.Contains(cerr.Message, "limit") {
		t.Fatalf("error = %v, want response_too_large", err)
	}
}

func TestResponseLimit_JSONBodyOverLimitFails(t *testing.T) {
	c := newLimitTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		// Streamed without Content-Length, so the limit must hold while reading.
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte(`{"data":"` + strings.Repeat("x", 200) + `"}`))
	})
	_, err := c.Do(context.Background(), http.MethodGet, "/user", "get", Session{ID: "s"}, nil)
	wantTooLarge(t, err)
	_, err = c.DoJSON(context.Background(), http.MethodPut, "/user", "update", Session{ID: "s"}, nil, map[string]string{})
	wantTooLarge(t, err)
}

func TestResponseLimit_DeclaredLengthOverLimitFailsBeforeReading(t *testing.T) {
	c := newLimitTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		_, _ = w.Write([]byte(strings.Repeat("x", 1000)))
	})
	_, err := c.DoRaw(context.Background(), http.MethodGet, "/infostore", "document", Session{ID: "s"}, nil)
	wantTooLarge(t, err)
}

func TestResponseLimit_DocumentsHaveTheirOwnLimit(t *testing.T) {
	document := strings.Repeat("d", 100) // over the JSON limit, under the document limit
	c := newLimitTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(document))
	})
	got, err := c.DoRaw(context.Background(), http.MethodGet, "/infostore", "document", Session{ID: "s"}, nil)
	if err != nil || string(got) != document {
		t.Fatalf("DoRaw() = %d bytes, %v; want the whole document", len(got), err)
	}
}

func TestResponseLimit_DefaultsCoverLargeLegitimateResponses(t *testing.T) {
	c := New(nil, "https://office.example.org/ajax", "mbx", "0.1.0", &SessionStore{})
	if c.responseLimit(jsonResponse) < 128<<20 {
		t.Errorf("JSON limit = %d, want at least 128 MiB", c.responseLimit(jsonResponse))
	}
	if c.responseLimit(documentResponse) < 1<<30 {
		t.Errorf("document limit = %d, want at least 1 GiB", c.responseLimit(documentResponse))
	}
}
