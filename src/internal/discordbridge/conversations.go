// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/internal/discordbridge/conversations.go

package discordbridge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"msggw/internal/discord"
	"msggw/internal/mattermost"
	"msggw/internal/storage"
)

// ensureConversation returns the Mattermost side of a Discord channel,
// creating it the first time.
//
// Creating it means: route the channel to a destination, resolve that
// destination to a Mattermost channel and — in thread mode — open the
// thread with a root post naming the channel. There is no handleConversationUpdate
// equivalent (contrast bridge.Bridge, which reacts to Google Messages'
// ConversationEvent): this v1 adapter does not watch for Discord channel
// renames — see docs/discord-adapter-design.md.
func (b *Bridge) ensureConversation(ctx context.Context, channelID string) (storage.Conversation, error) {
	unlock := b.lockConversation(channelID)
	defer unlock()

	stored, err := b.db.GetConversation(ctx, tenant, channelID)
	if err == nil {
		return stored, nil
	}
	if !errors.Is(err, storage.ErrNotFound) {
		return storage.Conversation{}, fmt.Errorf("looking up channel %s: %w", channelID, err)
	}

	conv, err := b.dc.Conversation(ctx, channelID)
	if err != nil {
		return storage.Conversation{}, err
	}

	destination, rule := b.router.Route(conv)
	mmChannelID, err := b.mm.ResolveDestination(ctx, destination, b.cfg.Routing.JoinChannels)
	if err != nil {
		return storage.Conversation{}, fmt.Errorf("routing channel %q via %s: %w", conv.Title(), rule, err)
	}

	stored = storage.Conversation{
		ID:           conv.ID,
		ChannelID:    mmChannelID,
		DisplayName:  conv.Title(),
		IsGroup:      conv.IsGroup,
		LastSeen:     time.Now(),
		Participants: participantsOf(conv),
	}

	if b.threadMode {
		rootID, err := b.mm.Post(ctx, mattermost.NewPost{
			ChannelID: mmChannelID,
			Message:   conversationHeader(conv),
		})
		if err != nil {
			return storage.Conversation{}, fmt.Errorf("opening a Mattermost thread for %q: %w", conv.Title(), err)
		}
		stored.RootPostID = rootID
	}

	if err := b.db.SaveConversation(ctx, tenant, stored); err != nil {
		return storage.Conversation{}, fmt.Errorf("storing the mapping for channel %s: %w", channelID, err)
	}

	b.log.Info("bridged a new Discord channel",
		"channel", conv.ID, "title", conv.Title(), "rule", rule,
		"destination", destination.String(), "mattermost_channel_id", mmChannelID,
		"root_post", stored.RootPostID)

	return stored, nil
}

// conversationHeader is the text of the root post that stands for a Discord
// channel. It names what the channel is and who else is in it (for a DM/
// group DM), so a Mattermost channel holding several threads is readable at
// a glance — mirrors bridge.Bridge's conversationHeader for Google Messages.
func conversationHeader(conv discord.Conversation) string {
	var b strings.Builder

	fmt.Fprintf(&b, "#### %s\n", conv.Title())

	kind := "Discord DM"
	if conv.IsGroup {
		kind = "Discord channel"
	}
	fmt.Fprintf(&b, "_%s", kind)
	if names := participantNames(conv); names != "" {
		fmt.Fprintf(&b, " with %s", names)
	}
	b.WriteString("_")

	return b.String()
}

// participantNames lists the other parties' display names, for a DM/group
// DM header. A guild channel's participants are not known upfront (see
// discord.Conversation's doc comment), so this is typically empty there.
func participantNames(conv discord.Conversation) string {
	others := conv.OtherParticipants()
	names := make([]string, 0, len(others))
	for _, p := range others {
		if p.DisplayName != "" {
			names = append(names, p.DisplayName)
		}
	}
	return strings.Join(names, ", ")
}

func participantsOf(conv discord.Conversation) []storage.Participant {
	out := make([]storage.Participant, 0, len(conv.Participants))
	for _, p := range conv.Participants {
		out = append(out, storage.Participant{
			ID:          p.ID,
			DisplayName: p.DisplayName,
			IsMe:        p.IsMe,
		})
	}
	return out
}
