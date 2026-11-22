package auth

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Schenna43lp1/openchat/internal/store"
)

const newPassword = "my-own-new-password"

func account(t *testing.T, s *Service, id int64) store.User {
	t.Helper()
	u, err := s.FindID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func reset(t *testing.T, s *Service, admin, target store.User) Issued {
	t.Helper()
	issued, err := s.ResetPassword(t.Context(), admin, testPassword, target.ID, ResetState(target))
	if err != nil {
		t.Fatal(err)
	}
	return issued
}

func TestChangePassword(t *testing.T) {
	s, st := newTestService(t)
	alice := seed(t, st, "alice", store.RoleUser, false)
	token, _, err := s.StartSession(t.Context(), alice)
	if err != nil {
		t.Fatal(err)
	}
	watch := s.Watch(token)
	if ok, err := watch(t.Context()); err != nil || !ok {
		t.Fatalf("setup = %t, %v", ok, err)
	}

	changed, err := s.ChangePassword(t.Context(), alice, testPassword, newPassword)
	if err != nil || changed.MustChangePassword || changed != account(t, s, alice.ID) {
		t.Fatalf("changed = %+v, %v", changed, err)
	}
	if _, err := s.Session(t.Context(), token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("session survived: %v", err)
	}
	if ok, err := watch(t.Context()); err != nil || ok {
		t.Fatalf("connection stays authorized: %t, %v", ok, err)
	}
	if _, err := s.Authenticate(t.Context(), "alice", testPassword); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old password: %v", err)
	}
	if _, err := s.Authenticate(t.Context(), "alice", newPassword); err != nil {
		t.Fatalf("new password: %v", err)
	}
	if _, _, err := s.StartSession(t.Context(), changed); err != nil {
		t.Fatalf("session for the new password: %v", err)
	}
}

func TestChangePasswordRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		current, next string
		want          error
	}{
		"wrong password": {"wrong-password", newPassword, ErrWrongPassword},
		"same password":  {testPassword, testPassword, ErrSamePassword},
		"too short":      {testPassword, "short", ErrInvalidPassword},
		"too long":       {testPassword, strings.Repeat("ü", 37), ErrInvalidPassword},
	} {
		t.Run(name, func(t *testing.T) {
			s, st := newTestService(t)
			alice := seed(t, st, "alice", store.RoleUser, false)
			if _, err := s.ChangePassword(t.Context(), alice, tc.current, tc.next); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if account(t, s, alice.ID) != alice {
				t.Fatal("refused change was stored")
			}
		})
	}
}

func TestChangePasswordLosesAgainstReset(t *testing.T) {
	s, st := newTestService(t)
	admin, alice := seed(t, st, "admin", store.RoleAdmin, false), seed(t, st, "alice", store.RoleUser, false)
	compare := s.compare
	var issued Issued
	s.compare = func(hash, password []byte) error {
		s.compare = compare
		issued = reset(t, s, admin, alice)
		return compare(hash, password)
	}
	if _, err := s.ChangePassword(t.Context(), alice, testPassword, newPassword); !errors.Is(err, ErrAccountChanged) {
		t.Fatalf("error = %v", err)
	}
	if account(t, s, alice.ID) != issued.User {
		t.Fatal("change overwrote the reset")
	}
}

func TestCreateAccount(t *testing.T) {
	s, st := newTestService(t)
	admin := seed(t, st, "admin", store.RoleAdmin, false)
	issued, err := s.CreateAccount(t.Context(), admin, testPassword, " Newcomer ")
	if err != nil {
		t.Fatal(err)
	}
	u := issued.User
	if u.Username != "Newcomer" || u.Role != store.RoleUser || !u.MustChangePassword || u.OperatorResetOnly || u != account(t, s, u.ID) {
		t.Fatalf("account = %+v", u)
	}
	if len(issued.Password) < 26 {
		t.Fatalf("issued password has %d characters", len(issued.Password))
	}
	signedIn, err := s.Authenticate(t.Context(), "newcomer", issued.Password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChangePassword(t.Context(), signedIn, issued.Password, issued.Password); !errors.Is(err, ErrSamePassword) {
		t.Fatalf("issued password could be kept: %v", err)
	}
	changed, err := s.ChangePassword(t.Context(), signedIn, issued.Password, newPassword)
	if err != nil || changed.MustChangePassword {
		t.Fatalf("changed = %+v, %v", changed, err)
	}
}

func TestCreateAccountRefusals(t *testing.T) {
	s, st := newTestService(t)
	admin := seed(t, st, "admin", store.RoleAdmin, false)
	moderator := seed(t, st, "moderator", store.RoleModerator, false)
	user := seed(t, st, "user", store.RoleUser, false)
	before := list(t, s)
	for name, tc := range map[string]struct {
		actor              store.User
		password, username string
		want               error
	}{
		"moderator":      {moderator, testPassword, "newcomer", ErrForbidden},
		"user":           {user, testPassword, "newcomer", ErrForbidden},
		"wrong password": {admin, "wrong-password", "newcomer", ErrWrongPassword},
		"taken":          {admin, testPassword, "USER", ErrUsernameTaken},
		"invalid name":   {admin, testPassword, "no", ErrInvalidUsername},
	} {
		if _, err := s.CreateAccount(t.Context(), tc.actor, tc.password, tc.username); !errors.Is(err, tc.want) {
			t.Errorf("%s: error = %v, want %v", name, err, tc.want)
		}
	}
	if got := list(t, s); len(got) != len(before) {
		t.Fatalf("accounts = %+v", got)
	}
}

func TestResetPermissions(t *testing.T) {
	type kind struct {
		role   store.Role
		former bool // was admin
		banned bool
	}
	targets := map[string]kind{
		"user":             {role: store.RoleUser},
		"moderator":        {role: store.RoleModerator},
		"banned user":      {role: store.RoleUser, banned: true},
		"admin":            {role: store.RoleAdmin},
		"banned admin":     {role: store.RoleAdmin, banned: true},
		"former admin":     {role: store.RoleUser, former: true},
		"former, promoted": {role: store.RoleModerator, former: true},
		"self":             {},
	}
	for _, role := range []store.Role{store.RoleAdmin, store.RoleModerator, store.RoleUser} {
		for name, k := range targets {
			t.Run(string(role)+"/"+name, func(t *testing.T) {
				s, st := newTestService(t)
				seed(t, st, "keeper", store.RoleAdmin, false)
				actor := seed(t, st, "actor", role, false)
				target := actor
				if name != "self" {
					initial := k.role
					if k.former {
						initial = store.RoleAdmin
					}
					target = seed(t, st, "target", initial, k.banned)
					if k.former {
						if err := s.SetRole(t.Context(), operator, "target", k.role); err != nil {
							t.Fatal(err)
						}
						target = account(t, s, target.ID)
					}
				}

				var want error
				switch {
				case role != store.RoleAdmin:
					want = ErrForbidden
				case name == "self" || k.former || k.role == store.RoleAdmin:
					want = ErrOperatorOnly
				}
				issued, err := s.ResetPassword(t.Context(), actor, testPassword, target.ID, ResetState(target))
				if !errors.Is(err, want) {
					t.Fatalf("error = %v, want %v", err, want)
				}
				got := account(t, s, target.ID)
				if want != nil {
					if got != target || issued.Password != "" {
						t.Fatalf("refused reset had an effect: %+v", got)
					}
					return
				}
				if got != issued.User || !got.MustChangePassword || got.Banned != k.banned || got.PasswordHash == target.PasswordHash {
					t.Fatalf("account = %+v", got)
				}
			})
		}
	}
}

func TestDemotingAnAdminDoesNotAllowAReset(t *testing.T) {
	s, st := newTestService(t)
	attacker, victim := seed(t, st, "attacker", store.RoleAdmin, false), seed(t, st, "victim", store.RoleUser, false)
	for _, role := range []store.Role{store.RoleAdmin, store.RoleUser} {
		if err := s.SetRole(t.Context(), attacker, "victim", role); err != nil {
			t.Fatal(err)
		}
	}
	demoted := account(t, s, victim.ID)
	if _, err := s.ResetPassword(t.Context(), attacker, testPassword, victim.ID, ResetState(demoted)); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("error = %v", err)
	}
}

// Everything that may happen while the password of the admin is compared.
func TestResetChecksAgainInsideTheTransaction(t *testing.T) {
	for name, tc := range map[string]struct {
		meanwhile func(ctx context.Context, s *Service, admin store.User) error
		want      error
	}{
		"admin demoted": {func(ctx context.Context, s *Service, _ store.User) error {
			return s.SetRole(ctx, operator, "admin", store.RoleUser)
		}, ErrForbidden},
		"admin banned": {func(ctx context.Context, s *Service, _ store.User) error {
			_, err := s.ToggleBan(ctx, operator, "admin")
			return err
		}, ErrForbidden},
		"admin reset": {func(ctx context.Context, s *Service, _ store.User) error {
			_, err := s.OperatorReset(ctx, "admin")
			return err
		}, ErrForbidden},
		"admin changed password": {func(ctx context.Context, s *Service, admin store.User) error {
			_, err := s.ChangePassword(ctx, admin, testPassword, newPassword)
			return err
		}, ErrAccountChanged},
		"target promoted": {func(ctx context.Context, s *Service, _ store.User) error {
			return s.SetRole(ctx, operator, "target", store.RoleAdmin)
		}, ErrOperatorOnly},
		"target reset": {func(ctx context.Context, s *Service, _ store.User) error {
			_, err := s.OperatorReset(ctx, "target")
			return err
		}, ErrAccountChanged},
	} {
		t.Run(name, func(t *testing.T) {
			s, st := newTestService(t)
			seed(t, st, "keeper", store.RoleAdmin, false)
			admin, target := seed(t, st, "admin", store.RoleAdmin, false), seed(t, st, "target", store.RoleUser, false)
			compare := s.compare
			var between store.User
			s.compare = func(hash, password []byte) error {
				s.compare = compare
				err := compare(hash, password)
				if e := tc.meanwhile(t.Context(), s, admin); e != nil {
					t.Fatal(e)
				}
				between = account(t, s, target.ID)
				return err
			}
			if _, err := s.ResetPassword(t.Context(), admin, testPassword, target.ID, ResetState(target)); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if account(t, s, target.ID) != between {
				t.Fatal("refused reset was stored")
			}
		})
	}
}

func TestParallelResetsIssueOnePassword(t *testing.T) {
	s, st := newTestService(t)
	admin, alice := seed(t, st, "admin", store.RoleAdmin, false), seed(t, st, "alice", store.RoleUser, false)
	var issued [4]Issued
	var results [4]error
	var wg sync.WaitGroup
	for i := range results {
		wg.Go(func() {
			issued[i], results[i] = s.ResetPassword(context.Background(), admin, testPassword, alice.ID, ResetState(alice))
		})
	}
	wg.Wait()

	succeeded := 0
	for i, err := range results {
		switch {
		case err == nil:
			succeeded++
			if _, err := s.Authenticate(t.Context(), "alice", issued[i].Password); err != nil {
				t.Errorf("the shown password does not work: %v", err)
			}
		case !errors.Is(err, ErrAccountChanged):
			t.Errorf("error = %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("resets = %d, want 1", succeeded)
	}
}

func TestResetBeatsRunningLogin(t *testing.T) {
	s, st := newTestService(t)
	admin, alice := seed(t, st, "admin", store.RoleAdmin, false), seed(t, st, "alice", store.RoleUser, false)
	compare := s.compare
	s.compare = func(hash, password []byte) error {
		s.compare = compare
		reset(t, s, admin, alice)
		return compare(hash, password)
	}
	user, err := s.Authenticate(t.Context(), "alice", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if token, _, err := s.StartSession(t.Context(), user); !errors.Is(err, ErrInvalidCredentials) || token != "" {
		t.Fatalf("old password survived the reset: %v", err)
	}
}

func TestOperatorReset(t *testing.T) {
	s, st := newTestService(t)
	root, bob := seed(t, st, "root", store.RoleAdmin, false), seed(t, st, "bob", store.RoleUser, false)
	token, _, err := s.StartSession(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddMessage(t.Context(), store.Message{
		Kind: store.KindDirect, SenderID: bob.ID, Sender: "bob", RecipientID: root.ID, Recipient: "root", Body: "kept", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	issued, err := s.OperatorReset(t.Context(), " ROOT ")
	if err != nil || !issued.User.MustChangePassword || issued.User.Role != store.RoleAdmin || issued.User.DMVisibleAfterID != 0 {
		t.Fatalf("issued = %+v, %v", issued.User, err)
	}
	if _, err := s.Session(t.Context(), token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("session survived: %v", err)
	}
	if _, err := s.Authenticate(t.Context(), "root", issued.Password); err != nil {
		t.Fatalf("issued password: %v", err)
	}
	if history, err := st.History(t.Context(), root.ID); err != nil || len(history) != 1 {
		t.Fatalf("history = %v, %v", history, err)
	}
	if _, err := s.OperatorReset(t.Context(), "ghost"); !errors.Is(err, ErrUnknownUser) {
		t.Fatalf("unknown: %v", err)
	}
}

func TestWatchRefusesIssuedPasswords(t *testing.T) {
	s, st := newTestService(t)
	seed(t, st, "root", store.RoleAdmin, false)
	issued, err := s.OperatorReset(t.Context(), "root")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := s.StartSession(t.Context(), issued.User)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Watch(token)(t.Context()); err != nil || ok {
		t.Fatalf("chat with an issued password: %t, %v", ok, err)
	}
}

func TestPasswordsAreNeverLogged(t *testing.T) {
	s, st := newTestService(t)
	var log bytes.Buffer
	s.logger = slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	admin := seed(t, st, "admin", store.RoleAdmin, false)

	created, err := s.CreateAccount(t.Context(), admin, testPassword, "alice")
	if err != nil {
		t.Fatal(err)
	}
	again := reset(t, s, admin, created.User)
	if _, err := s.ChangePassword(t.Context(), again.User, again.Password, newPassword); err != nil {
		t.Fatal(err)
	}
	byOperator, err := s.OperatorReset(t.Context(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChangePassword(t.Context(), byOperator.User, "a-wrong-guess", newPassword); !errors.Is(err, ErrWrongPassword) {
		t.Fatal(err)
	}
	for _, secret := range []string{testPassword, newPassword, "a-wrong-guess", created.Password, again.Password, byOperator.Password, byOperator.User.PasswordHash} {
		if strings.Contains(log.String(), secret) {
			t.Fatal("a secret is in the log")
		}
	}
}
