// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 20:12:40
// Original filename: src/internal/storage/messages.go

package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Message directions.
const (
	// DirectionIn is phone to Mattermost.
	DirectionIn = "in"
	// DirectionOut is Mattermost to phone.
	DirectionOut = "out"
)

// Message is the mapping between one Google Messages message and its
// Mattermost post.
type Message struct {
	ID             string
	PostID         string
	ConversationID string
	Direction      string
	// Status is the last gmproto.MessageStatusType observed.
	Status    int32
	CreatedAt time.Time
}

// SaveMessage records a message mapping, or updates the mapping already there.
func (db *DB) SaveMessage(ctx context.Context, msg Message) error {
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now()
	}
	_, err := db.execContext(ctx, `
		INSERT INTO messages (
			gmessages_message_id, mattermost_post_id, conversation_id,
			direction, status, created_at
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(gmessages_message_id) DO UPDATE SET
			mattermost_post_id = excluded.mattermost_post_id,
			conversation_id    = excluded.conversation_id,
			direction          = excluded.direction,
			status             = excluded.status`,
		msg.ID, msg.PostID, msg.ConversationID, msg.Direction, msg.Status,
		msg.CreatedAt.Unix())
	return err
}

// GetMessage returns the mapping for a Google Messages message ID, or
// ErrNotFound.
//
// This is the deduplication check docs/SOLUTION.md calls for: a reconnect can
// replay events, and a message that is already mapped must not be posted twice.
func (db *DB) GetMessage(ctx context.Context, id string) (Message, error) {
	row := db.queryRowContext(ctx, `
		SELECT gmessages_message_id, mattermost_post_id, conversation_id,
		       direction, status, created_at
		FROM messages WHERE gmessages_message_id = ?`, id)

	var (
		msg       Message
		createdAt int64
	)
	err := row.Scan(&msg.ID, &msg.PostID, &msg.ConversationID, &msg.Direction,
		&msg.Status, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	if err != nil {
		return Message{}, err
	}
	msg.CreatedAt = time.Unix(createdAt, 0)
	return msg, nil
}

// GetMessageByPost is the reverse lookup: which Google Messages message a
// Mattermost post stands for. It backs replies typed in Mattermost against a
// specific bridged message.
func (db *DB) GetMessageByPost(ctx context.Context, postID string) (Message, error) {
	if postID == "" {
		return Message{}, ErrNotFound
	}
	row := db.queryRowContext(ctx, `
		SELECT gmessages_message_id, mattermost_post_id, conversation_id,
		       direction, status, created_at
		FROM messages WHERE mattermost_post_id = ? LIMIT 1`, postID)

	var (
		msg       Message
		createdAt int64
	)
	err := row.Scan(&msg.ID, &msg.PostID, &msg.ConversationID, &msg.Direction,
		&msg.Status, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	if err != nil {
		return Message{}, err
	}
	msg.CreatedAt = time.Unix(createdAt, 0)
	return msg, nil
}

// UpdateMessageStatus records a new delivery status and reports whether it
// actually changed. The caller uses that to avoid re-decorating a post with a
// status it already shows, since the phone repeats status updates freely.
func (db *DB) UpdateMessageStatus(ctx context.Context, id string, status int32) (changed bool, err error) {
	result, err := db.execContext(ctx,
		`UPDATE messages SET status = ? WHERE gmessages_message_id = ? AND status != ?`,
		status, id, status)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected > 0, err
}

// AddPendingOutbound records that a Mattermost post has been sent to the phone
// under a temporary ID, before Google Messages has assigned it a real one.
func (db *DB) AddPendingOutbound(ctx context.Context, tmpID, postID, conversationID string) error {
	_, err := db.execContext(ctx, `
		INSERT INTO outbound_pending (tmp_id, mattermost_post_id, conversation_id, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(tmp_id) DO UPDATE SET
			mattermost_post_id = excluded.mattermost_post_id,
			conversation_id    = excluded.conversation_id`,
		tmpID, postID, conversationID, time.Now().Unix())
	return err
}

// TakePendingOutbound resolves a temporary ID to the Mattermost post it came
// from and forgets it. It returns ErrNotFound for a tmpID this daemon did not
// issue — which is how a message sent from the phone itself, or from another
// paired device, is told apart from one the bridge sent.
func (db *DB) TakePendingOutbound(ctx context.Context, tmpID string) (postID string, err error) {
	if tmpID == "" {
		return "", ErrNotFound
	}
	tx, err := db.beginTx(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	err = tx.QueryRowContext(ctx,
		`SELECT mattermost_post_id FROM outbound_pending WHERE tmp_id = ?`, tmpID).Scan(&postID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM outbound_pending WHERE tmp_id = ?`, tmpID); err != nil {
		return "", err
	}
	return postID, tx.Commit()
}

// PrunePendingOutbound drops entries older than maxAge. A message the phone
// never acknowledged leaves its temporary ID behind for good, so without this
// the table only ever grows.
func (db *DB) PrunePendingOutbound(ctx context.Context, maxAge time.Duration) (int64, error) {
	result, err := db.execContext(ctx,
		`DELETE FROM outbound_pending WHERE created_at <= ?`,
		time.Now().Add(-maxAge).Unix())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// CountMessages returns how many message mappings are stored, for the status
// command.
func (db *DB) CountMessages(ctx context.Context) (int, error) {
	var count int
	err := db.queryRowContext(ctx, `SELECT COUNT(*) FROM messages`).Scan(&count)
	return count, err
}
