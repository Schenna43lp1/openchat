package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// HistoryLimit is how many public messages are kept, and how many direct messages per sender.
const HistoryLimit = 100

// Kind separates public from direct messages.
type Kind string

// The kinds of stored messages. Join and leave notices are never stored.
const (
	KindPublic Kind = "message"
	KindDirect Kind = "direct"
)

// Message is one stored chat message. An ID of 0 means that no account may claim that side.
type Message struct {
	ID          int64
	Kind        Kind
	SenderID    int64
	Sender      string
	RecipientID int64
	Recipient   string
	Body        string
	CreatedAt   time.Time
}

// AddMessage stores m and trims what it displaces.
func (s *Store) AddMessage(ctx context.Context, m Message) (Message, error) {
	err := inTx(ctx, s.db, func(tx *sql.Tx) error {
		var err error
		if m.ID, err = insertMessage(ctx, tx, m); err != nil {
			return err
		}
		// Imported direct messages without a known sender are never trimmed, deleting those is up to the operator.
		if m.Kind == KindDirect {
			_, err = tx.ExecContext(ctx,
				`DELETE FROM messages WHERE id IN (
					SELECT id FROM messages WHERE sender_id = ? AND kind = ? ORDER BY id DESC LIMIT -1 OFFSET ?)`,
				m.SenderID, m.Kind, HistoryLimit)
		} else {
			_, err = tx.ExecContext(ctx,
				`DELETE FROM messages WHERE id IN (
					SELECT id FROM messages WHERE kind = ? ORDER BY id DESC LIMIT -1 OFFSET ?)`,
				m.Kind, HistoryLimit)
		}
		return err
	})
	if err != nil {
		return Message{}, fmt.Errorf("%w: add message: %w", ErrSave, err)
	}
	return m, nil
}

func insertMessage(ctx context.Context, tx *sql.Tx, m Message) (int64, error) {
	var created int64 // legacy messages may have no time
	if !m.CreatedAt.IsZero() {
		created = m.CreatedAt.UnixMilli()
	}
	result, err := tx.ExecContext(ctx,
		`INSERT INTO messages (kind, sender_id, sender_name, recipient_id, recipient_name, body, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		m.Kind, nullID(m.SenderID), m.Sender, nullID(m.RecipientID), m.Recipient, m.Body, created)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// History returns what userID may read, oldest first: the newest public messages and the newest
// direct messages of that account. Neither kind can displace the other.
func (s *Store) History(ctx context.Context, userID int64) ([]Message, error) {
	const columns = `id, kind, coalesce(sender_id, 0) AS sender_id, sender_name,
		coalesce(recipient_id, 0) AS recipient_id, recipient_name, body, created_at`
	rows, err := s.db.QueryContext(ctx,
		`SELECT * FROM (SELECT `+columns+` FROM messages WHERE kind = 'message' ORDER BY id DESC LIMIT ?)
		 UNION ALL
		 SELECT * FROM (SELECT `+columns+` FROM messages
		                 WHERE kind = 'direct' AND (messages.sender_id = ? OR messages.recipient_id = ?)
		                 ORDER BY id DESC LIMIT ?)
		 ORDER BY id`,
		HistoryLimit, userID, userID, HistoryLimit)
	if err != nil {
		return nil, fmt.Errorf("read history: %w", err)
	}
	defer rows.Close()

	var history []Message
	for rows.Next() {
		var m Message
		var created int64
		if err := rows.Scan(&m.ID, &m.Kind, &m.SenderID, &m.Sender, &m.RecipientID, &m.Recipient, &m.Body, &created); err != nil {
			return nil, fmt.Errorf("read message: %w", err)
		}
		m.CreatedAt = fromMillis(created)
		history = append(history, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read history: %w", err)
	}
	return history, nil
}
