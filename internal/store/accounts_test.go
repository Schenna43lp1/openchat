package store

import (
	"database/sql"
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"testing"
	"testing/fstest"
	"time"
)

func allowPasswordChange(User, User) error { return nil }

func sendDirect(t *testing.T, s *Store, from, to User, body string) Message {
	t.Helper()
	m, err := s.AddMessage(t.Context(), Message{
		Kind: KindDirect, SenderID: from.ID, Sender: from.Username, RecipientID: to.ID, Recipient: to.Username,
		Body: body, CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func historyBodies(t *testing.T, s *Store, u User) []string {
	t.Helper()
	history, err := s.History(t.Context(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for _, m := range history {
		bodies = append(bodies, m.Body)
	}
	return bodies
}

func TestUpgradeFromSchema1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openchat.db")
	first, err := fs.ReadFile(migrationFiles, "migrations/0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrate(t.Context(), db, fstest.MapFS{"migrations/0001_init.sql": {Data: first}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `
		INSERT INTO users (username, password_hash, role, banned) VALUES
			('root', 'hash-root', 'admin', 0), ('former', 'hash-former', 'admin', 1),
			('mod', 'hash-mod', 'moderator', 0), ('alice', 'hash-alice', 'user', 0);
		INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (x'01', 4, 4102444800000);
		INSERT INTO messages (kind, sender_id, sender_name, recipient_id, recipient_name, body, created_at)
			VALUES ('direct', 1, 'root', 4, 'alice', 'kept', 1);`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s := openTest(t, path)
	if got := schemaVersion(t.Context(), t, s); got != 3 {
		t.Fatalf("schema version = %d, want 3", got)
	}
	for name, only := range map[string]bool{"root": true, "former": true, "mod": false, "alice": false} {
		u, err := s.UserByName(t.Context(), name)
		if err != nil || u.OperatorResetOnly != only || u.MustChangePassword || u.DMVisibleAfterID != 0 || u.PasswordHash != "hash-"+name {
			t.Errorf("%s = %+v, %v", name, u, err)
		}
	}
	alice, _, err := s.Session(t.Context(), []byte{1})
	if err != nil || alice.Username != "alice" {
		t.Fatalf("session = %+v, %v", alice, err)
	}
	if got := historyBodies(t, s, alice); !slices.Equal(got, []string{"kept"}) {
		t.Fatalf("history = %v", got)
	}
}

func TestAdminMarkerSurvivesDemotion(t *testing.T) {
	s := newTest(t)
	born, promoted := addUser(t, s, "born", RoleAdmin), addUser(t, s, "promoted", RoleUser)
	if !born.OperatorResetOnly || promoted.OperatorResetOnly {
		t.Fatalf("new accounts = %+v, %+v", born, promoted)
	}
	setRole := func(name string, role Role) {
		if _, _, err := s.UpdateUser(t.Context(), name, func(u *User, _ int) error {
			u.Role = role
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	setRole("promoted", RoleAdmin)
	for _, name := range []string{"born", "promoted"} {
		for _, role := range []Role{RoleModerator, RoleUser} {
			setRole(name, role)
			if u, err := s.UserByName(t.Context(), name); err != nil || !u.OperatorResetOnly {
				t.Fatalf("%s as %s = %+v, %v", name, role, u, err)
			}
		}
	}
}

func TestSetPassword(t *testing.T) {
	s := newTest(t)
	admin, alice, bob := addUser(t, s, "admin", RoleAdmin), addUser(t, s, "alice", RoleUser), addUser(t, s, "bob", RoleUser)
	for _, u := range []User{admin, alice} {
		if err := s.CreateSession(t.Context(), []byte(u.Username), u, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	sendDirect(t, s, bob, alice, "received")
	last := sendDirect(t, s, alice, bob, "sent")
	if _, err := s.AddMessage(t.Context(), Message{Kind: KindPublic, SenderID: bob.ID, Sender: "bob", Body: "public", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	updated, err := s.SetPassword(t.Context(), PasswordChange{
		ActorID: admin.ID, TargetID: alice.ID, Hash: "issued", MustChange: true, HideDirectMessages: true,
		Check: func(actor, target User) error {
			if actor != admin || target != alice {
				t.Errorf("check got %+v and %+v", actor, target)
			}
			return nil
		},
	})
	if err != nil || updated.PasswordHash != "issued" || !updated.MustChangePassword || updated.DMVisibleAfterID < last.ID {
		t.Fatalf("updated = %+v, %v", updated, err)
	}
	if stored, err := s.UserByID(t.Context(), alice.ID); err != nil || stored != updated {
		t.Fatalf("stored = %+v, %v", stored, err)
	}
	if _, _, err := s.Session(t.Context(), []byte("alice")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session survived: %v", err)
	}
	if _, _, err := s.Session(t.Context(), []byte("admin")); err != nil {
		t.Fatalf("session of the actor ended: %v", err)
	}

	sendDirect(t, s, bob, alice, "new")
	if got := historyBodies(t, s, alice); !slices.Equal(got, []string{"public", "new"}) {
		t.Fatalf("history of the target = %v", got)
	}
	if got := historyBodies(t, s, bob); !slices.Equal(got, []string{"received", "sent", "public", "new"}) {
		t.Fatalf("history of the other side = %v", got)
	}

	// Choosing an own password must not bring the hidden messages back.
	changed, err := s.SetPassword(t.Context(), PasswordChange{ActorID: alice.ID, TargetID: alice.ID, Hash: "own", Check: allowPasswordChange})
	if err != nil || changed.MustChangePassword || changed.DMVisibleAfterID != updated.DMVisibleAfterID {
		t.Fatalf("own change = %+v, %v", changed, err)
	}
}

func TestHidingWithoutAnyMessages(t *testing.T) {
	s := newTest(t)
	alice := addUser(t, s, "alice", RoleUser)
	updated, err := s.SetPassword(t.Context(), PasswordChange{TargetID: alice.ID, Hash: "new", HideDirectMessages: true, Check: allowPasswordChange})
	if err != nil || updated.DMVisibleAfterID != 0 {
		t.Fatalf("updated = %+v, %v", updated, err)
	}
}

func TestSetPasswordFailuresChangeNothing(t *testing.T) {
	refusal := errors.New("refused")
	for name, tc := range map[string]struct {
		actor   int64
		missing bool
		refuse  bool
		broken  bool
		want    error
	}{
		"refused":    {refuse: true, want: refusal},
		"no target":  {missing: true, want: ErrNotFound},
		"no actor":   {actor: 999, want: ErrNotFound},
		"disk error": {broken: true, want: ErrSave},
	} {
		t.Run(name, func(t *testing.T) {
			s := newTest(t)
			alice := addUser(t, s, "alice", RoleUser)
			change := PasswordChange{ActorID: tc.actor, TargetID: alice.ID, Hash: "must not be stored", Check: allowPasswordChange}
			if tc.missing {
				change.TargetID = 999
			}
			if tc.refuse {
				change.Check = func(User, User) error { return refusal }
			}
			if tc.broken {
				// The second statement fails, so the first must not stick.
				if err := s.CreateSession(t.Context(), []byte("alice"), alice, time.Now().Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.ExecContext(t.Context(),
					`CREATE TRIGGER fail_delete BEFORE DELETE ON sessions BEGIN SELECT RAISE(ABORT, 'disk unavailable'); END`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.SetPassword(t.Context(), change); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if u, err := s.UserByID(t.Context(), alice.ID); err != nil || u != alice {
				t.Fatalf("account = %+v, %v", u, err)
			}
		})
	}
}

func TestCreateUserByChecksTheActor(t *testing.T) {
	s := newTest(t)
	admin := addUser(t, s, "admin", RoleAdmin)
	refusal := errors.New("refused")
	if _, err := s.CreateUserBy(t.Context(), admin.ID, User{Username: "new", PasswordHash: "hash", Role: RoleUser}, func(actor User) error {
		if actor != admin {
			t.Errorf("actor = %+v", actor)
		}
		return refusal
	}); !errors.Is(err, refusal) {
		t.Fatalf("error = %v", err)
	}
	if _, err := s.UserByName(t.Context(), "new"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("refused account was stored: %v", err)
	}
}
