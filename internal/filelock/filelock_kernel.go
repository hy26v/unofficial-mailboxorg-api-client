//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris || windows

// ABOUTME: Kernel-backed lock: flock on unix, LockFileEx on Windows, held by an open handle.
// ABOUTME: Process exit releases it, so there is no stale-lock guessing and no break-in race.
package filelock

import (
	"context"
	"fmt"
	"os"
)

// Lock is a held exclusive lock. Release it exactly once.
type Lock struct {
	file *os.File
}

// Acquire waits until it holds the exclusive lock on path or ctx ends. The lock file is
// created when missing and is never removed, so every holder locks the same file.
func Acquire(ctx context.Context, path string, opts Options) (*Lock, error) {
	if err := prepareLockPath(path); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|noFollowFlag, 0o600)
	if err != nil {
		return nil, fmt.Errorf("could not open lock: %w", err)
	}
	closeWithError := func(lockErr error) (*Lock, error) {
		if closeErr := file.Close(); lockErr == nil {
			lockErr = closeErr
		}
		return nil, lockErr
	}
	info, err := file.Stat()
	if err != nil {
		return closeWithError(fmt.Errorf("could not inspect opened lock: %w", err))
	}
	if !info.Mode().IsRegular() {
		return closeWithError(fmt.Errorf("lock path is not a regular file: %s", path))
	}
	if err := file.Chmod(0o600); err != nil {
		return closeWithError(fmt.Errorf("could not protect lock file: %w", err))
	}

	for {
		acquired, lockErr := tryLock(file)
		if lockErr != nil {
			return closeWithError(fmt.Errorf("could not acquire lock: %w", lockErr))
		}
		if acquired {
			return &Lock{file: file}, nil
		}
		if err := waitPoll(ctx, opts.pollInterval()); err != nil {
			return closeWithError(err)
		}
	}
}

// Release unlocks and closes the lock handle. The lock file stays in place, so release
// never deletes a file that another process may hold.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := unlock(l.file)
	closeErr := l.file.Close()
	l.file = nil
	if unlockErr != nil {
		return fmt.Errorf("could not release lock: %w", unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("could not close lock: %w", closeErr)
	}
	return nil
}
