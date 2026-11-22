package auth

import (
	"errors"
	"testing"

	"github.com/Schenna43lp1/openchat/internal/store"
)

func TestBanDuringLoginLeavesNoSession(t *testing.T) {
	s, st := newTestService(t)
	seed(t, st, "victim", store.RoleUser, false)
	compare := s.compare
	s.compare = func(hash, password []byte) error {
		// After the account was read, before the session is written.
		if _, err := s.ToggleBan(t.Context(), operator, "victim"); err != nil {
			t.Fatal(err)
		}
		return compare(hash, password)
	}
	user, err := s.Authenticate(t.Context(), "victim", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := s.StartSession(t.Context(), user)
	if !errors.Is(err, ErrInvalidCredentials) || token != "" {
		t.Fatalf("session after a ban: created = %t, error = %v", token != "", err)
	}
}

func TestOutdatedPasswordStartsNoSession(t *testing.T) {
	s, st := newTestService(t)
	user := seed(t, st, "alice", store.RoleUser, false)
	user.PasswordHash = "hash from before a password change"
	token, _, err := s.StartSession(t.Context(), user)
	if !errors.Is(err, ErrInvalidCredentials) || token != "" {
		t.Fatalf("session for an old password: created = %t, error = %v", token != "", err)
	}
}
