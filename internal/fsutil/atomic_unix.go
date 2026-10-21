//go:build unix

// ABOUTME: Unix details for atomic writes: O_NOFOLLOW temp files and directory fsync.
// ABOUTME: Directory sync makes the rename itself durable across a crash or power loss.
package fsutil

import (
	"os"

	"golang.org/x/sys/unix"
)

const noFollowFlag = unix.O_NOFOLLOW

// syncDir flushes the directory entry created by a rename. Some file systems do not
// support fsync on a directory; the data file itself is already synced, so this step is
// best effort and its errors are ignored.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

func isTransientRenameError(error) bool {
	return false
}
