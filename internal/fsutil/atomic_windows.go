//go:build windows

// ABOUTME: Windows details for atomic writes: retry renames blocked by sharing conflicts.
// ABOUTME: Windows has no O_NOFOLLOW or directory fsync; the rename is already durable.
package fsutil

import (
	"errors"

	"golang.org/x/sys/windows"
)

const noFollowFlag = 0

func syncDir(string) {}

// isTransientRenameError reports the errors MoveFileEx returns while another process,
// often a virus scanner or search indexer, briefly holds the target or temp file open.
func isTransientRenameError(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}
