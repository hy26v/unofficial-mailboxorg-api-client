// ABOUTME: Tests failed-login accounting when a credential login is redirected.
// ABOUTME: The redirect itself is neutral; the end of the redirect chain decides the outcome.
package requestguard

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func redirectingLoginBase(status int, location, finalBody string, calls *atomic.Int32) roundTripFunc {
	return func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		if req.Response == nil { // the first hop: the credential login itself
			resp := response(req, status, "")
			resp.Header.Set("Location", location)
			return resp, nil
		}
		return response(req, http.StatusOK, finalBody), nil
	}
}

func guardStateAfterLogin(t *testing.T, base roundTripFunc) guardState {
	t.Helper()
	statePath := filepath.Join(t.TempDir(), "request-guard.json")
	guard := testTransport(base, statePath)
	req, _ := http.NewRequest(http.MethodPost, "https://office.example.org/ajax/login?action=login",
		strings.NewReader("name=user&password=secret"))
	if _, err := doAndClose(t, &http.Client{Transport: guard}, req); err != nil {
		t.Fatalf("login: %v", err)
	}
	if _, err := os.Stat(statePath); os.IsNotExist(err) {
		return guardState{}
	}
	return readStateFile(t, statePath)
}

func TestRedirectedSuccessfulLoginIsNotCountedAsFailure(t *testing.T) {
	cases := map[string]struct {
		status   int
		location string
	}{
		"302 to a GET result":           {http.StatusFound, "/ajax/login/result"},
		"307 to the same login action":  {http.StatusTemporaryRedirect, "https://office.example.org/ajax/login?action=login&node=2"},
		"308 to another login endpoint": {http.StatusPermanentRedirect, "/ox/ajax/login?action=login"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			base := redirectingLoginBase(tc.status, tc.location, `{"session":"redirected-session"}`, &calls)
			state := guardStateAfterLogin(t, base)
			if calls.Load() != 2 {
				t.Fatalf("provider calls = %d, want the login and its redirect", calls.Load())
			}
			if state.LoginFailures != 0 || state.NextLoginUnixMs != 0 {
				t.Fatalf("state after a successful redirected login = %+v, want no failure or backoff", state)
			}
		})
	}
}

func TestRedirectedRejectedLoginCountsOneFailure(t *testing.T) {
	var calls atomic.Int32
	base := redirectingLoginBase(http.StatusFound, "/ajax/login/result", `{"error":"Invalid credentials","code":"LGI-0006"}`, &calls)
	state := guardStateAfterLogin(t, base)
	if state.LoginFailures != 1 || state.NextLoginUnixMs == 0 {
		t.Fatalf("state after a rejected redirected login = %+v, want exactly one failure with backoff", state)
	}
}
