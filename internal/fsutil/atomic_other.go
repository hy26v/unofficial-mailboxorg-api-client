//go:build !unix && !windows

// ABOUTME: Fallback details for atomic writes on platforms without unix or Windows APIs.
// ABOUTME: Uses plain exclusive creation and rename without directory sync or retries.
package fsutil

const noFollowFlag = 0

func syncDir(string) {}

func isTransientRenameError(error) bool {
	return false
}
