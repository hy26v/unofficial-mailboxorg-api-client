// ABOUTME: Verifies cross-process-equivalent pacing, login backoff, and shared 429 cooldowns.
// ABOUTME: Ensures the guard never retries requests, including ambiguous mutations.
package requestguard

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func response(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func testTransport(base http.RoundTripper, statePath string) *Transport {
	guard := New(base, statePath)
	guard.MinInterval = -1
	guard.InitialLoginBackoff = 500 * time.Millisecond
	guard.MaxLoginBackoff = 2 * time.Second
	guard.FailureWindow = 5 * time.Second
	guard.lockPollInterval = time.Millisecond
	guard.staleLockAge = time.Second
	return guard
}

func doAndClose(t *testing.T, client *http.Client, req *http.Request) (*http.Response, error) {
	t.Helper()
	resp, err := client.Do(req)
	if err != nil {
		return resp, err
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatalf("reading response: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("closing response: %v", err)
	}
	return resp, nil
}

func TestTransportSpacesRequestsAcrossInstances(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "request-guard.json")
	var mu sync.Mutex
	var starts []time.Time
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()
		return response(req, http.StatusOK, `{}`), nil
	})
	first := testTransport(base, statePath)
	second := testTransport(base, statePath)
	first.MinInterval = 60 * time.Millisecond
	second.MinInterval = 60 * time.Millisecond

	for _, guard := range []*Transport{first, second} {
		req, err := http.NewRequest(http.MethodGet, "https://office.example.org/ajax/user?action=get", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := doAndClose(t, &http.Client{Transport: guard}, req); err != nil {
			t.Fatalf("request failed: %v", err)
		}
	}

	if len(starts) != 2 {
		t.Fatalf("transport calls = %d, want 2", len(starts))
	}
	if gap := starts[1].Sub(starts[0]); gap < 50*time.Millisecond {
		t.Errorf("cross-instance request gap = %v, want at least 50ms", gap)
	}
	info, err := os.Stat(statePath)
	if err != nil {
		t.Fatalf("stat state: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("state mode = %o, want 600", got)
	}
}

func TestRequestCostReservesOneSharedSlotPerBatchAction(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "request-guard.json")
	var mu sync.Mutex
	var starts []time.Time
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()
		return response(req, http.StatusOK, `{}`), nil
	})
	first := testTransport(base, statePath)
	second := testTransport(base, statePath)
	first.MinInterval = 30 * time.Millisecond
	second.MinInterval = 30 * time.Millisecond

	ctx := WithRequestCost(context.Background(), 3)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, "https://office.example.org/ajax/multiple", strings.NewReader(`[]`))
	if _, err := doAndClose(t, &http.Client{Transport: first}, req); err != nil {
		t.Fatalf("batch request: %v", err)
	}
	req, _ = http.NewRequest(http.MethodGet, "https://office.example.org/ajax/user?action=get", nil)
	if _, err := doAndClose(t, &http.Client{Transport: second}, req); err != nil {
		t.Fatalf("following request: %v", err)
	}

	if len(starts) != 2 {
		t.Fatalf("transport calls = %d, want 2", len(starts))
	}
	if gap := starts[1].Sub(starts[0]); gap < 80*time.Millisecond {
		t.Errorf("request gap after cost-3 batch = %v, want at least 80ms", gap)
	}
}

func TestFailedCredentialLoginBacksOffAcrossInstances(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "request-guard.json")
	var calls atomic.Int32
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return response(req, http.StatusOK, `{"error":"Invalid credentials","code":"LGI-0006"}`), nil
	})
	first := testTransport(base, statePath)
	second := testTransport(base, statePath)

	req, err := http.NewRequest(http.MethodPost, "https://office.example.org/ajax/login?action=login", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := doAndClose(t, &http.Client{Transport: first}, req); err != nil {
		t.Fatalf("first login request failed at transport layer: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, "https://office.example.org/ajax/login?action=login", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&http.Client{Transport: second}).Do(req)
	var rateErr *cliErrors.Error
	if !errors.As(err, &rateErr) {
		t.Fatalf("second login error = %v, want structured rate limit", err)
	}
	if rateErr.Code != "rate_limited" || !rateErr.Retryable || rateErr.RetryAfterMs < 350 {
		t.Errorf("second login error = %+v, want retryable local backoff", rateErr)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("provider login calls = %d, want exactly 1", got)
	}
}

func TestBareCredentialLoginFailureAlsoBacksOff(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "request-guard.json")
	var calls atomic.Int32
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return response(req, http.StatusUnauthorized, `not json`), nil
	})
	first := testTransport(base, statePath)
	second := testTransport(base, statePath)

	req, _ := http.NewRequest(http.MethodPost, "https://office.example.org/ajax/login?action=login", nil)
	if _, err := doAndClose(t, &http.Client{Transport: first}, req); err != nil {
		t.Fatalf("first login transport error: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	req, _ = http.NewRequestWithContext(ctx, http.MethodPost, "https://office.example.org/ajax/login?action=login", nil)
	_, err := (&http.Client{Transport: second}).Do(req)
	var rateErr *cliErrors.Error
	if !errors.As(err, &rateErr) || rateErr.Code != "rate_limited" {
		t.Fatalf("second login error = %v, want local rate limit", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("provider login calls = %d, want 1", got)
	}
}

func TestCredentialLoginLockSerializesConcurrentFailures(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "request-guard.json")
	var calls atomic.Int32
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		return response(req, http.StatusOK, `{"error":"Invalid credentials","code":"LGI-0006"}`), nil
	})

	start := make(chan struct{})
	errs := make(chan error, 2)
	for range 2 {
		guard := testTransport(base, statePath)
		go func() {
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://office.example.org/ajax/login?action=login", nil)
			_, err := doAndCloseNoTest(&http.Client{Transport: guard}, req)
			errs <- err
		}()
	}
	close(start)
	var transportSuccesses, localLimits int
	for range 2 {
		err := <-errs
		if err == nil {
			transportSuccesses++
			continue
		}
		var rateErr *cliErrors.Error
		if errors.As(err, &rateErr) && rateErr.Code == "rate_limited" {
			localLimits++
			continue
		}
		t.Fatalf("unexpected concurrent login error: %v", err)
	}
	if transportSuccesses != 1 || localLimits != 1 || calls.Load() != 1 {
		t.Errorf("successes=%d local_limits=%d provider_calls=%d, want 1/1/1", transportSuccesses, localLimits, calls.Load())
	}
}

func doAndCloseNoTest(client *http.Client, req *http.Request) (*http.Response, error) {
	resp, err := client.Do(req)
	if err != nil {
		return resp, err
	}
	_, readErr := io.Copy(io.Discard, resp.Body)
	closeErr := resp.Body.Close()
	if readErr != nil {
		return resp, readErr
	}
	return resp, closeErr
}

func TestSuccessfulCredentialLoginClearsBackoff(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "request-guard.json")
	var calls atomic.Int32
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return response(req, http.StatusOK, `{"error":"Invalid credentials","code":"LGI-0006"}`), nil
		}
		return response(req, http.StatusOK, `{"session":"sess-1"}`), nil
	})
	guard := testTransport(base, statePath)
	guard.InitialLoginBackoff = 20 * time.Millisecond
	guard.MaxLoginBackoff = 20 * time.Millisecond
	client := &http.Client{Transport: guard}

	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://office.example.org/ajax/login?action=login", nil)
		_, err := doAndClose(t, client, req)
		cancel()
		if err != nil {
			t.Fatalf("login %d: %v", i+1, err)
		}
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("provider login calls = %d, want 3 after successful reset", got)
	}
}

func TestProvider429CreatesSharedCooldownWithoutRetry(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "request-guard.json")
	var calls atomic.Int32
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		resp := response(req, http.StatusTooManyRequests, `{"error":"rate limited"}`)
		resp.Header.Set("Retry-After", "2")
		return resp, nil
	})
	first := testTransport(base, statePath)
	second := testTransport(base, statePath)

	req, _ := http.NewRequest(http.MethodPut, "https://office.example.org/ajax/tasks?action=new", strings.NewReader(`{}`))
	resp, err := doAndClose(t, &http.Client{Transport: first}, req)
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("first status = %d, want 429", resp.StatusCode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	req, _ = http.NewRequestWithContext(ctx, http.MethodPut, "https://office.example.org/ajax/tasks?action=new", strings.NewReader(`{}`))
	_, err = (&http.Client{Transport: second}).Do(req)
	var rateErr *cliErrors.Error
	if !errors.As(err, &rateErr) || rateErr.Code != "rate_limited" || rateErr.RetryAfterMs < 1_500 {
		t.Fatalf("second request error = %v (%+v), want shared provider cooldown", err, rateErr)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("provider calls = %d, want 1 (no automatic mutation retry)", got)
	}
}

func TestProviderLoginRateCodeCreatesLoginCooldown(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "request-guard.json")
	var calls atomic.Int32
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return response(req, http.StatusOK, `{"error":"Too many login attempts","code":"LGI-0026"}`), nil
	})
	first := testTransport(base, statePath)
	second := testTransport(base, statePath)

	req, _ := http.NewRequest(http.MethodPost, "https://office.example.org/ajax/login?action=login", nil)
	resp, err := doAndClose(t, &http.Client{Transport: first}, req)
	if err != nil {
		t.Fatalf("first login: %v", err)
	}
	if got := resp.Header.Get("Retry-After"); got != "5" {
		t.Errorf("synthesized Retry-After = %q, want 5", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	req, _ = http.NewRequestWithContext(ctx, http.MethodPost, "https://office.example.org/ajax/login?action=login", nil)
	_, err = (&http.Client{Transport: second}).Do(req)
	var rateErr *cliErrors.Error
	if !errors.As(err, &rateErr) || rateErr.Code != "rate_limited" || rateErr.RetryAfterMs < 4_500 {
		t.Fatalf("second login error = %v (%+v), want shared login cooldown", err, rateErr)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("provider login calls = %d, want 1", got)
	}
}

func TestExponentialBackoffCapsAtMaximum(t *testing.T) {
	initial := 2 * time.Second
	maximum := time.Minute
	wants := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second, time.Minute, time.Minute}
	for i, want := range wants {
		if got := exponentialBackoff(initial, maximum, i+1); got != want {
			t.Errorf("failure %d backoff = %v, want %v", i+1, got, want)
		}
	}
}
