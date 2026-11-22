package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func addUser(t *testing.T, s *Store, name string, role Role) User {
	t.Helper()
	u, err := s.CreateUser(t.Context(), User{Username: name, PasswordHash: "hash", Role: role, CreatedAt: time.UnixMilli(1_767_323_045_000)})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestUsers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openchat.db")
	s := openTest(t, path)
	bob := addUser(t, s, "bob", RoleUser)
	alice := addUser(t, s, "Alice", RoleAdmin)

	if _, err := s.CreateUser(t.Context(), User{Username: "ALICE", PasswordHash: "x", Role: RoleUser}); !errors.Is(err, ErrTaken) {
		t.Fatalf("duplicate: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTest(t, path)
	if got, err := s.UserByName(t.Context(), "aLiCe"); err != nil || got != alice {
		t.Fatalf("Alice = %+v, %v", got, err)
	}
	if _, err := s.UserByName(t.Context(), "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	if got, err := s.Users(t.Context()); err != nil || len(got) != 2 || got[0] != alice || got[1] != bob {
		t.Fatalf("users = %+v, %v", got, err)
	}
	if n, err := s.ActiveAdmins(t.Context()); err != nil || n != 1 {
		t.Fatalf("active admins = %d, %v", n, err)
	}
}

func TestUserIDsAreNeverReused(t *testing.T) {
	s := newTest(t)
	old := addUser(t, s, "bob", RoleUser)
	if _, err := s.db.ExecContext(t.Context(), `DELETE FROM users WHERE id = ?`, old.ID); err != nil {
		t.Fatal(err)
	}
	if again := addUser(t, s, "bob", RoleUser); again.ID == old.ID {
		t.Fatal("ID was reused")
	}
}

func TestUpdateUser(t *testing.T) {
	s := newTest(t)
	bob, carol := addUser(t, s, "bob", RoleUser), addUser(t, s, "carol", RoleUser)
	for _, u := range []User{bob, carol} {
		if err := s.CreateSession(t.Context(), []byte(u.Username), u.ID, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}

	refusal := errors.New("refused")
	_, _, err := s.UpdateUser(t.Context(), "bob", func(u *User, _ int) error {
		u.Banned = true
		return refusal
	})
	if !errors.Is(err, refusal) || errors.Is(err, ErrSave) {
		t.Fatalf("refusal = %v", err)
	}
	if _, _, err := s.UpdateUser(t.Context(), "ghost", func(*User, int) error { return nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}

	before, after, err := s.UpdateUser(t.Context(), "BOB", func(u *User, _ int) error {
		u.Banned, u.Role = true, RoleModerator
		return nil
	})
	if err != nil || before != bob || !after.Banned || after.Role != RoleModerator {
		t.Fatalf("update = %+v, %+v, %v", before, after, err)
	}
	if _, _, err := s.Session(t.Context(), []byte("bob")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ban kept the session: %v", err)
	}
	if _, _, err := s.Session(t.Context(), []byte("carol")); err != nil {
		t.Fatalf("ban ended a foreign session: %v", err)
	}
}

func TestFailedWriteRollsBack(t *testing.T) {
	s := newTest(t)
	bob := addUser(t, s, "bob", RoleUser)
	if err := s.CreateSession(t.Context(), []byte("token"), bob.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(),
		`CREATE TRIGGER fail_write BEFORE DELETE ON sessions BEGIN SELECT RAISE(ABORT, 'disk unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	// The second statement of the transaction fails, so the first must not stick.
	_, _, err := s.UpdateUser(t.Context(), "bob", func(u *User, _ int) error {
		u.Banned = true
		return nil
	})
	if !errors.Is(err, ErrSave) {
		t.Fatalf("error = %v", err)
	}
	if got, err := s.UserByName(t.Context(), "bob"); err != nil || got.Banned {
		t.Fatalf("failed ban stayed: %+v, %v", got, err)
	}
}

func TestSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openchat.db")
	s := openTest(t, path)
	bob := addUser(t, s, "bob", RoleUser)
	now := time.UnixMilli(time.Now().UnixMilli())
	for token, expires := range map[string]time.Time{"live": now.Add(time.Hour), "old": now.Add(-time.Second)} {
		if err := s.CreateSession(t.Context(), []byte(token), bob.ID, expires); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateSession(t.Context(), []byte("orphan"), 999, now); !errors.Is(err, ErrSave) {
		t.Fatalf("session without an account: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s = openTest(t, path)
	if u, expires, err := s.Session(t.Context(), []byte("live")); err != nil || u != bob || !expires.Equal(now.Add(time.Hour)) {
		t.Fatalf("after reopen = %+v, %v, %v", u, expires, err)
	}
	if n, err := s.DeleteExpiredSessions(t.Context(), now); err != nil || n != 1 {
		t.Fatalf("cleanup = %d, %v", n, err)
	}
	if err := s.DeleteSession(t.Context(), []byte("live")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Session(t.Context(), []byte("live")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted session: %v", err)
	}
}

func addMessage(t *testing.T, s *Store, m Message) Message {
	t.Helper()
	m.CreatedAt = time.UnixMilli(time.Now().UnixMilli())
	saved, err := s.AddMessage(t.Context(), m)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func bodies(t *testing.T, s *Store, userID int64) []string {
	t.Helper()
	history, err := s.History(t.Context(), userID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range history {
		out = append(out, m.Body)
	}
	return out
}

func TestHistoryVisibility(t *testing.T) {
	s := newTest(t)
	alice, bob, eve := addUser(t, s, "alice", RoleUser), addUser(t, s, "bob", RoleUser), addUser(t, s, "eve", RoleUser)
	addMessage(t, s, Message{Kind: KindPublic, SenderID: alice.ID, Sender: "alice", Body: "public"})
	addMessage(t, s, Message{Kind: KindDirect, SenderID: alice.ID, Sender: "alice", RecipientID: bob.ID, Recipient: "bob", Body: "private"})
	// Same names, but no account may claim it.
	addMessage(t, s, Message{Kind: KindDirect, Sender: "eve", Recipient: "bob", Body: "orphan"})

	for id, want := range map[int64]string{alice.ID: "[public private]", bob.ID: "[public private]", eve.ID: "[public]", 0: "[public]"} {
		if got := fmt.Sprint(bodies(t, s, id)); got != want {
			t.Errorf("user %d reads %s, want %s", id, got, want)
		}
	}
}

func TestRetention(t *testing.T) {
	s := newTest(t)
	alice, bob := addUser(t, s, "alice", RoleUser), addUser(t, s, "bob", RoleUser)
	addMessage(t, s, Message{Kind: KindPublic, SenderID: bob.ID, Sender: "bob", Body: "oldest public"})
	addMessage(t, s, Message{Kind: KindDirect, SenderID: bob.ID, Sender: "bob", RecipientID: alice.ID, Recipient: "alice", Body: "kept"})
	for i := range HistoryLimit + 1 {
		addMessage(t, s, Message{Kind: KindDirect, SenderID: alice.ID, Sender: "alice", RecipientID: alice.ID, Recipient: "alice", Body: fmt.Sprint("direct ", i)})
	}

	if got := bodies(t, s, bob.ID); fmt.Sprint(got) != "[oldest public kept]" {
		t.Fatalf("flood of another account displaced messages: %v", got)
	}
	var fromAlice int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM messages WHERE sender_id = ?`, alice.ID).Scan(&fromAlice); err != nil || fromAlice != HistoryLimit {
		t.Fatalf("direct messages kept = %d, %v", fromAlice, err)
	}

	for i := range HistoryLimit {
		addMessage(t, s, Message{Kind: KindPublic, SenderID: alice.ID, Sender: "alice", Body: fmt.Sprint("public ", i)})
	}
	if got := bodies(t, s, bob.ID); len(got) != HistoryLimit+1 || got[0] != "kept" || got[1] != "public 0" {
		t.Fatalf("after trimming: %d messages, starting %v", len(got), got[:2])
	}
}
