package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// IssueListLimit is how many issued passwords are listed. The totals always cover all of them.
const IssueListLimit = 10

// IssueKind says how a password came to be issued.
type IssueKind string

// The kinds. Who did it is never told by a name alone: an admin could be called like the operator.
const (
	IssueCreated  IssueKind = "created"
	IssueReset    IssueKind = "reset"
	IssueOperator IssueKind = "operator"
)

// PasswordIssue is one password that somebody else chose for an account.
type PasswordIssue struct {
	Kind       IssueKind
	By         string // empty for the operator
	At         time.Time
	RedeemedAt time.Time // zero unless an own password was set with it
}

// IssueHistory lists the newest first.
type IssueHistory struct {
	Latest   []PasswordIssue
	Total    int
	Redeemed int
}

// PasswordIssues returns what the owner of userID gets to see.
func (s *Store) PasswordIssues(ctx context.Context, userID int64) (IssueHistory, error) {
	var history IssueHistory
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*), count(redeemed_at) FROM password_issues WHERE user_id = ?`, userID,
	).Scan(&history.Total, &history.Redeemed); err != nil {
		return IssueHistory{}, fmt.Errorf("count issued passwords: %w", err)
	}
	if history.Total == 0 {
		return history, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT kind, actor_name, issued_at, coalesce(redeemed_at, 0)
		   FROM password_issues WHERE user_id = ? ORDER BY id DESC LIMIT ?`, userID, IssueListLimit)
	if err != nil {
		return IssueHistory{}, fmt.Errorf("list issued passwords: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var issue PasswordIssue
		var at, redeemed int64
		if err := rows.Scan(&issue.Kind, &issue.By, &at, &redeemed); err != nil {
			return IssueHistory{}, fmt.Errorf("read issued password: %w", err)
		}
		issue.At, issue.RedeemedAt = fromMillis(at), fromMillis(redeemed)
		history.Latest = append(history.Latest, issue)
	}
	if err := rows.Err(); err != nil {
		return IssueHistory{}, fmt.Errorf("list issued passwords: %w", err)
	}
	return history, nil
}

func recordIssue(ctx context.Context, tx *sql.Tx, userID int64, kind IssueKind, by string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO password_issues (user_id, kind, actor_name, issued_at) VALUES (?, ?, ?, ?)`,
		userID, kind, by, time.Now().UnixMilli())
	return err
}

// redeemIssue marks the newest one, the only one whose password still works.
func redeemIssue(ctx context.Context, tx *sql.Tx, userID int64) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE password_issues SET redeemed_at = ?
		  WHERE redeemed_at IS NULL AND id = (SELECT max(id) FROM password_issues WHERE user_id = ?)`,
		time.Now().UnixMilli(), userID)
	return err
}
