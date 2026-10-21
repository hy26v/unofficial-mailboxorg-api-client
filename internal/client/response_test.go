// ABOUTME: Regression tests for validating successful OX response envelopes.
// ABOUTME: Ensures malformed success bodies cannot be mistaken for empty write results.
package client

import (
	"context"
	"errors"
	"net/http"
	"testing"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func TestDo_RejectsNullSuccessEnvelope(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/tasks", "update", testutil.Response{Body: []byte("null")})

	c := newTestClient(t, srv)
	_, err := c.Do(
		context.Background(),
		http.MethodPut,
		"/tasks",
		"update",
		Session{ID: "sess-1"},
		nil,
	)
	var cliErr *cliErrors.Error
	if !errors.As(err, &cliErr) || cliErr.Code != "invalid_response" {
		t.Fatalf("Do error = %v, want invalid_response", err)
	}
}

func TestDoWithTimestamp_RejectsNonPositiveTimestamp(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/tasks", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{"id": 4}, "timestamp": 0},
	})

	c := newTestClient(t, srv)
	_, _, err := c.DoWithTimestamp(
		context.Background(),
		http.MethodGet,
		"/tasks",
		"get",
		Session{ID: "sess-1"},
		nil,
	)
	var cliErr *cliErrors.Error
	if !errors.As(err, &cliErr) || cliErr.Code != "invalid_response" {
		t.Fatalf("DoWithTimestamp error = %v, want invalid_response", err)
	}
}

func TestEnvelopeDecoders_ClassifyMalformedJSONAsInvalidResponse(t *testing.T) {
	tests := map[string]func() error{
		"data": func() error {
			_, err := decodeDataEnvelope([]byte(`<html>not json</html>`), http.StatusOK)
			return err
		},
		"timestamped data": func() error {
			_, _, err := decodeTimestampedDataEnvelope([]byte(`<html>not json</html>`), http.StatusOK)
			return err
		},
	}
	for name, decode := range tests {
		t.Run(name, func(t *testing.T) {
			var cliErr *cliErrors.Error
			if err := decode(); !errors.As(err, &cliErr) || cliErr.Code != "invalid_response" {
				t.Fatalf("decoder error = %v, want invalid_response", err)
			}
		})
	}
}
