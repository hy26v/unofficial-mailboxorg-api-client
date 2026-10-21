// ABOUTME: Cross-process HTTP pacing and failed-login backoff for the mbx CLI.
// ABOUTME: Prevents request bursts and shares provider Retry-After cooldowns via MBX_HOME.
package requestguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/filelock"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/fsutil"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/retryafter"
)

const (
	// DefaultMinInterval keeps the CLI at a sustained four HTTP requests per second across
	// processes sharing one MBX_HOME. OX's documented generic default is 1,500 requests
	// per five minutes; the sustained rate leaves a 20 percent margin below that boundary.
	DefaultMinInterval = 250 * time.Millisecond

	// Failed credential logins use exponential backoff. The live mailbox.org endpoint
	// returned 429 on failure 21 with a ten-minute Retry-After, so this progression keeps
	// a broken unattended credential from reaching that boundary while still making one
	// corrected retry inexpensive.
	DefaultInitialLoginBackoff = 2 * time.Second
	DefaultMaxLoginBackoff     = time.Minute
	DefaultFailureWindow       = 10 * time.Minute

	defaultRetryAfter = time.Minute

	// MaxProviderCooldown caps how long one provider Retry-After value (and any stored
	// guard delay) can block requests. See retryafter.MaxDelay for the reasoning.
	MaxProviderCooldown = retryafter.MaxDelay

	defaultLockPollInterval = 25 * time.Millisecond
	defaultStaleLockAge     = 2 * time.Minute
	maxStateSize            = 64 << 10
	maxObservedBodySize     = 1 << 20
)

// Transport wraps an HTTP transport with a filesystem-coordinated request guard. The
// state path must be shared by every CLI process that should share one request budget.
type Transport struct {
	Base                http.RoundTripper
	StatePath           string
	MinInterval         time.Duration
	InitialLoginBackoff time.Duration
	MaxLoginBackoff     time.Duration
	FailureWindow       time.Duration

	lockPollInterval time.Duration
	staleLockAge     time.Duration
}

type requestCostKey struct{}

// WithRequestCost assigns the number of server-side actions represented by one HTTP
// exchange. Ordinary requests cost one; /multiple uses its sub-request count so batches
// cannot bypass the sustained shared budget.
func WithRequestCost(ctx context.Context, cost int) context.Context {
	if cost <= 1 {
		return ctx
	}
	return context.WithValue(ctx, requestCostKey{}, cost)
}

// New returns a transport with the CLI's conservative production defaults.
func New(base http.RoundTripper, statePath string) *Transport {
	if base == nil {
		base = http.DefaultTransport
	}
	return &Transport{
		Base:                base,
		StatePath:           statePath,
		MinInterval:         DefaultMinInterval,
		InitialLoginBackoff: DefaultInitialLoginBackoff,
		MaxLoginBackoff:     DefaultMaxLoginBackoff,
		FailureWindow:       DefaultFailureWindow,
		lockPollInterval:    defaultLockPollInterval,
		staleLockAge:        defaultStaleLockAge,
	}
}

// RoundTrip spaces every HTTP request, serializes full credential logins, and records
// provider cooldowns. It deliberately does not retry: replaying a mutation after an
// ambiguous response could apply it twice.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("request guard received a nil request")
	}
	if strings.TrimSpace(t.StatePath) == "" {
		return nil, requestGuardError(fmt.Errorf("request guard state path is empty"))
	}

	// A request that follows a redirect of a credential login carries that login's
	// outcome, so it is guarded and observed as part of the same login.
	credentialLogin := isCredentialLogin(req) || followsCredentialLoginRedirect(req)
	var loginLock *fileLock
	if credentialLogin {
		var err error
		loginLock, err = t.acquireLock(
			req.Context(),
			t.StatePath+".login.lock",
			maxDuration(t.lockStaleAfter(), t.failureWindow()+time.Minute),
		)
		if err != nil {
			return nil, requestGuardError(err)
		}
		if err := t.waitForLoginPermit(req.Context()); err != nil {
			_ = loginLock.release()
			return nil, requestGuardError(err)
		}
	}

	if err := t.waitForRequestPermit(req.Context(), requestCost(req.Context())); err != nil {
		if loginLock != nil {
			_ = loginLock.release()
		}
		return nil, requestGuardError(err)
	}

	resp, err := t.base().RoundTrip(req)
	if err != nil {
		if loginLock != nil {
			_ = loginLock.release()
		}
		return resp, err
	}
	if resp == nil {
		if loginLock != nil {
			_ = loginLock.release()
		}
		return nil, requestGuardError(fmt.Errorf("request guard transport returned a nil response"))
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		// Preserve the provider response even if persisting the shared cooldown fails.
		// The current request still reports the authoritative 429 to the caller.
		_ = t.observeRateLimit(resp.Header.Get("Retry-After"), credentialLogin)
	}

	if loginLock == nil {
		return resp, nil
	}
	// A redirect is not the login's result. The client follows it, and the guard observes
	// the response at the end of the redirect chain instead. Counting the redirect as a
	// rejection added a failure and a backoff to every successful redirected login.
	if isFollowableRedirect(resp) {
		if resp.Body == nil {
			_ = loginLock.release()
			return resp, nil
		}
		resp.Body = &observedBody{ReadCloser: resp.Body, limit: 0, done: func([]byte) { _ = loginLock.release() }}
		return resp, nil
	}
	if resp.Body == nil {
		t.observeCredentialLogin(resp.StatusCode, nil)
		_ = loginLock.release()
		return resp, nil
	}

	resp.Body = &observedBody{
		ReadCloser: resp.Body,
		limit:      maxObservedBodySize,
		done: func(body []byte) {
			if delay := t.observeCredentialLogin(resp.StatusCode, body); delay > 0 && resp.Header.Get("Retry-After") == "" {
				if resp.Header == nil {
					resp.Header = make(http.Header)
				}
				resp.Header.Set("Retry-After", strconv.FormatInt(durationSecondsCeil(delay), 10))
			}
			_ = loginLock.release()
		},
	}
	return resp, nil
}

func (t *Transport) base() http.RoundTripper {
	if t.Base == nil {
		return http.DefaultTransport
	}
	return t.Base
}

func isCredentialLogin(req *http.Request) bool {
	return req != nil && req.URL != nil && req.Method == http.MethodPost &&
		strings.HasSuffix(req.URL.Path, "/login") &&
		req.URL.Query().Get("action") == "login"
}

// followsCredentialLoginRedirect reports whether net/http created req to follow a
// redirect chain that started with a credential login. The client sets req.Response to
// the redirect response, whose Request is the previous hop.
func followsCredentialLoginRedirect(req *http.Request) bool {
	for hops, resp := 0, req.Response; resp != nil && hops < 20; hops++ {
		if isCredentialLogin(resp.Request) {
			return true
		}
		if resp.Request == nil {
			return false
		}
		resp = resp.Request.Response
	}
	return false
}

// isFollowableRedirect matches the responses net/http's client follows: a redirect
// status with a Location header. Other 3xx responses reach the caller as the result.
func isFollowableRedirect(resp *http.Response) bool {
	switch resp.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return resp.Header.Get("Location") != ""
	}
	return false
}

type guardState struct {
	NextRequestUnixMs      int64 `json:"next_request_unix_ms,omitempty"`
	ServerCooldownUnixMs   int64 `json:"server_cooldown_unix_ms,omitempty"`
	LoginFailures          int   `json:"login_failures,omitempty"`
	LastLoginFailureUnixMs int64 `json:"last_login_failure_unix_ms,omitempty"`
	NextLoginUnixMs        int64 `json:"next_login_unix_ms,omitempty"`
}

func (t *Transport) waitForRequestPermit(ctx context.Context, cost int) error {
	for {
		var blockedUntil time.Time
		err := t.readState(ctx, func(state guardState) {
			blockedUntil = unixMilliTime(state.ServerCooldownUnixMs)
		})
		if err != nil {
			return err
		}
		if err := waitUntil(ctx, blockedUntil, t.stateMessage("request delayed by provider rate limit")); err != nil {
			return err
		}

		now := time.Now()
		var scheduled time.Time
		var schedulingErr error
		err = t.updateState(ctx, func(state *guardState) {
			next := unixMilliTime(state.NextRequestUnixMs)
			scheduled = latestTime(now, next)
			if deadline, ok := ctx.Deadline(); ok && !deadline.After(scheduled) {
				schedulingErr = localRateLimitError("request delayed by local burst protection", time.Until(scheduled))
				return
			}
			state.NextRequestUnixMs = unixMilliCeil(scheduled.Add(requestInterval(t.minInterval(), cost)))
		})
		if err != nil {
			return err
		}
		if schedulingErr != nil {
			return schedulingErr
		}
		if err := waitUntil(ctx, scheduled, "request delayed by local burst protection"); err != nil {
			return err
		}

		// A different request can receive a provider cooldown while this request is
		// waiting for its reserved slot. Recheck immediately before going on the wire.
		blockedUntil = time.Time{}
		err = t.readState(ctx, func(state guardState) {
			blockedUntil = unixMilliTime(state.ServerCooldownUnixMs)
		})
		if err != nil {
			return err
		}
		if !blockedUntil.After(time.Now()) {
			return nil
		}
	}
}

func requestCost(ctx context.Context) int {
	cost, _ := ctx.Value(requestCostKey{}).(int)
	if cost < 1 {
		return 1
	}
	return cost
}

func requestInterval(interval time.Duration, cost int) time.Duration {
	if interval <= 0 || cost <= 0 {
		return 0
	}
	maxDuration := time.Duration(1<<63 - 1)
	if int64(cost) > int64(maxDuration/interval) {
		return maxDuration
	}
	return interval * time.Duration(cost)
}

func (t *Transport) waitForLoginPermit(ctx context.Context) error {
	for {
		now := time.Now()
		var permittedAt time.Time
		err := t.updateState(ctx, func(state *guardState) {
			t.expireLoginFailures(state, now)
			permittedAt = latestTime(
				now,
				unixMilliTime(state.ServerCooldownUnixMs),
				unixMilliTime(state.NextLoginUnixMs),
			)
		})
		if err != nil {
			return err
		}
		if err := waitUntil(ctx, permittedAt, t.stateMessage("credential login delayed after authentication failures or a provider rate limit")); err != nil {
			return err
		}

		var stillBlockedUntil time.Time
		err = t.readState(ctx, func(state guardState) {
			stillBlockedUntil = latestTime(
				unixMilliTime(state.ServerCooldownUnixMs),
				unixMilliTime(state.NextLoginUnixMs),
			)
		})
		if err != nil {
			return err
		}
		if !stillBlockedUntil.After(time.Now()) {
			return nil
		}
	}
}

// stateMessage names the shared state file in a delay message, so a user who sees a long
// cooldown knows which file holds it and which MBX_HOME it affects.
func (t *Transport) stateMessage(message string) string {
	return fmt.Sprintf("%s; the delay is stored in %s", message, t.StatePath)
}

func (t *Transport) observeRateLimit(retryAfter string, credentialLogin bool) error {
	now := time.Now()
	delay := retryafter.Parse(retryAfter, now)
	if delay <= 0 {
		delay = defaultRetryAfter
	}
	blockedUntil := now.Add(delay)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return t.updateState(ctx, func(state *guardState) {
		if unixMilliTime(state.ServerCooldownUnixMs).Before(blockedUntil) {
			state.ServerCooldownUnixMs = unixMilliCeil(blockedUntil)
		}
		if credentialLogin && unixMilliTime(state.NextLoginUnixMs).Before(blockedUntil) {
			state.NextLoginUnixMs = unixMilliCeil(blockedUntil)
		}
	})
}

func (t *Transport) observeCredentialLogin(status int, body []byte) time.Duration {
	if status == http.StatusTooManyRequests {
		return 0
	}

	var envelope struct {
		Error   json.RawMessage `json:"error"`
		Code    string          `json:"code"`
		Session string          `json:"session"`
	}
	parsed := json.Unmarshal(body, &envelope) == nil

	now := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if parsed && status >= http.StatusOK && status < http.StatusMultipleChoices &&
		strings.TrimSpace(envelope.Session) != "" && !nonNullJSON(envelope.Error) {
		_ = t.updateState(ctx, func(state *guardState) {
			state.LoginFailures = 0
			state.LastLoginFailureUnixMs = 0
			state.NextLoginUnixMs = 0
		})
		return 0
	}

	providerRateLimit := parsed && cliErrors.IsLoginRateLimitCode(envelope.Code)
	var providerDelay time.Duration
	if providerRateLimit {
		providerDelay = t.failureWindow()
	}
	_ = t.updateState(ctx, func(state *guardState) {
		t.expireLoginFailures(state, now)
		state.LoginFailures++
		state.LastLoginFailureUnixMs = now.UnixMilli()
		backoff := providerDelay
		if backoff == 0 {
			backoff = exponentialBackoff(t.initialLoginBackoff(), t.maxLoginBackoff(), state.LoginFailures)
		}
		next := now.Add(backoff)
		if unixMilliTime(state.NextLoginUnixMs).Before(next) {
			state.NextLoginUnixMs = unixMilliCeil(next)
		}
	})
	return providerDelay
}

func nonNullJSON(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) && !bytes.Equal(trimmed, []byte(`""`))
}

func (t *Transport) expireLoginFailures(state *guardState, now time.Time) {
	last := unixMilliTime(state.LastLoginFailureUnixMs)
	if last.IsZero() || now.Sub(last) < t.failureWindow() {
		return
	}
	state.LoginFailures = 0
	state.LastLoginFailureUnixMs = 0
	state.NextLoginUnixMs = 0
}

func exponentialBackoff(initial, maximum time.Duration, failures int) time.Duration {
	if failures <= 1 {
		return initial
	}
	backoff := initial
	for i := 1; i < failures && backoff < maximum; i++ {
		if backoff > maximum/2 {
			return maximum
		}
		backoff *= 2
	}
	if backoff > maximum {
		return maximum
	}
	return backoff
}

func (t *Transport) minInterval() time.Duration {
	if t.MinInterval < 0 {
		return 0
	}
	return t.MinInterval
}

func (t *Transport) initialLoginBackoff() time.Duration {
	if t.InitialLoginBackoff <= 0 {
		return DefaultInitialLoginBackoff
	}
	return t.InitialLoginBackoff
}

func (t *Transport) maxLoginBackoff() time.Duration {
	if t.MaxLoginBackoff <= 0 {
		return DefaultMaxLoginBackoff
	}
	if t.MaxLoginBackoff < t.initialLoginBackoff() {
		return t.initialLoginBackoff()
	}
	return t.MaxLoginBackoff
}

func (t *Transport) failureWindow() time.Duration {
	if t.FailureWindow <= 0 {
		return DefaultFailureWindow
	}
	return t.FailureWindow
}

func (t *Transport) pollInterval() time.Duration {
	if t.lockPollInterval <= 0 {
		return defaultLockPollInterval
	}
	return t.lockPollInterval
}

func (t *Transport) lockStaleAfter() time.Duration {
	if t.staleLockAge <= 0 {
		return defaultStaleLockAge
	}
	return t.staleLockAge
}

func waitUntil(ctx context.Context, at time.Time, message string) error {
	delay := time.Until(at)
	if delay <= 0 {
		return nil
	}
	if deadline, ok := ctx.Deadline(); ok && !deadline.After(at) {
		return localRateLimitError(message, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return localRateLimitError(message, time.Until(at))
		}
		return ctx.Err()
	}
}

func localRateLimitError(message string, delay time.Duration) *cliErrors.Error {
	if delay < 0 {
		delay = 0
	}
	return &cliErrors.Error{
		Code:         "rate_limited",
		Message:      message,
		Retryable:    true,
		RetryAfterMs: retryafter.Milliseconds(delay),
	}
}

func requestGuardError(err error) *cliErrors.Error {
	var structured *cliErrors.Error
	if errors.As(err, &structured) {
		return structured
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return localRateLimitError("request delayed by another mbx process", time.Second)
	}
	if errors.Is(err, context.Canceled) {
		return &cliErrors.Error{Code: "request_canceled", Message: "request canceled"}
	}
	return &cliErrors.Error{Code: "request_guard_error", Message: err.Error()}
}

func durationSecondsCeil(value time.Duration) int64 {
	if value <= 0 {
		return 0
	}
	seconds := value / time.Second
	if value%time.Second != 0 {
		seconds++
	}
	return int64(seconds)
}

func latestTime(values ...time.Time) time.Time {
	var latest time.Time
	for _, value := range values {
		if value.After(latest) {
			latest = value
		}
	}
	return latest
}

func unixMilliTime(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(value)
}

func unixMilliCeil(value time.Time) int64 {
	millis := value.UnixMilli()
	if value.After(time.UnixMilli(millis)) {
		millis++
	}
	return millis
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func (t *Transport) readState(ctx context.Context, inspect func(guardState)) error {
	lock, err := t.acquireLock(ctx, t.StatePath+".lock", t.lockStaleAfter())
	if err != nil {
		return err
	}
	defer lock.release()
	state, err := loadState(t.StatePath)
	if err != nil {
		return err
	}
	t.clampState(&state, time.Now())
	inspect(state)
	return nil
}

func (t *Transport) updateState(ctx context.Context, update func(*guardState)) error {
	lock, err := t.acquireLock(ctx, t.StatePath+".lock", t.lockStaleAfter())
	if err != nil {
		return err
	}
	defer lock.release()
	state, err := loadState(t.StatePath)
	if err != nil {
		return err
	}
	t.clampState(&state, time.Now())
	update(&state)
	return saveState(t.StatePath, state)
}

// maxStoredDelay is the longest wait any stored timestamp may impose. It covers the
// provider cap and this transport's own login delays, which may be configured longer.
func (t *Transport) maxStoredDelay() time.Duration {
	return maxDuration(MaxProviderCooldown, maxDuration(t.failureWindow(), t.maxLoginBackoff()))
}

// clampState limits stored wait deadlines to now plus maxStoredDelay. A state file
// written before Retry-After was capped, edited by hand, or written with a wrong clock can
// otherwise hold a deadline far in the future that blocks every command.
func (t *Transport) clampState(state *guardState, now time.Time) {
	limit := unixMilliCeil(now.Add(t.maxStoredDelay()))
	for _, deadline := range []*int64{&state.NextRequestUnixMs, &state.ServerCooldownUnixMs, &state.NextLoginUnixMs} {
		if *deadline > limit {
			*deadline = limit
		}
	}
}

func loadState(path string) (guardState, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return guardState{}, nil
	}
	if err != nil {
		return guardState{}, fmt.Errorf("could not inspect request guard state: %w", err)
	}
	if !info.Mode().IsRegular() {
		return guardState{}, fmt.Errorf("request guard state is not a regular file: %s", path)
	}
	if info.Size() > maxStateSize {
		return guardState{}, fmt.Errorf("request guard state exceeds %d bytes: %s", maxStateSize, path)
	}
	file, err := os.Open(path)
	if err != nil {
		return guardState{}, fmt.Errorf("could not open request guard state: %w", err)
	}
	defer file.Close()
	var state guardState
	decoder := json.NewDecoder(io.LimitReader(file, maxStateSize+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return guardState{}, fmt.Errorf("could not decode request guard state: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return guardState{}, fmt.Errorf("request guard state contains multiple JSON values: %s", path)
		}
		return guardState{}, fmt.Errorf("could not decode request guard state: %w", err)
	}
	return state, nil
}

func saveState(path string, state guardState) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("could not create request guard directory: %w", err)
	}
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("could not encode request guard state: %w", err)
	}
	if err := fsutil.WriteFileAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf("could not write request guard state: %w", err)
	}
	return nil
}

// fileLock is a held cross-process guard lock.
type fileLock struct {
	lock *filelock.Lock
}

// acquireLock takes the cross-process lock at path. On supported platforms the kernel
// owns the lock (flock or LockFileEx) and releases it when the holder exits, so a live
// holder is never mistaken for a stale one. staleAfter applies only to the token-file
// fallback on other platforms.
func (t *Transport) acquireLock(ctx context.Context, path string, staleAfter time.Duration) (*fileLock, error) {
	lock, err := filelock.Acquire(ctx, path, filelock.Options{PollInterval: t.pollInterval(), StaleAfter: staleAfter})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(err, ctxErr) {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("could not acquire request guard lock: %w", err)
	}
	return &fileLock{lock: lock}, nil
}

func (l *fileLock) release() error {
	if err := l.lock.Release(); err != nil {
		return fmt.Errorf("could not release request guard lock: %w", err)
	}
	return nil
}

type observedBody struct {
	io.ReadCloser
	limit int
	done  func([]byte)

	mu        sync.Mutex
	observed  []byte
	truncated bool
	once      sync.Once
}

func (b *observedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.mu.Lock()
	if remaining := b.limit - len(b.observed); remaining > 0 && n > 0 {
		keep := n
		if keep > remaining {
			keep = remaining
			b.truncated = true
		}
		b.observed = append(b.observed, p[:keep]...)
	} else if n > 0 {
		b.truncated = true
	}
	b.mu.Unlock()
	if err == io.EOF {
		b.finish()
	}
	return n, err
}

func (b *observedBody) Close() error {
	err := b.ReadCloser.Close()
	b.finish()
	return err
}

func (b *observedBody) finish() {
	b.once.Do(func() {
		b.mu.Lock()
		body := append([]byte(nil), b.observed...)
		truncated := b.truncated
		b.mu.Unlock()
		if truncated {
			body = nil
		}
		b.done(body)
	})
}
