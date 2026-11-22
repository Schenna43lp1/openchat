package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// CreateSession stores the hash of a token, never the token. It returns ErrNotFound if the account is
// banned, gone or has another password than user. The check and the write are one statement,
// because any of that can happen while a password is being compared.
func (s *Store) CreateSession(ctx context.Context, tokenHash []byte, user User, expires time.Time) error {
	result, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at)
		 SELECT ?, id, ? FROM users WHERE id = ? AND password_hash = ? AND banned = 0`,
		tokenHash, expires.UnixMilli(), user.ID, user.PasswordHash)
	if err != nil {
		return fmt.Errorf("%w: create session: %w", ErrSave, err)
	}
	created, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%w: create session: %w", ErrSave, err)
	}
	if created == 0 {
		return ErrNotFound
	}
	return nil
}

// Session returns the account and the end of a session, which may have passed already.
func (s *Store) Session(ctx context.Context, tokenHash []byte) (User, time.Time, error) {
	var u User
	var created, expires int64
	err := s.db.QueryRowContext(ctx,
		`SELECT `+userColumns+`, sessions.expires_at
		   FROM sessions JOIN users ON users.id = sessions.user_id
		  WHERE sessions.token_hash = ?`, tokenHash,
	).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.Banned, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, time.Time{}, ErrNotFound
	}
	if err != nil {
		return User{}, time.Time{}, fmt.Errorf("read session: %w", err)
	}
	u.CreatedAt = fromMillis(created)
	return u, time.UnixMilli(expires), nil
}

// DeleteSession ends one session.
func (s *Store) DeleteSession(ctx context.Context, tokenHash []byte) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash); err != nil {
		return fmt.Errorf("%w: delete session: %w", ErrSave, err)
	}
	return nil
}

// DeleteExpiredSessions returns how many sessions it removed.
func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now.UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("%w: delete expired sessions: %w", ErrSave, err)
	}
	return result.RowsAffected()
}
