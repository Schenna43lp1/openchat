package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const legacyCreated = "2026-01-02T03:04:05Z"

func createdMillis(t *testing.T) int64 {
	t.Helper()
	created, err := time.Parse(time.RFC3339, legacyCreated)
	if err != nil {
		t.Fatal(err)
	}
	return created.UnixMilli()
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeLegacyUsers writes the format that v1.0.x would choose for the path.
func writeLegacyUsers(t *testing.T, path string, users []legacyUser) {
	t.Helper()
	if !isSQLiteFile(path) {
		writeJSON(t, path, users)
		return
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(t.Context(), `CREATE TABLE IF NOT EXISTS users (
		username TEXT PRIMARY KEY COLLATE NOCASE,
		password_hash TEXT NOT NULL,
		role TEXT NOT NULL,
		banned INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL
	)`)
	for _, u := range users {
		if err != nil {
			break
		}
		_, err = db.ExecContext(t.Context(), `INSERT INTO users VALUES (?, ?, ?, ?, ?)`, u.Username, u.PasswordHash, string(u.Role), u.Banned, u.CreatedAt)
	}
	if err := errors.Join(err, db.Close()); err != nil {
		t.Fatal(err)
	}
}

func fileContents(t *testing.T, paths ...string) string {
	t.Helper()
	var all strings.Builder
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		all.Write(raw)
	}
	return all.String()
}

func TestImportLegacy(t *testing.T) {
	for _, usersFile := range []string{"users.json", "users.sqlite"} {
		t.Run(usersFile, func(t *testing.T) {
			dir := t.TempDir()
			src := LegacySources{Users: filepath.Join(dir, usersFile), History: filepath.Join(dir, "chat-history.json")}
			created := createdMillis(t)
			writeLegacyUsers(t, src.Users, []legacyUser{
				{"Root", "hash-root", RoleAdmin, false, legacyCreated},
				{"alice", "hash-alice", RoleModerator, false, legacyCreated},
				{"blocked", "hash-blocked", RoleUser, true, legacyCreated},
				{"undated", "hash-undated", "superuser", false, "not a date"},
				{"nohash", "", RoleUser, false, legacyCreated},
			})
			writeJSON(t, src.History, []legacyMessage{
				{KindPublic, "alice", "", "public", created + 1},
				{KindDirect, "alice", "root", "private", created + 2},
				{KindDirect, "alice", "root", "before the accounts", created - 1000},
				{KindDirect, "root", "ghost", "to nobody", created + 3},
				{KindDirect, "root", "undated", "to undated", created + 4},
				{KindPublic, "gone", "", "from a deleted account", 0},
				{"system", "", "", "alice hat den Chat betreten.", created},
				{"unknown", "", "", "unknown type", created},
			})
			original := fileContents(t, src.Users, src.History)

			path := filepath.Join(dir, "openchat.db")
			s := openTest(t, path)
			result, err := s.ImportLegacy(t.Context(), src, dir)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Imported || result.Users != 4 || result.Messages != 6 || result.Skipped != 3 {
				t.Fatalf("result = %+v", result)
			}
			if fileContents(t, src.Users, src.History) != original {
				t.Fatal("originals changed")
			}
			backup := fileContents(t, filepath.Join(result.BackupDir, "users", usersFile), filepath.Join(result.BackupDir, "history", "chat-history.json"))
			if backup != original {
				t.Fatal("backup differs from the originals")
			}

			users := map[string]User{}
			all, err := s.Users(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			for _, u := range all {
				users[u.Username] = u
			}
			if u := users["Root"]; u.Role != RoleAdmin || !u.OperatorResetOnly || u.PasswordHash != "hash-root" || u.CreatedAt.UnixMilli() != created {
				t.Errorf("Root = %+v", u)
			}
			if u := users["alice"]; u.Role != RoleModerator {
				t.Errorf("alice = %+v", u)
			}
			if u := users["blocked"]; !u.Banned {
				t.Errorf("blocked = %+v", u)
			}
			if u := users["undated"]; u.Role != RoleUser || !u.CreatedAt.IsZero() {
				t.Errorf("undated = %+v", u)
			}

			for name, want := range map[string]string{
				"Root":    "[public private to nobody to undated from a deleted account]",
				"alice":   "[public private from a deleted account]",
				"undated": "[public from a deleted account]",
			} {
				if got := fmt.Sprint(bodies(t, s, users[name].ID)); got != want {
					t.Errorf("%s reads %s, want %s", name, got, want)
				}
			}
			// Deleting old direct messages is up to the operator, so the hidden one is still stored.
			var stored int
			if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM messages`).Scan(&stored); err != nil || stored != 6 {
				t.Fatalf("stored = %d, %v", stored, err)
			}
			late := addUser(t, s, "ghost", RoleUser)
			if got := bodies(t, s, late.ID); strings.Contains(fmt.Sprint(got), "to nobody") {
				t.Fatal("a new account inherited a message")
			}

			// Once only, also after a restart.
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s = openTest(t, path)
			if again, err := s.ImportLegacy(t.Context(), src, dir); err != nil || again.Imported || again.Ignored {
				t.Fatalf("second import = %+v, %v", again, err)
			}
		})
	}
}

func TestImportAbortsOnDamagedSources(t *testing.T) {
	account := `{"username":"alice","passwordHash":"x"}`
	for _, tc := range []struct{ name, usersFile, users, history string }{
		{"users JSON", "users.json", `[{"username":"alice",`, `[]`},
		{"history JSON", "users.json", `[` + account + `]`, `[{"type":"message"`},
		{"history is an object", "users.json", `[]`, `{"type":"message"}`},
		{"users SQLite", "users.sqlite", "this is not a database, not at all", `[]`},
		{"account twice", "users.json", `[` + account + `,{"username":"ALICE","passwordHash":"y"}]`, `[]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			src := LegacySources{Users: filepath.Join(dir, tc.usersFile), History: filepath.Join(dir, "chat-history.json")}
			for path, content := range map[string]string{src.Users: tc.users, src.History: tc.history} {
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			s := openTest(t, filepath.Join(dir, "openchat.db"))
			if _, err := s.ImportLegacy(t.Context(), src, dir); err == nil {
				t.Fatal("damaged source was accepted")
			}
			if fileContents(t, src.Users, src.History) != tc.users+tc.history {
				t.Fatal("originals changed")
			}
			if users, err := s.Users(t.Context()); err != nil || len(users) != 0 {
				t.Fatalf("partial import: %v, %v", users, err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 3 {
				t.Fatalf("leftovers: %v, %v", entries, err)
			}

			// The repaired file is imported on the next start.
			if err := os.Remove(src.Users); err != nil {
				t.Fatal(err)
			}
			writeLegacyUsers(t, src.Users, []legacyUser{{"alice", "x", RoleUser, false, legacyCreated}})
			writeJSON(t, src.History, []legacyMessage{})
			if result, err := s.ImportLegacy(t.Context(), src, dir); err != nil || !result.Imported {
				t.Fatalf("retry = %+v, %v", result, err)
			}
		})
	}
}

func TestIncompatibleLegacySchemaIsRejected(t *testing.T) {
	for name, setup := range map[string]string{
		"missing columns": `CREATE TABLE users (username TEXT)`,
		"null value": `CREATE TABLE users (username TEXT, password_hash TEXT, role TEXT, banned INTEGER, created_at TEXT);
			INSERT INTO users VALUES ('alice', 'hash', 'user', 0, NULL)`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "users.sqlite")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			_, execErr := db.ExecContext(t.Context(), setup)
			if err := errors.Join(execErr, db.Close()); err != nil {
				t.Fatal(err)
			}
			if _, err := newTest(t).ImportLegacy(t.Context(), LegacySources{Users: path}, dir); err == nil {
				t.Fatal("incompatible schema was accepted")
			}
		})
	}
}

func TestImportEdgeCases(t *testing.T) {
	dir := t.TempDir()
	src := LegacySources{Users: filepath.Join(dir, "users.json"), History: filepath.Join(dir, "chat-history.json")}
	s := newTest(t)
	if result, err := s.ImportLegacy(t.Context(), src, dir); err != nil || result.Imported || result.Ignored {
		t.Fatalf("no files = %+v, %v", result, err)
	}
	if err := os.WriteFile(src.Users, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if result, err := newTest(t).ImportLegacy(t.Context(), src, dir); err != nil || result.Users != 0 {
		t.Fatalf("empty file = %+v, %v", result, err)
	}
	addUser(t, s, "existing", RoleAdmin)
	if result, err := s.ImportLegacy(t.Context(), src, dir); err != nil || !result.Ignored || result.Imported {
		t.Fatalf("database with data = %+v, %v", result, err)
	}
	if _, err := newTest(t).ImportLegacy(t.Context(), LegacySources{Users: dir}, dir); err == nil {
		t.Fatal("directory was accepted")
	}
}
