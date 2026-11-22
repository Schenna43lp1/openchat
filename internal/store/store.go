// Package store keeps accounts, sessions and messages in one SQLite database.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // database driver
)

// Errors that callers handle.
var (
	ErrNotFound = errors.New("not found")
	ErrTaken    = errors.New("already exists")
	ErrSave     = errors.New("save failed")
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Store is safe for concurrent use by one process.
type Store struct {
	db *sql.DB
}

// Open creates or upgrades the database at path. It fails if the location cannot be written,
// so that a misconfigured volume stops the server at startup instead of losing data later.
func Open(ctx context.Context, path string) (*Store, error) {
	if strings.Contains(path, "?") {
		return nil, fmt.Errorf("database path %q must not contain a question mark", path)
	}
	if err := ensureWritable(path); err != nil {
		return nil, err
	}
	// SQLite would create the file world-readable.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create database: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("create database: %w", err)
	}

	// In the DSN, so that they also apply if database/sql replaces the connection.
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := migrate(ctx, db, migrationFiles); err != nil {
		return nil, errors.Join(fmt.Errorf("prepare database %s: %w", path, err), db.Close())
	}
	return &Store{db: db}, nil
}

// Close releases the database file.
func (s *Store) Close() error {
	return s.db.Close()
}

// Ping reports whether the database answers.
func (s *Store) Ping(ctx context.Context) error {
	var one int
	return s.db.QueryRowContext(ctx, `SELECT 1`).Scan(&one)
}

// migrate applies the files in name order. The number of applied files is the schema version.
func migrate(ctx context.Context, db *sql.DB, files fs.FS) error {
	names, err := fs.Glob(files, "migrations/*.sql") // sorted
	if err != nil {
		return err
	}
	var version int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > len(names) {
		return fmt.Errorf("schema version %d is newer than this server (%d)", version, len(names))
	}
	for i := version; i < len(names); i++ {
		script, err := fs.ReadFile(files, names[i])
		if err != nil {
			return err
		}
		err = inTx(ctx, db, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, string(script)); err != nil {
				return err
			}
			// PRAGMA takes no placeholders.
			_, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1))
			return err
		})
		if err != nil {
			return fmt.Errorf("apply %s: %w", names[i], err)
		}
	}
	return nil
}

func inTx(ctx context.Context, db *sql.DB, run func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := run(tx); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return errors.Join(err, rollbackErr)
		}
		return err
	}
	return tx.Commit()
}

// ensureWritable never alters an existing file.
func ensureWritable(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	// The directory itself must be writable for the SQLite journal.
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

func toMillis(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UnixMilli()
}

func fromMillis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}
