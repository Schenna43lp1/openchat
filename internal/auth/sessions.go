package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Schenna43lp1/openchat/internal/store"
)

// SessionDuration is how long a login lasts.
const SessionDuration = 24 * time.Hour

// ErrNoSession means that a token is unknown or expired.
var ErrNoSession = errors.New("no valid session")

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// StartSession returns a new token. Only its hash is stored.
func (s *Service) StartSession(ctx context.Context, user store.User) (token string, expires time.Time, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, fmt.Errorf("create token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	expires = time.Now().Add(SessionDuration)
	if err := s.store.CreateSession(ctx, hashToken(token), user.ID, expires); err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}

// Session returns the current state of the account behind a token, which may be banned.
func (s *Service) Session(ctx context.Context, token string) (store.User, error) {
	user, expires, err := s.store.Session(ctx, hashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		return store.User{}, ErrNoSession
	}
	if err != nil {
		return store.User{}, err
	}
	if !time.Now().Before(expires) {
		if err := s.EndSession(ctx, token); err != nil {
			s.logger.Error("remove expired session", "err", err)
		}
		return store.User{}, ErrNoSession
	}
	return user, nil
}

// EndSession also revokes the connections that belong to the token.
func (s *Service) EndSession(ctx context.Context, token string) error {
	if err := s.store.DeleteSession(ctx, hashToken(token)); err != nil {
		return err
	}
	s.revision.Add(1)
	return nil
}

// RemoveExpiredSessions is for periodic cleanup.
func (s *Service) RemoveExpiredSessions(ctx context.Context) error {
	removed, err := s.store.DeleteExpiredSessions(ctx, time.Now())
	if err != nil {
		return err
	}
	if removed > 0 {
		s.logger.Info("expired sessions removed", "count", removed)
	}
	return nil
}

// Watch returns a check for a long-lived connection. It is cheap enough for every event: the database
// is only asked again after a logout or ban in this process. An error says nothing about the session.
func (s *Service) Watch(token string) func(context.Context) (bool, error) {
	hash := hashToken(token)
	var (
		mu      sync.Mutex
		known   bool
		seen    uint64
		expires time.Time
	)
	return func(ctx context.Context) (bool, error) {
		mu.Lock()
		defer mu.Unlock()

		// Read before the query, so that a revocation during the query is not missed.
		revision := s.revision.Load()
		if known && seen == revision {
			return time.Now().Before(expires), nil
		}
		user, until, err := s.store.Session(ctx, hash)
		switch {
		case errors.Is(err, store.ErrNotFound):
			until = time.Time{}
		case err != nil:
			return false, err
		case user.Banned:
			until = time.Time{}
		}
		known, seen, expires = true, revision, until
		return time.Now().Before(expires), nil
	}
}
