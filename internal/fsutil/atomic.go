// ABOUTME: Shared atomic file replacement for private CLI state and user output files.
// ABOUTME: Writes a synced temp file in the target directory, then renames it into place.
package fsutil

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// WriteFileAtomic replaces path with data. It writes a new file with mode perm in the same
// directory, syncs it to disk, renames it over path and then syncs the directory where
// the platform supports that. A reader therefore sees either the complete old content or
// the complete new content, never a truncated file, and a crash cannot leave path empty.
//
// The rename replaces path itself: if path is a symbolic link, the link is replaced and
// its target is not changed. Callers that must reject or follow links check path first.
// The parent directory must exist.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) (err error) {
	dir := filepath.Dir(path)
	tmp, tmpName, err := createTemp(dir, filepath.Base(path), perm)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	// The create mode is reduced by the umask; set the exact mode explicitly.
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := renameWithRetry(tmpName, path); err != nil {
		return err
	}
	syncDir(dir)
	return nil
}

// createTemp creates a new, empty file next to the target. O_EXCL and, where available,
// O_NOFOLLOW make sure the name is a fresh regular file and never an existing link.
func createTemp(dir, base string, perm os.FileMode) (*os.File, string, error) {
	for attempt := 0; attempt < 100; attempt++ {
		var raw [8]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return nil, "", fmt.Errorf("could not create a temporary file name: %w", err)
		}
		name := filepath.Join(dir, "."+base+"."+hex.EncodeToString(raw[:])+".tmp")
		file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|noFollowFlag, perm)
		if err == nil {
			return file, name, nil
		}
		if !os.IsExist(err) {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("could not create a unique temporary file in %s", dir)
}

// renameWithRetry renames, retrying briefly when the platform reports a transient
// sharing conflict (Windows: another process such as a virus scanner or indexer has the
// target open). Other errors are returned at once.
func renameWithRetry(from, to string) error {
	delay := 10 * time.Millisecond
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := os.Rename(from, to)
		if err == nil || !isTransientRenameError(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(delay)
		if delay < 200*time.Millisecond {
			delay *= 2
		}
	}
}
