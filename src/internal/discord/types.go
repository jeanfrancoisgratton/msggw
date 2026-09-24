// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/internal/discord/types.go

package discord

import "time"

// Participant is one member of a conversation, best-effort: unlike Google
// Messages, Discord does not hand over a channel's full member list cheaply,
// so this is populated from message authors seen so far, not authoritative
// at conversation-creation time. See docs/discord-adapter-design.md.
type Participant struct {
	// ID is the Discord user ID.
	ID          string
	DisplayName string
	// IsMe marks the bot's own identity.
	IsMe bool
}

// Conversation is one Discord channel: a guild text channel, a DM, or a
// group DM.
type Conversation struct {
	// ID is the Discord channel ID.
	ID string
	// GuildID is empty for a DM or group DM.
	GuildID string
	Name    string
	// IsGroup is true for a guild channel or a group DM; false for a 1:1 DM
	// — the same distinction DiscordRule.GroupsOnly/DirectsOnly use.
	IsGroup      bool
	Participants []Participant
}

// OtherParticipants returns everyone but the bot itself.
func (c Conversation) OtherParticipants() []Participant {
	out := make([]Participant, 0, len(c.Participants))
	for _, p := range c.Participants {
		if !p.IsMe {
			out = append(out, p)
		}
	}
	return out
}

// Title is the name to show in Mattermost: the channel's own name when it
// has one (a guild channel, or a named group DM), and otherwise the other
// participants' display names.
func (c Conversation) Title() string {
	if c.Name != "" {
		return c.Name
	}
	others := c.OtherParticipants()
	switch len(others) {
	case 0:
		return c.ID
	case 1:
		return others[0].DisplayName
	default:
		names := make([]string, 0, len(others))
		for _, p := range others {
			names = append(names, p.DisplayName)
		}
		return joinAnd(names)
	}
}

func joinAnd(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	out := names[0]
	for _, n := range names[1 : len(names)-1] {
		out += ", " + n
	}
	return out + " and " + names[len(names)-1]
}

// Attachment is a file already on Discord, as carried by an inbound
// message. Unlike Google Messages media, Discord attachments are available
// immediately over a public HTTPS URL — no pending/thumbnail states.
type Attachment struct {
	ID       string
	Name     string
	MimeType string
	URL      string
	Size     int64
}

// Upload is a file on its way to Discord, mirroring gmessages.Attachment.
type Upload struct {
	Name     string
	MimeType string
	Data     []byte
}

// Message is one message in a conversation.
type Message struct {
	ID             string
	ConversationID string
	GuildID        string

	AuthorID   string
	AuthorName string
	// IsFromMe marks a message sent by the bot itself. The discord package
	// filters these out before ever emitting a MessageEvent (see events.go),
	// so in practice a Message reaching the bridge always has this false;
	// the field exists for symmetry with gmessages.Message and in case a
	// future caller (e.g. backfill via the REST history endpoint) needs it.
	IsFromMe bool

	Timestamp   time.Time
	Text        string
	Attachments []Attachment

	// ReplyToID is the Discord message being replied to, if any.
	ReplyToID string
}

// HasContent reports whether there is anything worth posting. A message
// carrying only an embed or a sticker (no text, no attachments) is a known
// limitation of v1 — see docs/discord-adapter-design.md — and is treated as
// having no content rather than posting an empty line to Mattermost.
func (m Message) HasContent() bool { return m.Text != "" || len(m.Attachments) > 0 }
