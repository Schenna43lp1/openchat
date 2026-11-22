package store

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"testing/fstest"
)

func openTest(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newTest(t *testing.T) *Store {
	t.Helper()
	return openTest(t, filepath.Join(t.TempDir(), "openchat.db"))
}

func schemaVersion(ctx context.Context, t *testing.T, s *Store) int {
	t.Helper()
	var version int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func TestOpenMigratesOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "openchat.db")
	for range 2 {
		s := openTest(t, path)
		if got := schemaVersion(t.Context(), t, s); got != 2 {
			t.Fatalf("schema version = %d, want 2", got)
		}
		if err := s.Ping(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
		}
	}
}

func TestNewerSchemaIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openchat.db")
	s := openTest(t, path)
	if _, err := s.db.ExecContext(t.Context(), `PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if newer, err := Open(t.Context(), path); err == nil {
		_ = newer.Close()
		t.Fatal("newer schema was accepted")
	}
}

func TestFailedMigrationIsRolledBack(t *testing.T) {
	s := newTest(t)
	files := fstest.MapFS{
		"migrations/0001_init.sql":   {Data: []byte("SELECT 1;")},
		"migrations/0002_second.sql": {Data: []byte("SELECT 1;")},
		"migrations/0003_broken.sql": {Data: []byte("CREATE TABLE half_done (id INTEGER); INSERT INTO missing VALUES (1);")},
	}
	if err := migrate(t.Context(), s.db, files); err == nil {
		t.Fatal("broken migration succeeded")
	}
	if got := schemaVersion(t.Context(), t, s); got != 2 {
		t.Fatalf("schema version = %d, want 2", got)
	}
	var tables int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM sqlite_master WHERE name = 'half_done'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("half migration stayed: %d, %v", tables, err)
	}
}

func TestDamagedDatabaseIsRefusedAndUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openchat.db")
	const damaged = "this is not a SQLite database, not at all"
	if err := os.WriteFile(path, []byte(damaged), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(t.Context(), path); err == nil {
		_ = s.Close()
		t.Fatal("damaged database was accepted")
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != damaged {
		t.Fatalf("file changed: %q, %v", raw, err)
	}
}

func TestEnsureWritableCreatesDirectoryAndLeavesNoFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "data")
	if err := ensureWritable(filepath.Join(dir, "openchat.db")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries = %v, err = %v, want an empty directory", entries, err)
	}
}

func TestEnsureWritableKeepsExistingContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openchat.db")
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureWritable(path); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "existing" {
		t.Fatalf("content = %q, err = %v", raw, err)
	}
}

func TestEnsureWritableRejectsUnusablePaths(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	readOnly := filepath.Join(dir, "read-only")
	if err := os.WriteFile(readOnly, nil, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnly, 0o600) })

	cases := map[string]string{"parent is a file": filepath.Join(file, "openchat.db"), "target is a directory": dir}
	if os.Geteuid() != 0 { // root ignores file permissions
		cases["read-only file"] = readOnly
	}
	for name, path := range cases {
		if ensureWritable(path) == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
