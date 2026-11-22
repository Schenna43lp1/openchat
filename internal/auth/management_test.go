package auth

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Schenna43lp1/openchat/internal/store"
)

func TestBansAtTheSameTimeStayBans(t *testing.T) {
	s, st := newTestService(t)
	admin, moderator := seed(t, st, "admin", store.RoleAdmin, false), seed(t, st, "moderator", store.RoleModerator, false)
	target := seed(t, st, "target", store.RoleUser, false)

	var results [10]error
	var wg sync.WaitGroup
	for i := range results {
		actor := admin
		if i%2 == 1 {
			actor = moderator
		}
		wg.Go(func() { results[i] = s.SetBanned(context.Background(), actor, "target", true) })
	}
	wg.Wait()
	for i, err := range results {
		if err != nil {
			t.Errorf("ban %d: %v", i, err)
		}
	}
	if !account(t, s, target.ID).Banned {
		t.Fatal("a second ban undid the first")
	}
}

// What the request read at its beginning must not count.
func TestOutdatedActorsAreRefused(t *testing.T) {
	outdate := map[string]func(ctx context.Context, s *Service) error{
		"demoted": func(ctx context.Context, s *Service) error { return s.SetRole(ctx, operator, "actor", store.RoleUser) },
		"banned":  func(ctx context.Context, s *Service) error { return s.SetBanned(ctx, operator, "actor", true) },
		"reset": func(ctx context.Context, s *Service) error {
			_, err := s.OperatorReset(ctx, "actor")
			return err
		},
	}
	actions := map[string]func(ctx context.Context, s *Service, actor store.User) error{
		"ban": func(ctx context.Context, s *Service, actor store.User) error {
			return s.SetBanned(ctx, actor, "target", true)
		},
		"role": func(ctx context.Context, s *Service, actor store.User) error {
			return s.SetRole(ctx, actor, "target", store.RoleAdmin)
		},
	}
	for change, apply := range outdate {
		for action, run := range actions {
			t.Run(change+"/"+action, func(t *testing.T) {
				s, st := newTestService(t)
				seed(t, st, "keeper", store.RoleAdmin, false)
				actor, target := seed(t, st, "actor", store.RoleAdmin, false), seed(t, st, "target", store.RoleUser, false)
				if err := apply(t.Context(), s); err != nil {
					t.Fatal(err)
				}
				if err := run(t.Context(), s, actor); !errors.Is(err, ErrForbidden) {
					t.Fatalf("error = %v", err)
				}
				if account(t, s, target.ID) != target {
					t.Fatal("outdated actor changed the account")
				}
			})
		}
	}
}

func TestModeratorPromotedTargetCannotBeBanned(t *testing.T) {
	s, st := newTestService(t)
	moderator, target := seed(t, st, "moderator", store.RoleModerator, false), seed(t, st, "target", store.RoleUser, false)
	if err := s.SetRole(t.Context(), operator, "target", store.RoleModerator); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBanned(t.Context(), moderator, "target", true); !errors.Is(err, ErrStaffTarget) {
		t.Fatalf("error = %v", err)
	}
	if account(t, s, target.ID).Banned {
		t.Fatal("staff was banned by a moderator")
	}
}

func TestCanBan(t *testing.T) {
	admin := store.User{ID: 1, Role: store.RoleAdmin}
	moderator := store.User{ID: 2, Role: store.RoleModerator}
	user := store.User{ID: 3, Role: store.RoleUser}
	otherAdmin := store.User{ID: 4, Role: store.RoleAdmin}
	for _, tc := range []struct {
		actor, target store.User
		want          bool
	}{
		{admin, user, true}, {admin, moderator, true}, {admin, otherAdmin, true}, {admin, admin, false},
		{moderator, user, true}, {moderator, admin, false}, {moderator, moderator, false},
		{user, moderator, false},
	} {
		if got := CanBan(tc.actor, tc.target); got != tc.want {
			t.Errorf("%s %d on %s %d = %t", tc.actor.Role, tc.actor.ID, tc.target.Role, tc.target.ID, got)
		}
	}
}

func TestChangeRole(t *testing.T) {
	for name, tc := range map[string]struct {
		actor     string
		password  string
		target    int64 // 0 is the target account
		from, to  store.Role
		meanwhile func(ctx context.Context, s *Service, admin store.User) error
		want      error
	}{
		"promotion":      {actor: "admin", from: store.RoleUser, to: store.RoleAdmin},
		"moderator":      {actor: "moderator", from: store.RoleUser, to: store.RoleAdmin, want: ErrForbidden},
		"wrong password": {actor: "admin", password: "wrong-password", from: store.RoleUser, to: store.RoleAdmin, want: ErrWrongPassword},
		"outdated form":  {actor: "admin", from: store.RoleModerator, to: store.RoleAdmin, want: ErrAccountChanged},
		"invalid role":   {actor: "admin", from: store.RoleUser, to: "owner", want: ErrInvalidRole},
		"unknown":        {actor: "admin", target: 999, from: store.RoleUser, to: store.RoleAdmin, want: ErrUnknownUser},
		"demoted meanwhile": {actor: "admin", from: store.RoleUser, to: store.RoleAdmin, want: ErrForbidden,
			meanwhile: func(ctx context.Context, s *Service, _ store.User) error {
				return s.SetRole(ctx, operator, "admin", store.RoleUser)
			}},
		"password changed meanwhile": {actor: "admin", from: store.RoleUser, to: store.RoleAdmin, want: ErrAccountChanged,
			meanwhile: func(ctx context.Context, s *Service, admin store.User) error {
				_, err := s.ChangePassword(ctx, admin, testPassword, newPassword)
				return err
			}},
	} {
		t.Run(name, func(t *testing.T) {
			s, st := newTestService(t)
			seed(t, st, "keeper", store.RoleAdmin, false)
			actors := map[string]store.User{
				"admin":     seed(t, st, "admin", store.RoleAdmin, false),
				"moderator": seed(t, st, "moderator", store.RoleModerator, false),
			}
			target := seed(t, st, "target", store.RoleUser, false)
			id, password := target.ID, testPassword
			if tc.target != 0 {
				id = tc.target
			}
			if tc.password != "" {
				password = tc.password
			}
			if tc.meanwhile != nil {
				compare := s.compare
				s.compare = func(hash, given []byte) error {
					s.compare = compare
					err := compare(hash, given)
					if e := tc.meanwhile(t.Context(), s, actors["admin"]); e != nil {
						t.Fatal(e)
					}
					return err
				}
			}
			if err := s.ChangeRole(t.Context(), actors[tc.actor], password, id, tc.from, tc.to); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			got := account(t, s, target.ID)
			if tc.want != nil && got != target {
				t.Fatalf("refused change was stored: %+v", got)
			}
			if tc.want == nil && (got.Role != tc.to || !got.OperatorResetOnly) {
				t.Fatalf("account = %+v", got)
			}
		})
	}
}
