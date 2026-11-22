package store

import (
	"errors"
	"testing"
)

func TestIssuesAreRecordedAndRedeemed(t *testing.T) {
	s := newTest(t)
	admin, other := addUser(t, s, "admin", RoleAdmin), addUser(t, s, "other", RoleUser)
	member, err := s.CreateUserBy(t.Context(), admin.ID,
		User{Username: "member", PasswordHash: "first", Role: RoleUser, MustChangePassword: true},
		func(User) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	set := func(change PasswordChange) {
		t.Helper()
		change.TargetID, change.Hash, change.Check = member.ID, "hash", allowPasswordChange
		if _, err := s.SetPassword(t.Context(), change); err != nil {
			t.Fatal(err)
		}
	}
	set(PasswordChange{ActorID: member.ID})                  // redeems the first
	set(PasswordChange{ActorID: member.ID})                  // an ordinary change redeems nothing
	set(PasswordChange{ActorID: admin.ID, MustChange: true}) // never redeemed
	set(PasswordChange{MustChange: true})
	set(PasswordChange{ActorID: member.ID})

	history, err := s.PasswordIssues(t.Context(), member.ID)
	if err != nil || history.Total != 3 || history.Redeemed != 2 || len(history.Latest) != 3 {
		t.Fatalf("history = %+v, %v", history, err)
	}
	for i, want := range []struct {
		kind     IssueKind
		by       string
		redeemed bool
	}{{IssueOperator, "", true}, {IssueReset, "admin", false}, {IssueCreated, "admin", true}} {
		got := history.Latest[i]
		if got.Kind != want.kind || got.By != want.by || got.At.IsZero() || got.RedeemedAt.IsZero() == want.redeemed {
			t.Errorf("entry %d = %+v, want %+v", i, got, want)
		}
	}
	for _, u := range []User{admin, other} {
		if got, err := s.PasswordIssues(t.Context(), u.ID); err != nil || got.Total != 0 || len(got.Latest) != 0 {
			t.Errorf("history of %s = %+v, %v", u.Username, got, err)
		}
	}
}

func TestNoPasswordWithoutItsRecord(t *testing.T) {
	s := newTest(t)
	admin, member := addUser(t, s, "admin", RoleAdmin), addUser(t, s, "member", RoleUser)
	if _, err := s.db.ExecContext(t.Context(),
		`CREATE TRIGGER fail_record BEFORE INSERT ON password_issues BEGIN SELECT RAISE(ABORT, 'disk unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetPassword(t.Context(), PasswordChange{
		ActorID: admin.ID, TargetID: member.ID, Hash: "issued", MustChange: true, Check: allowPasswordChange,
	}); !errors.Is(err, ErrSave) {
		t.Fatalf("reset: %v", err)
	}
	if got, err := s.UserByID(t.Context(), member.ID); err != nil || got != member {
		t.Fatalf("reset without a record: %+v, %v", got, err)
	}
	if _, err := s.CreateUserBy(t.Context(), admin.ID,
		User{Username: "newcomer", PasswordHash: "issued", Role: RoleUser, MustChangePassword: true},
		func(User) error { return nil },
	); !errors.Is(err, ErrSave) {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.UserByName(t.Context(), "newcomer"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("account without a record: %v", err)
	}
}

func TestNoPasswordChangeWithoutRedeeming(t *testing.T) {
	s := newTest(t)
	member := addUser(t, s, "member", RoleUser)
	issued, err := s.SetPassword(t.Context(), PasswordChange{TargetID: member.ID, Hash: "issued", MustChange: true, Check: allowPasswordChange})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(),
		`CREATE TRIGGER fail_redeem BEFORE UPDATE ON password_issues BEGIN SELECT RAISE(ABORT, 'disk unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetPassword(t.Context(), PasswordChange{
		ActorID: member.ID, TargetID: member.ID, Hash: "own", Check: allowPasswordChange,
	}); !errors.Is(err, ErrSave) {
		t.Fatalf("change: %v", err)
	}
	if got, err := s.UserByID(t.Context(), member.ID); err != nil || got != issued {
		t.Fatalf("password was used without a trace: %+v, %v", got, err)
	}
}
