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

	// MustChangePassword is set while the password is one that somebody else has seen.
	MustChangePassword bool
	// OperatorResetOnly is set once an account is admin and stays.
	OperatorResetOnly bool
	// DMVisibleAfterID hides direct messages up to this ID from this account.
	DMVisibleAfterID int64
}

const userColumns = `users.id, users.username, users.password_hash, users.role, users.banned, coalesce(users.created_at, 0),
	users.must_change_password, users.operator_reset_only, users.dm_visible_after_id`

type row interface {
	Scan(dest ...any) error
}

// scanUser reads userColumns, followed by the columns that go to extra.
func scanUser(r row, extra ...any) (User, error) {
	var u User
	var created int64
	err := r.Scan(append([]any{&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.Banned, &created,
		&u.MustChangePassword, &u.OperatorResetOnly, &u.DMVisibleAfterID}, extra...)...)
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
	return s.CreateUserBy(ctx, 0, u, func(User) error { return nil })
}

// CreateUserBy is CreateUser for another account: check sees the actor inside the transaction and may refuse.
// An error from check is returned as it is.
func (s *Store) CreateUserBy(ctx context.Context, actorID int64, u User, check func(actor User) error) (User, error) {
	u.OperatorResetOnly = u.OperatorResetOnly || u.Role == RoleAdmin
	var refused error
	err := inTx(ctx, s.db, func(tx *sql.Tx) error {
		actor, err := actorByID(ctx, tx, actorID)
		if err != nil {
			return err
		}
		if refused = check(actor); refused != nil {
			return refused
		}
		var taken bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE username = ?)`, u.Username).Scan(&taken); err != nil {
			return err
		}
		if taken {
			return ErrTaken
		}
		u.ID, err = insertUser(ctx, tx, u)
		return err
	})
	if refused != nil {
		return User{}, refused
	}
	if errors.Is(err, ErrTaken) || errors.Is(err, ErrNotFound) {
		return User{}, err
	}
	if err != nil {
		return User{}, fmt.Errorf("%w: create user: %w", ErrSave, err)
	}
	return u, nil
}

func insertUser(ctx context.Context, tx *sql.Tx, u User) (int64, error) {
	result, err := tx.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, role, banned, created_at,
		                    must_change_password, operator_reset_only, dm_visible_after_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		u.Username, u.PasswordHash, u.Role, u.Banned, toMillis(u.CreatedAt),
		// Also here, because the import from v1.0.x does not go through CreateUserBy.
		u.MustChangePassword, u.OperatorResetOnly || u.Role == RoleAdmin, u.DMVisibleAfterID)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// UserByName ignores the case of ASCII letters, the only ones valid in names.
func (s *Store) UserByName(ctx context.Context, name string) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE username = ?`, name))
}

// UserByID returns ErrNotFound for IDs without an account.
func (s *Store) UserByID(ctx context.Context, id int64) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, userByIDQuery, id))
}

const userByIDQuery = `SELECT ` + userColumns + ` FROM users WHERE id = ?`

func userByID(ctx context.Context, tx *sql.Tx, id int64) (User, error) {
	return scanUser(tx.QueryRowContext(ctx, userByIDQuery, id))
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
		after.OperatorResetOnly = before.OperatorResetOnly || before.Role == RoleAdmin || after.Role == RoleAdmin
		if _, err := tx.ExecContext(ctx, `UPDATE users SET role = ?, banned = ?, operator_reset_only = ? WHERE id = ?`,
			after.Role, after.Banned, after.OperatorResetOnly, before.ID); err != nil {
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
