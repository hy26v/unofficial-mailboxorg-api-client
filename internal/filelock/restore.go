// ABOUTME: Puts a moved-aside token-file lock back without replacing a newer lock at the path.
// ABOUTME: Used by the token-file fallback; works where hard links are not supported.
package filelock

import (
	"errors"
	"fmt"
	"os"
)

// restoreLockWith moves another holder's lock back from aside to path, where aside is a
// name that only this process uses. It never replaces a lock that was created at path in
// the meantime, and it removes the aside file only when its content is at path again:
//
//   - A hard link (link) restores the same file. A link never replaces an existing path.
//   - Where hard links are not supported (plan9, js), path is created exclusively and gets
//     the aside file's content and modification time, so the owner's token check and the
//     stale-lock age still work.
//   - When path already holds a newer lock, or the restore fails, the aside file is kept
//     and the error names it, so another holder's lock is never deleted unproven.
func restoreLockWith(aside, path string, link func(oldname, newname string) error) error {
	linkErr := link(aside, path)
	if linkErr == nil {
		if err := os.Remove(aside); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("restored another process's lock, but could not remove %s: %w", aside, err)
		}
		return nil
	}
	if os.IsExist(linkErr) {
		return newerLockError(aside, path)
	}
	copyErr := copyLockExclusive(aside, path)
	if copyErr == nil {
		if err := os.Remove(aside); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("restored another process's lock, but could not remove %s: %w", aside, err)
		}
		return nil
	}
	if os.IsExist(copyErr) {
		return newerLockError(aside, path)
	}
	return fmt.Errorf("could not restore another process's lock to %s; it is kept at %s: %w", path, aside, errors.Join(linkErr, copyErr))
}

func newerLockError(aside, path string) error {
	return fmt.Errorf("could not restore another process's lock to %s because a newer lock is there; the earlier lock is kept at %s", path, aside)
}

// copyLockExclusive creates path only if it does not exist and gives it the content and
// modification time of aside. A partly written path is removed: this call created it.
func copyLockExclusive(aside, path string) error {
	info, err := os.Lstat(aside)
	if err != nil {
		return err
	}
	content, err := os.ReadFile(aside)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(content)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = os.Remove(path)
		return err
	}
	// Keep the original age so that a stale lock still becomes stale. A failure here only
	// delays stale breaking, so it does not undo the restore.
	_ = os.Chtimes(path, info.ModTime(), info.ModTime())
	return nil
}
