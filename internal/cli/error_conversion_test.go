// ABOUTME: Covers structured transport errors that net/http or another layer wraps.
// ABOUTME: Keeps local request-guard retry metadata intact in CLI error output.
package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/requestguard"
)

func TestToCLIErrorUnwrapsStructuredError(t *testing.T) {
	want := &cliErrors.Error{
		Code:         "rate_limited",
		Message:      "request delayed by provider rate limit",
		Retryable:    true,
		RetryAfterMs: 12_345,
	}
	got := toCLIError(fmt.Errorf("transport failed: %w", want), "auth_failed")
	if got != want {
		t.Fatalf("toCLIError() = %+v, want original structured error %+v", got, want)
	}
}

func TestToCLIErrorDoesNotExposeSessionFromWrappedGuardFailure(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "request-guard.json")
	if err := os.Mkdir(statePath, 0o700); err != nil {
		t.Fatal(err)
	}
	guard := requestguard.New(http.DefaultTransport, statePath)
	req, err := http.NewRequest(http.MethodGet, "https://office.example.org/ajax/user?action=get&session=super-secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&http.Client{Transport: guard}).Do(req)
	if err == nil {
		t.Fatal("request succeeded, want guard-state failure")
	}
	got := toCLIError(err, "api_error")
	if got.Code != "request_guard_error" {
		t.Fatalf("error code = %q, want request_guard_error", got.Code)
	}
	if strings.Contains(got.Message, "super-secret") || strings.Contains(got.Message, "session=") {
		t.Errorf("structured guard error exposed request URL: %q", got.Message)
	}
}

func TestToCLIError_TransportFailureIsARetryableNetworkError(t *testing.T) {
	err := &url.Error{Op: "Get", URL: "https://office.mailbox.org/ajax/mail?session=secret-session", Err: context.DeadlineExceeded}
	got := toCLIError(err, "api_error")
	if got.Code != "network_error" || !got.Retryable || strings.Contains(got.Message, "secret-session") {
		t.Errorf("error = %+v, want a retryable network_error without the URL", got)
	}
	if got := toCLIError(context.DeadlineExceeded, "api_error"); got.Code != "network_error" || !got.Retryable {
		t.Errorf("deadline = %+v", got)
	}
}
