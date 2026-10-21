//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris || windows

// ABOUTME: Regression tests for cross-process lock exclusion, crash release and stale races.
// ABOUTME: Separate file handles model independent CLI processes contending for one lock path.
package filelock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAcquire_ContendsAndOwnerCloseReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transaction.lock")
	first, err := Acquire(context.Background(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}

	waitCtx, cancelWait := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancelWait()
	if _, err := Acquire(waitCtx, path, Options{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contending acquire error=%v, want deadline exceeded", err)
	}

	// Closing the owner handle without the normal unlock models process termination.
	// Kernel-backed locks must become available without deleting or aging the lock file.
	if first.file == nil {
		t.Fatal("kernel-backed lock did not retain its owner handle")
	}
	if err := first.file.Close(); err != nil {
		t.Fatal(err)
	}
	first.file = nil

	reacquireCtx, cancelReacquire := context.WithTimeout(context.Background(), time.Second)
	defer cancelReacquire()
	second, err := Acquire(reacquireCtx, path, Options{})
	if err != nil {
		t.Fatalf("reacquire after owner close: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("persistent lock inode is unavailable: info=%v err=%v", info, err)
	}
}

func TestAcquire_OldLockFileOfLiveHolderIsNotBroken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "request-guard.json.lock")
	opts := Options{PollInterval: time.Millisecond, StaleAfter: time.Second}
	holder, err := Acquire(context.Background(), path, opts)
	if err != nil {
		t.Fatal(err)
	}
	// A live holder whose lock file looks old (a slow login response, a suspended
	// laptop) must keep the lock. Breaking locks by age let two waiters, or a waiter and
	// this holder, all believe they held it.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		_, err := Acquire(ctx, path, opts)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("acquire of a live but old lock = %v, want it to wait", err)
		}
	}
	if err := holder.Release(); err != nil {
		t.Fatal(err)
	}
	next, err := Acquire(context.Background(), path, opts)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	_ = next.Release()
}

func TestAcquire_RejectsSymlinkLockPath(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "link.lock")
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if lock, err := Acquire(context.Background(), path, Options{}); err == nil {
		_ = lock.Release()
		t.Fatal("Acquire() accepted a symlink lock path")
	}
}
