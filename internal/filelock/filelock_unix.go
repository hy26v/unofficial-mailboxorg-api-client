//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

// ABOUTME: Unix implementation of non-blocking whole-file advisory locks (flock).
// ABOUTME: Separate opens contend even within one process, matching independent CLI invocations.
package filelock

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

const noFollowFlag = unix.O_NOFOLLOW

func tryLock(file *os.File) (bool, error) {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return false, nil
	}
	return false, err
}

func unlock(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
