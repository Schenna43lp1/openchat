package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Schenna43lp1/openchat/internal/store"
)

// cancelDuringWrite ends the context after delay, like a browser that disconnects during the request.
func cancelDuringWrite(delay time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// Sleeping is too coarse on Windows for delays below a millisecond.
		for start := time.Now(); time.Since(start) < delay; {
		}
		cancel()
	}()
	return ctx, cancel
}

// The driver can report "context canceled" for a write that happened.
// A revocation must reach open connections anyway.
func TestInterruptedRevocationReachesOpenConnections(t *testing.T) {
	revocations := map[string]func(s *Service, ctx context.Context, token string){
		"logout": func(s *Service, ctx context.Context, token string) { _ = s.EndSession(ctx, token) },
		"ban":    func(s *Service, ctx context.Context, _ string) { _ = s.SetBanned(ctx, operator, "victim", true) },
	}
	for name, revoke := range revocations {
		t.Run(name, func(t *testing.T) {
			s, st := newTestService(t)
			victim := seed(t, st, "victim", store.RoleUser, false)
			for i := range 60 {
				token, _, err := s.StartSession(t.Context(), victim)
				if err != nil {
					t.Fatal(err)
				}
				watch := s.Watch(token)
				if ok, err := watch(t.Context()); err != nil || !ok {
					t.Fatalf("setup = %t, %v", ok, err)
				}

				ctx, cancel := cancelDuringWrite(time.Duration(i*50) * time.Microsecond)
				revoke(s, ctx, token)
				cancel()

				_, err = s.Session(t.Context(), token)
				if err != nil && !errors.Is(err, ErrNoSession) {
					t.Fatal(err)
				}
				ended := err != nil
				live, err := watch(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if ended && live {
					t.Fatalf("attempt %d: session ended, but its connections stay authorized", i)
				}
				if u, err := s.Find(t.Context(), "victim"); err != nil {
					t.Fatal(err)
				} else if u.Banned {
					if err := s.SetBanned(t.Context(), operator, "victim", false); err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}
}

func TestLogoutFinishesWithoutTheClient(t *testing.T) {
	s, st := newTestService(t)
	alice := seed(t, st, "alice", store.RoleUser, false)
	token, _, err := s.StartSession(t.Context(), alice)
	if err != nil {
		t.Fatal(err)
	}
	gone, cancel := context.WithCancel(context.Background())
	cancel()

	if err := s.EndSession(gone, token); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, err := s.Session(t.Context(), token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("session survived: %v", err)
	}
}
