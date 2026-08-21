// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 20:06:11
// Original filename: src/internal/bridge/conversations.go

package bridge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"msggw/internal/gmessages"
	"msggw/internal/mattermost"
	"msggw/internal/storage"
)

// ensureConversation returns the Mattermost side of a Google Messages
// conversation, creating it the first time.
//
// Creating it means: route the conversation to a destination, resolve that
// destination to a channel and — in thread mode — open the thread with a root
// post naming who the conversation is with.
func (b *Bridge) ensureConversation(ctx context.Context, conversationID string) (storage.Conversation, error) {
	unlock := b.lockConversation(conversationID)
	defer unlock()

	stored, err := b.db.GetConversation(ctx, conversationID)
	if err == nil {
		return stored, nil
	}
	if !errors.Is(err, storage.ErrNotFound) {
		return storage.Conversation{}, fmt.Errorf("looking up conversation %s: %w", conversationID, err)
	}

	conv, err := b.gm.GetConversation(ctx, conversationID)
	if err != nil {
		return storage.Conversation{}, err
	}

	destination, rule := b.router.Route(conv)
	channelID, err := b.mm.ResolveDestination(ctx, destination)
	if err != nil {
		return storage.Conversation{}, fmt.Errorf("routing conversation %q via %s: %w", conv.Title(), rule, err)
	}

	stored = storage.Conversation{
		ID:                    conv.ID,
		ChannelID:             channelID,
		DisplayName:           conv.Title(),
		IsGroup:               conv.IsGroup,
		OutgoingParticipantID: conv.OutgoingID,
		Type:                  int32(conv.Type),
		SendMode:              int32(conv.SendMode),
		LastSeen:              time.Now(),
		Participants:          participantsOf(conv),
	}

	if b.threadMode {
		rootID, err := b.mm.Post(ctx, mattermost.NewPost{
			ChannelID: channelID,
			Message:   conversationHeader(conv),
		})
		if err != nil {
			return storage.Conversation{}, fmt.Errorf("opening a Mattermost thread for %q: %w", conv.Title(), err)
		}
		stored.RootPostID = rootID
	}

	if err := b.db.SaveConversation(ctx, stored); err != nil {
		return storage.Conversation{}, fmt.Errorf("storing the mapping for conversation %s: %w", conversationID, err)
	}

	b.log.Info("bridged a new conversation",
		"conversation", conv.ID, "title", conv.Title(), "rule", rule,
		"destination", destination.String(), "channel_id", channelID,
		"root_post", stored.RootPostID)

	if count := b.cfg.GMessages.BackfillCount; count > 0 {
		b.backfill(ctx, stored, count)
	}

	return stored, nil
}

// handleConversationUpdate keeps the stored metadata in step with the phone:
// a renamed group, a changed participant list, a conversation that switched
// from SMS to RCS.
//
// It deliberately does not re-route an existing conversation. Moving a live
// thread to another channel mid-conversation would strand its history, so a
// changed routing rule only affects conversations bridged after the change.
func (b *Bridge) handleConversationUpdate(ctx context.Context, conv gmessages.Conversation) error {
	stored, err := b.db.GetConversation(ctx, conv.ID)
	if errors.Is(err, storage.ErrNotFound) {
		// Not bridged yet. It will be, when a message arrives; creating a
		// thread now would open empty threads for every conversation the phone
		// happens to mention.
		return nil
	}
	if err != nil {
		return fmt.Errorf("looking up conversation %s: %w", conv.ID, err)
	}

	title := conv.Title()
	renamed := title != "" && title != stored.DisplayName

	stored.DisplayName = title
	stored.IsGroup = conv.IsGroup
	stored.Type = int32(conv.Type)
	stored.SendMode = int32(conv.SendMode)
	if conv.OutgoingID != "" {
		stored.OutgoingParticipantID = conv.OutgoingID
	}
	if len(conv.Participants) > 0 {
		stored.Participants = participantsOf(conv)
	}

	if err := b.db.SaveConversation(ctx, stored); err != nil {
		return fmt.Errorf("updating conversation %s: %w", conv.ID, err)
	}

	if renamed && stored.RootPostID != "" {
		if err := b.mm.UpdateMessage(ctx, stored.RootPostID, conversationHeader(conv)); err != nil {
			// The mapping is already saved; a stale thread title is cosmetic.
			b.log.Warn("could not rename a bridged thread",
				"conversation", conv.ID, "title", title, "error", err)
		} else {
			b.log.Info("renamed a bridged thread", "conversation", conv.ID, "title", title)
		}
	}

	return nil
}

// backfill posts the tail of a conversation's history when it is first
// bridged, oldest first so the thread reads in order.
func (b *Bridge) backfill(ctx context.Context, stored storage.Conversation, count int) {
	messages, err := b.gm.FetchMessages(ctx, stored.ID, count)
	if err != nil {
		b.log.Warn("could not backfill a conversation", "conversation", stored.ID, "error", err)
		return
	}

	// FetchMessages returns newest first.
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if !msg.HasContent() {
			continue
		}
		if err := b.postMessage(ctx, stored, msg); err != nil {
			b.log.Warn("could not backfill a message",
				"conversation", stored.ID, "message", msg.ID, "error", err)
			return
		}
	}
	b.log.Info("backfilled a conversation", "conversation", stored.ID, "messages", len(messages))
}

// conversationHeader is the text of the root post that stands for a
// conversation. It names who the conversation is with and how it is carried,
// so a channel holding several threads is readable at a glance.
func conversationHeader(conv gmessages.Conversation) string {
	var b strings.Builder

	fmt.Fprintf(&b, "#### %s\n", conv.Title())

	kind := "SMS/MMS"
	if conv.Type.String() == "RCS" {
		kind = "RCS"
	}
	if conv.IsGroup {
		kind = "group " + kind
	}
	fmt.Fprintf(&b, "_%s conversation", kind)

	if numbers := participantNumbers(conv); numbers != "" {
		fmt.Fprintf(&b, " with %s", numbers)
	}
	b.WriteString("_")

	return b.String()
}

// participantNumbers lists the other parties' numbers, which is the detail a
// contact name hides and the one that matters when replying.
func participantNumbers(conv gmessages.Conversation) string {
	others := conv.OtherParticipants()
	numbers := make([]string, 0, len(others))
	for _, p := range others {
		if p.Phone != "" {
			numbers = append(numbers, p.Phone)
		}
	}
	return strings.Join(numbers, ", ")
}

func participantsOf(conv gmessages.Conversation) []storage.Participant {
	out := make([]storage.Participant, 0, len(conv.Participants))
	for _, p := range conv.Participants {
		out = append(out, storage.Participant{
			ID:          p.ID,
			Phone:       p.Phone,
			DisplayName: p.DisplayName,
			IsMe:        p.IsMe,
		})
	}
	return out
}
