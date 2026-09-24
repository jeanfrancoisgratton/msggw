// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/internal/corebridge/conversations.go

package corebridge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"msggw/internal/mattermost"
	"msggw/internal/storage"
	"msggw/internal/transport"
)

// ensureConversation returns the Mattermost side of a conversation, creating
// it the first time.
//
// Creating it means: route the conversation to a destination, resolve that
// destination to a Mattermost channel and — in thread mode — open the thread
// with a root post naming the conversation.
func (b *Bridge) ensureConversation(ctx context.Context, conversationID string) (storage.Conversation, error) {
	unlock := b.lockConversation(conversationID)
	defer unlock()

	stored, err := b.db.GetConversation(ctx, b.tenant, conversationID)
	if err == nil {
		return stored, nil
	}
	if !errors.Is(err, storage.ErrNotFound) {
		return storage.Conversation{}, fmt.Errorf("looking up conversation %s: %w", conversationID, err)
	}

	conv, err := b.tp.Conversation(ctx, conversationID)
	if err != nil {
		return storage.Conversation{}, err
	}

	destination, rule := b.router.Route(conv)
	channelID, err := b.mm.ResolveDestination(ctx, destination, b.cfg.JoinChannels)
	if err != nil {
		return storage.Conversation{}, fmt.Errorf("routing conversation %q via %s: %w", conv.Title(), rule, err)
	}

	stored = storage.Conversation{
		ID:                    conv.ID,
		ChannelID:             channelID,
		DisplayName:           conv.Title(),
		IsGroup:               conv.IsGroup,
		OutgoingParticipantID: conv.OutgoingID,
		LastSeen:              time.Now(),
		Participants:          participantsOf(conv),
	}

	if b.cfg.ThreadPerConversation {
		rootID, err := b.mm.Post(ctx, mattermost.NewPost{
			ChannelID: channelID,
			Message:   conversationHeader(conv),
		})
		if err != nil {
			return storage.Conversation{}, fmt.Errorf("opening a Mattermost thread for %q: %w", conv.Title(), err)
		}
		stored.RootPostID = rootID
	}

	if err := b.db.SaveConversation(ctx, b.tenant, stored); err != nil {
		return storage.Conversation{}, fmt.Errorf("storing the mapping for conversation %s: %w", conversationID, err)
	}

	b.log.Info("bridged a new conversation",
		"conversation", conv.ID, "title", conv.Title(), "rule", rule,
		"destination", destination.String(), "channel_id", channelID,
		"root_post", stored.RootPostID)

	return stored, nil
}

// conversationHeader is the text of the root post that stands for a
// conversation. It names who the conversation is with and how it is
// carried, so a Mattermost channel holding several threads is readable at a
// glance. It has to work for both a gmessages conversation (which populates
// Kind and participants' Phone) and a Discord one (which populates neither,
// and distinguishes a DM from a channel purely by IsGroup).
func conversationHeader(conv transport.Conversation) string {
	var b strings.Builder

	fmt.Fprintf(&b, "#### %s\n", conv.Title())
	fmt.Fprintf(&b, "_%s", kindLabel(conv))
	if others := participantLabels(conv); others != "" {
		fmt.Fprintf(&b, " with %s", others)
	}
	b.WriteString("_")

	return b.String()
}

// kindLabel names how the conversation is carried.
func kindLabel(conv transport.Conversation) string {
	if conv.Kind == "" {
		// Discord never populates Kind; its own shape says DM vs channel
		// instead.
		if conv.IsGroup {
			return "Discord channel"
		}
		return "Discord DM"
	}
	kind := "SMS/MMS"
	if conv.Kind == "RCS" {
		kind = "RCS"
	}
	if conv.IsGroup {
		kind = "group " + kind
	}
	return kind + " conversation"
}

// participantLabels lists the other parties, preferring a phone number
// (gmessages) and falling back to a display name (Discord, or a gmessages
// participant with no number on file).
func participantLabels(conv transport.Conversation) string {
	others := conv.OtherParticipants()
	labels := make([]string, 0, len(others))
	for _, p := range others {
		switch {
		case p.Phone != "":
			labels = append(labels, p.Phone)
		case p.DisplayName != "":
			labels = append(labels, p.DisplayName)
		}
	}
	return strings.Join(labels, ", ")
}

func participantsOf(conv transport.Conversation) []storage.Participant {
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
