package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrRefused means that the account may not do this in its current state.
var ErrRefused = errors.New("refused")

// UserKey is the key pair of an account. The server never sees the private key unwrapped.
// Salt and Wrapped are empty once the key is retired.
type UserKey struct {
	ID        string
	UserID    int64
	PublicKey []byte
	Salt      []byte
	Wrapped   []byte
	CreatedAt time.Time
	RetiredAt time.Time
}

// InstanceID is random per database and never changes.
func (s *Store) InstanceID(ctx context.Context) ([]byte, error) {
	var id []byte
	if err := s.db.QueryRowContext(ctx, `SELECT instance_id FROM instance WHERE id = 1`).Scan(&id); err != nil {
		return nil, fmt.Errorf("read instance id: %w", err)
	}
	return id, nil
}

const keyColumns = `key_id, user_id, public_key, key_salt, wrapped, created_at, coalesce(retired_at, 0)`

func scanKey(r row) (UserKey, error) {
	var k UserKey
	var created, retired int64
	err := r.Scan(&k.ID, &k.UserID, &k.PublicKey, &k.Salt, &k.Wrapped, &created, &retired)
	if errors.Is(err, sql.ErrNoRows) {
		return UserKey{}, ErrNotFound
	}
	if err != nil {
		return UserKey{}, fmt.Errorf("read key: %w", err)
	}
	k.CreatedAt, k.RetiredAt = fromMillis(created), fromMillis(retired)
	return k, nil
}

// CurrentKey returns ErrNotFound while the account has no key that is not retired.
func (s *Store) CurrentKey(ctx context.Context, userID int64) (UserKey, error) {
	return scanKey(s.db.QueryRowContext(ctx,
		`SELECT `+keyColumns+` FROM user_keys WHERE user_id = ? AND retired_at IS NULL`, userID))
}

// CurrentKeyByName returns the account and its current key. ErrNotFound covers both a missing account and a
// missing key.
func (s *Store) CurrentKeyByName(ctx context.Context, name string) (User, UserKey, error) {
	user, err := s.UserByName(ctx, name)
	if err != nil {
		return User{}, UserKey{}, err
	}
	key, err := s.CurrentKey(ctx, user.ID)
	if err != nil {
		return User{}, UserKey{}, err
	}
	return user, key, nil
}

// AddKey stores the first current key of an account. It returns ErrTaken if the account already has one or the
// ID is in use, and ErrRefused if the account is banned, still has an issued password (such a key would be
// wrapped with a password that somebody else knows) or no longer has the password hash that was checked.
func (s *Store) AddKey(ctx context.Context, k UserKey, checkedHash string) (UserKey, error) {
	if k.CreatedAt.IsZero() {
		k.CreatedAt = time.Now()
	}
	err := inTx(ctx, s.db, func(tx *sql.Tx) error {
		owner, err := userByID(ctx, tx, k.UserID)
		if err != nil {
			return err
		}
		if owner.Banned || owner.MustChangePassword || owner.PasswordHash != checkedHash {
			return ErrRefused
		}
		var taken bool
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM user_keys WHERE key_id = ? OR (user_id = ? AND retired_at IS NULL))`,
			k.ID, k.UserID).Scan(&taken); err != nil {
			return err
		}
		if taken {
			return ErrTaken
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO user_keys (key_id, user_id, public_key, key_salt, wrapped, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			k.ID, k.UserID, k.PublicKey, k.Salt, k.Wrapped, k.CreatedAt.UnixMilli())
		return err
	})
	switch {
	case err == nil:
		k.CreatedAt = fromMillis(k.CreatedAt.UnixMilli())
		return k, nil
	case errors.Is(err, ErrTaken), errors.Is(err, ErrNotFound), errors.Is(err, ErrRefused):
		return UserKey{}, err
	default:
		return UserKey{}, fmt.Errorf("%w: add key: %w", ErrSave, err)
	}
}
