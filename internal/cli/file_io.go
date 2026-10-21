// ABOUTME: Shared local-file writes for downloaded and exported user data.
// ABOUTME: Output files are replaced atomically with owner-only permissions; links are refused.
package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/fsutil"
)

// pathsReferToSameFile catches both equivalent path spellings and existing hard links.
// Symlink outputs are rejected by writePrivateFile itself, but resolving them here gives
// callers an earlier and more specific collision error where possible.
func pathsReferToSameFile(first, second string) bool {
	firstAbs, firstErr := filepath.Abs(first)
	secondAbs, secondErr := filepath.Abs(second)
	if firstErr == nil && secondErr == nil && filepath.Clean(firstAbs) == filepath.Clean(secondAbs) {
		return true
	}
	firstInfo, firstStatErr := os.Stat(first)
	secondInfo, secondStatErr := os.Stat(second)
	return firstStatErr == nil && secondStatErr == nil && os.SameFile(firstInfo, secondInfo)
}

// writePrivateFile writes a downloaded/exported document with owner-only permissions.
// It replaces the destination atomically: the data goes to a synced temporary file in the
// same directory, which is then renamed over the destination. An interrupted or failed
// write therefore never leaves a truncated or partly written file, and an existing 0644
// file is replaced by a 0600 one. An existing destination that is not a regular file
// (for example a symlink) is rejected. If a link appears after that check, the rename
// replaces the link itself and never writes through it.
func writePrivateFile(path string, data []byte) error {
	if info, statErr := os.Lstat(path); statErr == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("output path is not a regular file: %s", path)
		}
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	return fsutil.WriteFileAtomic(path, data, 0o600)
}

// openPrivateAppendFile opens a regular owner-only file for durable append-only records.
// It is used for resumable checkpoints, where truncating an existing file would destroy
// the evidence needed to avoid replaying already-completed mutations.
func openPrivateAppendFile(path string) (*os.File, error) {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("checkpoint path is not a regular file: %s", path)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("checkpoint path is not a regular file: %s", path)
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}
