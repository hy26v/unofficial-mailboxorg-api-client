// ABOUTME: Tests that provider Retry-After cooldowns and stored guard delays are capped.
// ABOUTME: A huge header or a corrupt request-guard.json must not block commands for years.
package requestguard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
)

func readStateFile(t *testing.T, path string) guardState {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state guardState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestProvider429WithHugeRetryAfterStoresCappedCooldown(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "request-guard.json")
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		resp := response(req, http.StatusTooManyRequests, `{}`)
		resp.Header.Set("Retry-After", "3153600000")
		return resp, nil
	})
	req, _ := http.NewRequest(http.MethodGet, "https://office.example.org/ajax/user?action=get", nil)
	if _, err := doAndClose(t, &http.Client{Transport: testTransport(base, statePath)}, req); err != nil {
		t.Fatal(err)
	}

	state := readStateFile(t, statePath)
	limit := time.Now().Add(MaxProviderCooldown + time.Second)
	if got := unixMilliTime(state.ServerCooldownUnixMs); got.After(limit) || got.Before(time.Now().Add(MaxProviderCooldown-time.Minute)) {
		t.Fatalf("stored cooldown = %v, want about %v from now", got, MaxProviderCooldown)
	}
}

func TestStoredOverlongCooldownIsClampedAndNamesStateFile(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "request-guard.json")
	far := time.Now().AddDate(100, 0, 0).UnixMilli()
	data, _ := json.Marshal(guardState{ServerCooldownUnixMs: far, NextLoginUnixMs: far, NextRequestUnixMs: far})
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	guard := testTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Fatal("provider contacted during a stored cooldown")
		return nil, nil
	}), statePath)

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://office.example.org/ajax/user?action=get", nil)
	_, err := (&http.Client{Transport: guard}).Do(req)
	var rateErr *cliErrors.Error
	if !errors.As(err, &rateErr) || rateErr.Code != "rate_limited" {
		t.Fatalf("error = %v, want rate_limited", err)
	}
	if maxMs := int(MaxProviderCooldown/time.Millisecond) + 1_000; rateErr.RetryAfterMs > maxMs || rateErr.RetryAfterMs <= 0 {
		t.Fatalf("retry_after_ms = %d, want a positive value of at most %d", rateErr.RetryAfterMs, maxMs)
	}
	if !strings.Contains(rateErr.Message, statePath) {
		t.Fatalf("message = %q, want it to name %s", rateErr.Message, statePath)
	}

	// Any later state update persists the clamped deadlines.
	if err := guard.updateState(context.Background(), func(*guardState) {}); err != nil {
		t.Fatal(err)
	}
	state := readStateFile(t, statePath)
	limit := time.Now().Add(MaxProviderCooldown + time.Second)
	for name, value := range map[string]int64{
		"server cooldown": state.ServerCooldownUnixMs,
		"next login":      state.NextLoginUnixMs,
		"next request":    state.NextRequestUnixMs,
	} {
		if unixMilliTime(value).After(limit) {
			t.Errorf("%s = %v after reload, want at most %v", name, unixMilliTime(value), limit)
		}
	}
}

func TestLoginCooldownMessageNamesStateFile(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "request-guard.json")
	data, _ := json.Marshal(guardState{NextLoginUnixMs: time.Now().Add(time.Minute).UnixMilli()})
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	guard := testTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Fatal("provider contacted during a login delay")
		return nil, nil
	}), statePath)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://office.example.org/ajax/login?action=login", nil)
	_, err := (&http.Client{Transport: guard}).Do(req)
	var rateErr *cliErrors.Error
	if !errors.As(err, &rateErr) || !strings.Contains(rateErr.Message, statePath) {
		t.Fatalf("error = %v, want a rate limit naming %s", err, statePath)
	}
}
