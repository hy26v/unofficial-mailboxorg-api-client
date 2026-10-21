// ABOUTME: Cross-process exclusive locks on a lock file, shared by the CLI and request guard.
// ABOUTME: Kernel-backed where supported; a token-file fallback covers the remaining platforms.
package filelock

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const defaultPollInterval = 25 * time.Millisecond

// Options controls how Acquire waits for a lock.
type Options struct {
	// PollInterval is the delay between attempts while another holder has the lock.
	PollInterval time.Duration

	// StaleAfter is used only by the token-file fallback on platforms without kernel
	// locks. A lock file older than this is treated as left behind by a crashed process
	// and is removed, but only after a check that it is still the same file. Zero never
	// breaks a lock: manual cleanup is then safer than stealing live ownership. Kernel
	// locks need no age rule, because the kernel releases them when the holder exits.
	StaleAfter time.Duration
}

func (o Options) pollInterval() time.Duration {
	if o.PollInterval <= 0 {
		return defaultPollInterval
	}
	return o.PollInterval
}

// prepareLockPath creates the lock directory and refuses an existing lock path that is
// not a regular file, such as a symlink that could redirect the lock elsewhere.
func prepareLockPath(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("could not create lock directory: %w", err)
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("lock path is not a regular file: %s", path)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("could not inspect lock: %w", err)
	}
	return nil
}

// waitPoll waits one poll interval or until ctx ends.
func waitPoll(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
