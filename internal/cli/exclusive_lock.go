// ABOUTME: Kernel-backed cross-process locks for multi-request CLI transactions.
// ABOUTME: Thin wrapper over internal/filelock that never breaks a lock by its age.
package cli

import (
	"context"
	"time"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/filelock"
)

const exclusiveLockPollInterval = 25 * time.Millisecond

type exclusiveFileLock struct {
	lock *filelock.Lock
}

// acquireExclusiveFileLock waits for the lock at path. StaleAfter stays zero: on
// platforms without kernel locks a lock left by a crashed process needs manual cleanup,
// which is safer than stealing live ownership.
func acquireExclusiveFileLock(ctx context.Context, path string) (*exclusiveFileLock, error) {
	lock, err := filelock.Acquire(ctx, path, filelock.Options{PollInterval: exclusiveLockPollInterval})
	if err != nil {
		return nil, err
	}
	return &exclusiveFileLock{lock: lock}, nil
}

func (lock *exclusiveFileLock) release() error {
	if lock == nil {
		return nil
	}
	return lock.lock.Release()
}
