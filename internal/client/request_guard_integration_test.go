// ABOUTME: Verifies request-guard login cooldown metadata survives the HTTP/client boundary.
// ABOUTME: Pins retryability, Retry-After propagation, and the absence of automatic retries.
package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/requestguard"
)

func TestLoginRateLimitCodeGetsGuardCooldownMetadataWithoutRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":"Too many login attempts","code":"LGI-0026","categories":"ERROR"}`))
	}))
	defer srv.Close()

	guard := requestguard.New(srv.Client().Transport, filepath.Join(t.TempDir(), "request-guard.json"))
	guard.MinInterval = -1
	guard.FailureWindow = 3 * time.Second
	httpClient := &http.Client{Transport: guard}
	c := New(httpClient, srv.URL+"/ajax", "mbx", "test", &SessionStore{Path: filepath.Join(t.TempDir(), "session.json")})

	_, err := c.Login(context.Background(), "test@example.org", "wrong")
	var rateErr *cliErrors.Error
	if !errors.As(err, &rateErr) {
		t.Fatalf("Login() error = %v, want structured provider error", err)
	}
	if rateErr.Code != "LGI-0026" || !rateErr.Retryable || rateErr.RetryAfterMs != 3_000 {
		t.Errorf("Login() error = %+v, want retryable LGI-0026 with 3000ms delay", rateErr)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("provider calls = %d, want exactly 1", got)
	}
}
