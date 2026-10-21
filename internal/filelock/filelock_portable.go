//go:build aix || js || plan9 || wasip1

// ABOUTME: Token-file lock fallback for platforms without supported kernel file locks.
// ABOUTME: Stale breaking and release move the file aside and check it before deleting it.
package filelock

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"time"
)

// Lock is a held exclusive lock. Release it exactly once.
type Lock struct {
	path  string
	token string
}

// Acquire creates path exclusively and writes a random token that identifies this holder.
func Acquire(ctx context.Context, path string, opts Options) (*Lock, error) {
	if err := prepareLockPath(path); err != nil {
		return nil, err
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, fmt.Errorf("could not create lock token: %w", err)
	}
	token := hex.EncodeToString(raw[:])

	for {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if _, writeErr := io.WriteString(file, token); writeErr != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return nil, fmt.Errorf("could not write lock: %w", writeErr)
			}
			if closeErr := file.Close(); closeErr != nil {
				_ = os.Remove(path)
				return nil, fmt.Errorf("could not close lock: %w", closeErr)
			}
			return &Lock{path: path, token: token}, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("could not acquire lock: %w", err)
		}

		broken, breakErr := breakStaleLock(path, token, opts.StaleAfter, time.Now())
		if breakErr != nil {
			return nil, breakErr
		}
		if broken {
			continue
		}
		if err := waitPoll(ctx, opts.pollInterval()); err != nil {
			return nil, err
		}
	}
}

// breakStaleLock removes the lock at path when it is older than staleAfter. Two waiters
// can both find the same stale lock; the first removes it and creates its own. To keep
// the second from removing that new, live lock, the file is first renamed to a name only
// this waiter uses and then compared (same file and same token) with the one inspected.
// A different file is put back without replacing any newer lock. It reports true when
// path is now free.
func breakStaleLock(path, token string, staleAfter time.Duration, now time.Time) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("could not inspect lock: %w", err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("lock path is not a regular file: %s", path)
	}
	if staleAfter <= 0 || now.Sub(info.ModTime()) <= staleAfter {
		return false, nil
	}
	seen, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("could not read lock: %w", err)
	}

	aside := path + ".stale." + token
	if err := os.Rename(path, aside); err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, fmt.Errorf("could not recover stale lock: %w", err)
	}
	if sameLockFile(aside, info, seen) {
		_ = os.Remove(aside)
		return true, nil
	}
	return false, restoreLock(aside, path)
}

// Release removes the lock file only when it still holds this lock's token. The file is
// moved aside before the check, so a lock that another process created after breaking
// this one as stale is never deleted by a check-then-remove race.
func (l *Lock) Release() error {
	if l == nil || l.path == "" {
		return nil
	}
	aside := l.path + ".release." + l.token
	if err := os.Rename(l.path, aside); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("could not release lock: %w", err)
	}
	data, err := os.ReadFile(aside)
	if err == nil && string(data) == l.token {
		if err := os.Remove(aside); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("could not release lock: %w", err)
		}
		return nil
	}
	return restoreLock(aside, l.path)
}

func sameLockFile(path string, inspected os.FileInfo, inspectedContent []byte) bool {
	info, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, inspected) {
		return false
	}
	content, err := os.ReadFile(path)
	return err == nil && bytes.Equal(content, inspectedContent)
}

// restoreLock moves another holder's lock back from aside to path; see restoreLockWith.
func restoreLock(aside, path string) error {
	return restoreLockWith(aside, path, os.Link)
}
