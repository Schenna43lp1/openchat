package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// PasswordChange describes one write of SetPassword.
type PasswordChange struct {
	ActorID  int64 // 0 is the operator, who has no account
	TargetID int64
	Hash     string
	// MustChange limits the account to choosing its own password.
	MustChange bool
	// HideDirectMessages hides all direct messages stored so far from the target.
	HideDirectMessages bool
	// Check sees both accounts as they are inside the transaction and may refuse. actor is empty for the operator.
	Check func(actor, target User) error
}

// SetPassword replaces a password, ends all sessions of the account and records issued passwords and their use,
// all in one transaction with the check.
// An error from Check is returned as it is.
func (s *Store) SetPassword(ctx context.Context, change PasswordChange) (User, error) {
	var updated User
	var refused error
	err := inTx(ctx, s.db, func(tx *sql.Tx) error {
		actor, err := actorByID(ctx, tx, change.ActorID)
		if err != nil {
			return err
		}
		target, err := userByID(ctx, tx, change.TargetID)
		if err != nil {
			return err
		}
		if refused = change.Check(actor, target); refused != nil {
			return refused
		}
		if change.HideDirectMessages {
			if err := tx.QueryRowContext(ctx, `SELECT max(?, (SELECT coalesce(max(id), 0) FROM messages))`, target.DMVisibleAfterID).
				Scan(&target.DMVisibleAfterID); err != nil {
				return err
			}
		}
		redeems := target.MustChangePassword && !change.MustChange
		target.PasswordHash, target.MustChangePassword = change.Hash, change.MustChange
		if _, err := tx.ExecContext(ctx,
			`UPDATE users SET password_hash = ?, must_change_password = ?, dm_visible_after_id = ? WHERE id = ?`,
			target.PasswordHash, target.MustChangePassword, target.DMVisibleAfterID, target.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, target.ID); err != nil {
			return err
		}
		switch {
		case change.MustChange && change.ActorID == 0:
			err = recordIssue(ctx, tx, target.ID, IssueOperator, "")
		case change.MustChange:
			err = recordIssue(ctx, tx, target.ID, IssueReset, actor.Username)
		case redeems:
			err = redeemIssue(ctx, tx, target.ID)
		}
		if err != nil {
			return err
		}
		updated = target
		return nil
	})
	switch {
	case err == nil:
		return updated, nil
	case refused != nil:
		return User{}, refused
	case errors.Is(err, ErrNotFound):
		return User{}, ErrNotFound
	default:
		return User{}, fmt.Errorf("%w: set password: %w", ErrSave, err)
	}
}

func actorByID(ctx context.Context, tx *sql.Tx, id int64) (User, error) {
	if id == 0 {
		return User{}, nil
	}
	return userByID(ctx, tx, id)
}
