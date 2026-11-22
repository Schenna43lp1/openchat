package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LegacySources are the data files of v1.0.x. A file that does not exist is skipped.
type LegacySources struct {
	Users   string // JSON, or SQLite if the extension is .db, .sqlite or .sqlite3
	History string // JSON
}

// ImportResult describes what ImportLegacy did.
type ImportResult struct {
	Imported bool
	// Ignored is set if files exist, but the database already has data of its own.
	Ignored   bool
	Users     int
	Messages  int
	Skipped   int // incomplete records, notices and unknown types, which v1.0.x did not show either
	BackupDir string
}

type legacyUser struct {
	Username     string `json:"username"`
	PasswordHash string `json:"passwordHash"`
	Role         Role   `json:"role"`
	Banned       bool   `json:"banned"`
	CreatedAt    string `json:"createdAt"`
}

type legacyMessage struct {
	Type      Kind   `json:"type"`
	Username  string `json:"username"`
	To        string `json:"to"`
	Message   string `json:"message"`
	Timestamp int64  `json:"timestamp"`
}

// ImportLegacy moves the data of v1.0.x into an empty database, once. Everything is read and checked
// before anything is written, the originals are never modified, and the import is one transaction.
func (s *Store) ImportLegacy(ctx context.Context, src LegacySources, dataDir string) (result ImportResult, err error) {
	var done, hasData bool
	if err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM legacy_import),
		        EXISTS (SELECT 1 FROM users) OR EXISTS (SELECT 1 FROM messages)`,
	).Scan(&done, &hasData); err != nil {
		return result, fmt.Errorf("check import state: %w", err)
	}
	if done {
		return result, nil
	}

	usersFound, err := regularFile(src.Users)
	if err != nil {
		return result, err
	}
	historyFound, err := regularFile(src.History)
	if err != nil {
		return result, err
	}
	if !usersFound && !historyFound {
		return result, nil
	}
	if hasData {
		result.Ignored = true
		return result, nil
	}

	var users []legacyUser
	if usersFound {
		if isSQLiteFile(src.Users) {
			users, err = readLegacySQLite(ctx, src.Users, dataDir)
		} else {
			err = readLegacyJSON(src.Users, &users)
		}
		if err != nil {
			return result, fmt.Errorf("read %s (nothing was changed): %w", src.Users, err)
		}
	}
	var messages []legacyMessage
	if historyFound {
		if err := readLegacyJSON(src.History, &messages); err != nil {
			return result, fmt.Errorf("read %s (nothing was changed): %w", src.History, err)
		}
	}

	backup, err := os.MkdirTemp(dataDir, "backup-v1.0-"+time.Now().UTC().Format("20060102-150405")+"-")
	if err != nil {
		return result, fmt.Errorf("create backup: %w", err)
	}
	defer func() {
		if err != nil {
			// A failed attempt must not pile up copies on every restart. The originals are intact.
			err = errors.Join(err, os.RemoveAll(backup))
		}
	}()
	if usersFound {
		if err := copyWithJournal(src.Users, filepath.Join(backup, "users")); err != nil {
			return result, fmt.Errorf("back up %s: %w", src.Users, err)
		}
	}
	if historyFound {
		if err := copyWithJournal(src.History, filepath.Join(backup, "history")); err != nil {
			return result, fmt.Errorf("back up %s: %w", src.History, err)
		}
	}

	var counts ImportResult
	if err := inTx(ctx, s.db, func(tx *sql.Tx) error {
		var err error
		if counts, err = writeLegacy(ctx, tx, users, messages); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO legacy_import (id, imported_at) VALUES (1, ?)`, time.Now().UnixMilli())
		return err
	}); err != nil {
		return result, fmt.Errorf("import v1.0.x data (nothing was changed): %w", err)
	}

	counts.Imported, counts.BackupDir = true, backup
	return counts, nil
}

func writeLegacy(ctx context.Context, tx *sql.Tx, users []legacyUser, messages []legacyMessage) (ImportResult, error) {
	var result ImportResult
	accounts := make(map[string]User, len(users))
	for _, old := range users {
		if old.Username == "" || old.PasswordHash == "" {
			result.Skipped++
			continue
		}
		key := strings.ToLower(old.Username)
		if _, ok := accounts[key]; ok {
			return result, fmt.Errorf("account %q exists twice", old.Username)
		}
		u := User{Username: old.Username, PasswordHash: old.PasswordHash, Role: old.Role, Banned: old.Banned}
		if !u.Role.Valid() {
			u.Role = RoleUser
		}
		if created, err := time.Parse(time.RFC3339, old.CreatedAt); err == nil {
			u.CreatedAt = created
		}
		id, err := insertUser(ctx, tx, u)
		if err != nil {
			return result, fmt.Errorf("account %q: %w", old.Username, err)
		}
		u.ID = id
		accounts[key] = u
		result.Users++
	}

	// The rule of v1.0.2: an account owns only what was sent after it was created.
	// Without a known creation time it owns nothing.
	owner := func(name string, sent int64) int64 {
		u, ok := accounts[strings.ToLower(name)]
		if !ok || u.CreatedAt.IsZero() || sent < u.CreatedAt.UnixMilli() {
			return 0
		}
		return u.ID
	}

	for _, old := range messages {
		if old.Type != KindPublic && old.Type != KindDirect {
			result.Skipped++
			continue
		}
		m := Message{
			Kind:      old.Type,
			SenderID:  owner(old.Username, old.Timestamp),
			Sender:    old.Username,
			Body:      old.Message,
			CreatedAt: fromMillis(old.Timestamp),
		}
		if m.Kind == KindDirect {
			m.RecipientID, m.Recipient = owner(old.To, old.Timestamp), old.To
		}
		if _, err := insertMessage(ctx, tx, m); err != nil {
			return result, fmt.Errorf("message: %w", err)
		}
		result.Messages++
	}
	return result, nil
}

func regularFile(path string) (bool, error) {
	if path == "" {
		return false, nil
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("%s is not a regular file", path)
	}
	return true, nil
}

func isSQLiteFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".db", ".sqlite", ".sqlite3":
		return true
	default:
		return false
	}
}

func readLegacyJSON(path string, target any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(raw)) == "" {
		return nil
	}
	return json.Unmarshal(raw, target)
}

// readLegacySQLite works on a copy, because opening a database may roll back its journal.
func readLegacySQLite(ctx context.Context, path, dataDir string) (users []legacyUser, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() == 0 {
		return nil, nil
	}
	work, err := os.MkdirTemp(dataDir, ".import-")
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(work)) }()
	if err := copyWithJournal(path, work); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", filepath.Join(work, filepath.Base(path)))
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	db.SetMaxOpenConns(1)

	var check string
	if err := db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&check); err != nil {
		return nil, err
	}
	if check != "ok" {
		return nil, fmt.Errorf("integrity check: %s", check)
	}
	rows, err := db.QueryContext(ctx, `SELECT username, password_hash, role, banned, created_at FROM users`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var u legacyUser
		var banned int
		if err := rows.Scan(&u.Username, &u.PasswordHash, &u.Role, &banned, &u.CreatedAt); err != nil {
			return nil, err
		}
		u.Banned = banned != 0
		users = append(users, u)
	}
	return users, rows.Err()
}

// copyWithJournal copies a file and the SQLite side files that belong to it.
func copyWithJournal(path, dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	base := filepath.Join(dir, filepath.Base(path))
	if err := copyFile(path, base); err != nil {
		return err
	}
	for _, suffix := range []string{"-journal", "-wal"} {
		if err := copyFile(path+suffix, base+suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func copyFile(from, to string) (err error) {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, out.Close()) }()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
