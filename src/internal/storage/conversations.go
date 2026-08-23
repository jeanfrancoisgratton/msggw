// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 19:45:42
// Original filename: src/internal/storage/conversations.go

package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Conversation is the mapping between one Google Messages conversation and its
// place in Mattermost.
type Conversation struct {
	ID          string
	ChannelID   string
	RootPostID  string
	DisplayName string
	IsGroup     bool
	// OutgoingParticipantID is the sender identity the phone expects on
	// outgoing messages in this conversation.
	OutgoingParticipantID string
	// Type and SendMode are gmproto.ConversationType and
	// gmproto.ConversationSendMode, kept as plain ints so that storage does not
	// depend on the protocol package.
	Type     int32
	SendMode int32
	LastSeen time.Time

	Participants []Participant
}

// Participant is one member of a conversation.
type Participant struct {
	ID          string
	Phone       string
	DisplayName string
	IsMe        bool
}

// NormalizePhone strips the punctuation people put in phone numbers, so that
// "+1 514 555-1212", "+1 (514) 555.1212" and "+15145551212" compare equal.
// It deliberately does not try to be a full E.164 normaliser: it has no way to
// know the local dialling plan, so a bare "5551212" stays as it is.
func NormalizePhone(phone string) string {
	var b strings.Builder
	b.Grow(len(phone))
	for _, r := range phone {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' && b.Len() == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// SaveConversation inserts or updates a conversation and replaces its
// participant list.
func (db *DB) SaveConversation(ctx context.Context, conv Conversation) error {
	tx, err := db.beginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `
		INSERT INTO conversations (
			gmessages_conversation_id, mattermost_channel_id, mattermost_root_post_id,
			display_name, is_group, outgoing_participant_id, conversation_type,
			send_mode, last_seen
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(tenant, gmessages_conversation_id) DO UPDATE SET
			mattermost_channel_id   = excluded.mattermost_channel_id,
			mattermost_root_post_id = excluded.mattermost_root_post_id,
			display_name            = excluded.display_name,
			is_group                = excluded.is_group,
			outgoing_participant_id = excluded.outgoing_participant_id,
			conversation_type       = excluded.conversation_type,
			send_mode               = excluded.send_mode,
			last_seen               = excluded.last_seen`,
		conv.ID, conv.ChannelID, conv.RootPostID, conv.DisplayName, conv.IsGroup,
		conv.OutgoingParticipantID, conv.Type, conv.SendMode, conv.LastSeen.Unix())
	if err != nil {
		return fmt.Errorf("saving conversation %s: %w", conv.ID, err)
	}

	if conv.Participants != nil {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM conversation_participants WHERE gmessages_conversation_id = ?`,
			conv.ID); err != nil {
			return fmt.Errorf("clearing participants of %s: %w", conv.ID, err)
		}
		for _, p := range conv.Participants {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO conversation_participants (
					gmessages_conversation_id, participant_id, phone,
					normalized_phone, display_name, is_me
				) VALUES (?, ?, ?, ?, ?, ?)`,
				conv.ID, p.ID, p.Phone, NormalizePhone(p.Phone), p.DisplayName, p.IsMe)
			if err != nil {
				return fmt.Errorf("saving participant %s of %s: %w", p.ID, conv.ID, err)
			}
		}
	}

	return tx.Commit()
}

// GetConversation returns the conversation with the given Google Messages ID,
// or ErrNotFound.
func (db *DB) GetConversation(ctx context.Context, id string) (Conversation, error) {
	return db.getConversationWhere(ctx, `gmessages_conversation_id = ?`, id)
}

// GetConversationByRootPost returns the conversation whose Mattermost thread
// is rooted at postID, or ErrNotFound. This is the lookup that turns a reply
// typed in Mattermost back into a Google Messages conversation.
func (db *DB) GetConversationByRootPost(ctx context.Context, postID string) (Conversation, error) {
	if postID == "" {
		return Conversation{}, ErrNotFound
	}
	return db.getConversationWhere(ctx, `mattermost_root_post_id = ?`, postID)
}

// GetSoleConversationInChannel returns the conversation bridged into channelID
// when it is the only one there, and ErrNotFound otherwise.
//
// It exists for the thread_per_conversation = false layout, where a channel is
// dedicated to a single conversation and a reply carries no thread to look up.
// Refusing to answer when the channel holds several conversations is
// deliberate: guessing which one a reply meant would send it to the wrong
// person.
func (db *DB) GetSoleConversationInChannel(ctx context.Context, channelID string) (Conversation, error) {
	var count int
	if err := db.queryRowContext(ctx,
		`SELECT COUNT(*) FROM conversations WHERE mattermost_channel_id = ?`,
		channelID).Scan(&count); err != nil {
		return Conversation{}, err
	}
	if count != 1 {
		return Conversation{}, ErrNotFound
	}
	return db.getConversationWhere(ctx, `mattermost_channel_id = ?`, channelID)
}

// FindConversationByPhone returns a conversation with a participant at the
// given number, or ErrNotFound. Used to reuse an existing thread when the same
// contact is reached through a new conversation ID.
func (db *DB) FindConversationByPhone(ctx context.Context, phone string) (Conversation, error) {
	normalized := NormalizePhone(phone)
	if normalized == "" {
		return Conversation{}, ErrNotFound
	}
	var id string
	err := db.queryRowContext(ctx, `
		SELECT c.gmessages_conversation_id
		FROM conversations c
		JOIN conversation_participants p
		  ON p.gmessages_conversation_id = c.gmessages_conversation_id
		WHERE p.normalized_phone = ? AND p.is_me = 0 AND c.is_group = 0
		ORDER BY c.last_seen DESC
		LIMIT 1`, normalized).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Conversation{}, ErrNotFound
	}
	if err != nil {
		return Conversation{}, err
	}
	return db.GetConversation(ctx, id)
}

// ListConversations returns every mapping, most recently active first.
func (db *DB) ListConversations(ctx context.Context) ([]Conversation, error) {
	rows, err := db.queryContext(ctx, conversationSelect+` ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Conversation
	for rows.Next() {
		conv, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, conv)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range out {
		if out[i].Participants, err = db.participants(ctx, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// TouchConversation records that a conversation saw activity at ts.
func (db *DB) TouchConversation(ctx context.Context, id string, ts time.Time) error {
	_, err := db.execContext(ctx,
		`UPDATE conversations SET last_seen = ? WHERE gmessages_conversation_id = ?`,
		ts.Unix(), id)
	return err
}

const conversationSelect = `
	SELECT gmessages_conversation_id, mattermost_channel_id, mattermost_root_post_id,
	       display_name, is_group, outgoing_participant_id, conversation_type,
	       send_mode, last_seen
	FROM conversations`

func (db *DB) getConversationWhere(ctx context.Context, where string, args ...any) (Conversation, error) {
	row := db.queryRowContext(ctx, conversationSelect+` WHERE `+where+` LIMIT 1`, args...)
	conv, err := scanConversation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Conversation{}, ErrNotFound
	}
	if err != nil {
		return Conversation{}, err
	}
	if conv.Participants, err = db.participants(ctx, conv.ID); err != nil {
		return Conversation{}, err
	}
	return conv, nil
}

// scanner is what sql.Row and sql.Rows have in common.
type scanner interface {
	Scan(dest ...any) error
}

func scanConversation(s scanner) (Conversation, error) {
	var (
		conv     Conversation
		lastSeen int64
	)
	err := s.Scan(&conv.ID, &conv.ChannelID, &conv.RootPostID, &conv.DisplayName,
		&conv.IsGroup, &conv.OutgoingParticipantID, &conv.Type, &conv.SendMode, &lastSeen)
	if err != nil {
		return Conversation{}, err
	}
	if lastSeen != 0 {
		conv.LastSeen = time.Unix(lastSeen, 0)
	}
	return conv, nil
}

func (db *DB) participants(ctx context.Context, conversationID string) ([]Participant, error) {
	rows, err := db.queryContext(ctx, `
		SELECT participant_id, phone, display_name, is_me
		FROM conversation_participants
		WHERE gmessages_conversation_id = ?
		ORDER BY is_me, participant_id`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Participant
	for rows.Next() {
		var p Participant
		if err := rows.Scan(&p.ID, &p.Phone, &p.DisplayName, &p.IsMe); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
