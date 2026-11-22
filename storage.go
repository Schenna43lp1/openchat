// Startup checks for the data files of the Open chat application.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ensureWritable fails if a data file cannot be persisted, so a misconfigured volume
// stops the server at startup instead of losing data later. It never alters the file.
func ensureWritable(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	// The directory itself must be writable for temp files, renames and SQLite journals.
	probe, err := os.CreateTemp(dir, ".write-check-*")
	if err != nil {
		return fmt.Errorf("write to data dir %s: %w", dir, err)
	}
	if err := errors.Join(probe.Close(), os.Remove(probe.Name())); err != nil {
		return fmt.Errorf("clean up write check in %s: %w", dir, err)
	}

	// An existing file can still belong to another user.
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("write to data file: %w", err)
	}
	return file.Close()
}
