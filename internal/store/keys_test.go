package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func testKey(userID int64, n int) UserKey {
	return UserKey{
		ID:        fmt.Sprintf("key-%d-%d", userID, n),
		UserID:    userID,
		PublicKey: []byte{4, byte(n)},
		Salt:      bytes.Repeat([]byte{byte(n)}, 16),
		Wrapped:   bytes.Repeat([]byte{byte(n)}, 100),
	}
}

func addTestUser(t *testing.T, s *Store, u User) User {
	t.Helper()
	if u.Role == "" {
		u.Role = RoleUser
	}
	u.PasswordHash = "hash"
	created, err := s.CreateUser(t.Context(), u)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func TestInstanceIDIsRandomAndStable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.db")
	first := openTest(t, path)
	id, err := first.InstanceID(t.Context())
	if err != nil || len(id) != 16 {
		t.Fatalf("instance id = %x, %v", id, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := openTest(t, path).InstanceID(t.Context())
	if err != nil || !bytes.Equal(again, id) {
		t.Fatalf("after reopening: %x, %v, want %x", again, err, id)
	}
	other, err := openTest(t, filepath.Join(dir, "b.db")).InstanceID(t.Context())
	if err != nil || bytes.Equal(other, id) {
		t.Fatalf("second database: %x, %v", other, err)
	}
}

func TestAddKey(t *testing.T) {
	ctx := t.Context()
	s := newTest(t)
	alice := addTestUser(t, s, User{Username: "alice"})
	bob := addTestUser(t, s, User{Username: "bob"})

	if _, err := s.CurrentKey(ctx, alice.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("before: %v", err)
	}
	before := time.Now().Add(-time.Second)
	added, err := s.AddKey(ctx, testKey(alice.ID, 1), "hash")
	if err != nil {
		t.Fatal(err)
	}
	if added.CreatedAt.Before(before) || added.CreatedAt.After(time.Now()) {
		t.Fatalf("created at %v", added.CreatedAt)
	}
	got, err := s.CurrentKey(ctx, alice.ID)
	if err != nil || got.ID != added.ID || !bytes.Equal(got.Wrapped, added.Wrapped) || !bytes.Equal(got.Salt, added.Salt) ||
		!bytes.Equal(got.PublicKey, added.PublicKey) || !got.CreatedAt.Equal(added.CreatedAt) || !got.RetiredAt.IsZero() {
		t.Fatalf("current = %+v, %v, want %+v", got, err, added)
	}

	user, byName, err := s.CurrentKeyByName(ctx, "ALICE")
	if err != nil || user.ID != alice.ID || byName.ID != added.ID {
		t.Fatalf("by name = %+v, %+v, %v", user, byName, err)
	}
	if _, _, err := s.CurrentKeyByName(ctx, "bob"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("account without key: %v", err)
	}
	if _, _, err := s.CurrentKeyByName(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing account: %v", err)
	}

	if _, err := s.AddKey(ctx, testKey(alice.ID, 2), "hash"); !errors.Is(err, ErrTaken) {
		t.Fatalf("second key: %v", err)
	}
	reused := testKey(bob.ID, 3)
	reused.ID = added.ID
	if _, err := s.AddKey(ctx, reused, "hash"); !errors.Is(err, ErrTaken) {
		t.Fatalf("reused id: %v", err)
	}
	if _, err := s.AddKey(ctx, testKey(bob.ID, 4), "hash"); err != nil {
		t.Fatalf("other account: %v", err)
	}
	if _, err := s.AddKey(ctx, testKey(9999, 5), "hash"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing account: %v", err)
	}
}

func TestAddKeyIsRefusedForBannedAndIssuedAccounts(t *testing.T) {
	s := newTest(t)
	banned := addTestUser(t, s, User{Username: "banned", Banned: true})
	issued := addTestUser(t, s, User{Username: "issued", MustChangePassword: true})
	for _, u := range []User{banned, issued} {
		if _, err := s.AddKey(t.Context(), testKey(u.ID, 1), "hash"); !errors.Is(err, ErrRefused) {
			t.Errorf("%s: %v", u.Username, err)
		}
		if _, err := s.CurrentKey(t.Context(), u.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s has a key: %v", u.Username, err)
		}
	}
}

// A password change between the check of the password and the write must not leave a key wrapped with the old one.
func TestAddKeyNeedsTheCheckedPassword(t *testing.T) {
	s := newTest(t)
	alice := addTestUser(t, s, User{Username: "alice"})
	if _, err := s.AddKey(t.Context(), testKey(alice.ID, 1), "older hash"); !errors.Is(err, ErrRefused) {
		t.Fatalf("stale hash: %v", err)
	}
}

func TestParallelKeysLeaveOne(t *testing.T) {
	s := newTest(t)
	alice := addTestUser(t, s, User{Username: "alice"})
	results := make(chan error, 8)
	for n := range 8 {
		go func() {
			_, err := s.AddKey(context.Background(), testKey(alice.ID, n), "hash")
			results <- err
		}()
	}
	var added int
	for range 8 {
		switch err := <-results; {
		case err == nil:
			added++
		case !errors.Is(err, ErrTaken):
			t.Error(err)
		}
	}
	if added != 1 {
		t.Fatalf("%d keys added, want 1", added)
	}
}

// The schema itself keeps the rules, also for writes outside of the Store.
func TestKeySchemaConstraints(t *testing.T) {
	ctx := t.Context()
	s := newTest(t)
	alice := addTestUser(t, s, User{Username: "alice"})
	if _, err := s.AddKey(ctx, testKey(alice.ID, 1), "hash"); err != nil {
		t.Fatal(err)
	}
	for name, statement := range map[string]string{
		"second current key": `INSERT INTO user_keys (key_id, user_id, public_key, key_salt, wrapped, created_at)
			VALUES ('x', ?, x'04', x'00', x'00', 1)`,
		"retired key with wrapped part": `INSERT INTO user_keys (key_id, user_id, public_key, key_salt, wrapped, created_at, retired_at)
			VALUES ('y', ?, x'04', x'00', x'00', 1, 2)`,
		"current key without wrapped part": `UPDATE user_keys SET wrapped = NULL WHERE user_id = ?`,
		"retired key that keeps its salt": `INSERT INTO user_keys (key_id, user_id, public_key, key_salt, wrapped, created_at, retired_at)
			VALUES ('z', ?, x'04', x'00', NULL, 1, 2)`,
	} {
		if _, err := s.db.ExecContext(ctx, statement, alice.ID); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}

	if _, err := s.db.ExecContext(ctx, `UPDATE user_keys SET retired_at = 2, key_salt = NULL, wrapped = NULL WHERE user_id = ?`, alice.ID); err != nil {
		t.Fatalf("retiring: %v", err)
	}
	if _, err := s.AddKey(ctx, testKey(alice.ID, 2), "hash"); err != nil {
		t.Fatalf("new key after retiring: %v", err)
	}
}

func TestNewMessagesArePlain(t *testing.T) {
	s := newTest(t)
	m, err := s.AddMessage(t.Context(), Message{Kind: KindPublic, Sender: "alice", Body: "hello", CreatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	var format string
	if err := s.db.QueryRowContext(t.Context(), `SELECT format FROM messages WHERE id = ?`, m.ID).Scan(&format); err != nil || format != "plain" {
		t.Fatalf("format = %q, %v", format, err)
	}
}
