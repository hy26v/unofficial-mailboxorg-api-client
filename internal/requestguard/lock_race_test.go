// ABOUTME: Regression tests for the request guard's cross-process locks.
// ABOUTME: A live holder must keep its lock even when the lock file looks stale.
package requestguard

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGuardLockOfLiveHolderIsNotBrokenAsStale(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "request-guard.json")
	first := testTransport(http.DefaultTransport, statePath)
	second := testTransport(http.DefaultTransport, statePath)
	lockPath := statePath + ".login.lock"

	held, err := first.acquireLock(context.Background(), lockPath, first.lockStaleAfter())
	if err != nil {
		t.Fatal(err)
	}
	// The old age-based recovery treated this live lock as stale and removed it, so both
	// processes went on to send credential logins at the same time.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if stolen, err := second.acquireLock(ctx, lockPath, second.lockStaleAfter()); !errors.Is(err, context.DeadlineExceeded) {
		if stolen != nil {
			_ = stolen.release()
		}
		t.Fatalf("second acquire = %v, want it to wait for the live holder", err)
	}

	if err := held.release(); err != nil {
		t.Fatal(err)
	}
	next, err := second.acquireLock(context.Background(), lockPath, second.lockStaleAfter())
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	if err := next.release(); err != nil {
		t.Fatal(err)
	}
}
