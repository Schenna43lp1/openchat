package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Role is stored with every account.
type Role string

// The roles.
const (
	RoleAdmin     Role = "admin"
	RoleModerator Role = "moderator"
	RoleUser      Role = "user"
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool {
	return r == RoleAdmin || r == RoleModerator || r == RoleUser
}

// Staff reports whether r may open the user management.
func (r Role) Staff() bool {
	return r == RoleAdmin || r == RoleModerator
}

// User is an account. A zero CreatedAt means that an imported account had no valid time.
type User struct {
	ID           int64
	Username     string
	PasswordHash string
	Role         Role
	Banned       bool
	CreatedAt    time.Time
}

const userColumns = `users.id, users.username, users.password_hash, users.role, users.banned, coalesce(users.created_at, 0)`

type row interface {
	Scan(dest ...any) error
}

func scanUser(r row) (User, error) {
	var u User
	var created int64
	err := r.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.Banned, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("read user: %w", err)
	}
	u.CreatedAt = fromMillis(created)
	return u, nil
}

// CreateUser adds an account and returns it with its ID.
func (s *Store) CreateUser(ctx context.Context, u User) (User, error) {
	err := inTx(ctx, s.db, func(tx *sql.Tx) error {
		var taken bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE username = ?)`, u.Username).Scan(&taken); err != nil {
			return err
		}
		if taken {
			return ErrTaken
		}
		var err error
		u.ID, err = insertUser(ctx, tx, u)
		return err
	})
	if errors.Is(err, ErrTaken) {
		return User{}, err
	}
	if err != nil {
		return User{}, fmt.Errorf("%w: create user: %w", ErrSave, err)
	}
	return u, nil
}

func insertUser(ctx context.Context, tx *sql.Tx, u User) (int64, error) {
	result, err := tx.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, role, banned, created_at) VALUES (?, ?, ?, ?, ?)`,
		u.Username, u.PasswordHash, u.Role, u.Banned, toMillis(u.CreatedAt))
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// UserByName ignores the case of ASCII letters, the only ones valid in names.
func (s *Store) UserByName(ctx context.Context, name string) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE username = ?`, name))
}

// Users lists all accounts by name.
func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userColumns+` FROM users ORDER BY username`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	return users, nil
}

// ActiveAdmins counts the admins who are not banned.
func (s *Store) ActiveAdmins(ctx context.Context) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, activeAdminsQuery).Scan(&count); err != nil {
		return 0, fmt.Errorf("count admins: %w", err)
	}
	return count, nil
}

const activeAdminsQuery = `SELECT count(*) FROM users WHERE role = 'admin' AND banned = 0`

// UpdateUser lets change edit role and ban of one account. The check and the write are one transaction,
// so rules like "keep one admin" hold under concurrency. A ban also ends the sessions of the account.
// An error from change is returned as it is.
func (s *Store) UpdateUser(ctx context.Context, name string, change func(u *User, activeAdmins int) error) (before, after User, err error) {
	var refused error
	err = inTx(ctx, s.db, func(tx *sql.Tx) error {
		var err error
		before, err = scanUser(tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE username = ?`, name))
		if err != nil {
			return err
		}
		var admins int
		if err := tx.QueryRowContext(ctx, activeAdminsQuery).Scan(&admins); err != nil {
			return err
		}
		after = before
		if refused = change(&after, admins); refused != nil {
			return refused
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET role = ?, banned = ? WHERE id = ?`, after.Role, after.Banned, before.ID); err != nil {
			return err
		}
		if after.Banned && !before.Banned {
			if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, before.ID); err != nil {
				return err
			}
		}
		return nil
	})
	switch {
	case err == nil:
		return before, after, nil
	case refused != nil:
		return User{}, User{}, refused
	case errors.Is(err, ErrNotFound):
		return User{}, User{}, ErrNotFound
	default:
		return User{}, User{}, fmt.Errorf("%w: update user: %w", ErrSave, err)
	}
}
