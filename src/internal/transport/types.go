// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/internal/transport/types.go

// Package transport is the normalized model every backend adapter
// (internal/gmessages, internal/discord, and whatever comes next) translates
// its own protocol into, and the interface the generic bridge core
// (internal/corebridge) is built against instead of any one adapter's
// concrete types. See docs/msggw-rcs-mattermost-discord.md for the
// architectural rationale.
//
// The shapes here are a literal least-common-superset merge of
// gmessages.Conversation/Message/Event and discord.Conversation/Message/Event,
// which had already independently converged on nearly the same fields —
// converting either adapter's own types to these is a mechanical field copy,
// not a redesign. A field only one adapter populates is simply left at its
// zero value by the other (e.g. Participant.Phone is always "" for Discord;
// Conversation.GuildID is always "" for Google Messages).
package transport

import "time"

// Participant is one party to a conversation.
type Participant struct {
	// ID is the adapter's own identifier: a Discord user ID, or a Google
	// Messages participant ID.
	ID string
	// Phone is populated by gmessages only; empty for Discord.
	Phone       string
	DisplayName string
	// IsMe marks the bridged identity itself (the phone, or the bot).
	IsMe bool
}

// Conversation is one bridged thread, whatever an adapter's own hierarchy
// calls it: an SMS/RCS conversation, a Discord channel, a Mattermost team+
// channel pair.
type Conversation struct {
	ID   string
	Name string
	// IsGroup distinguishes a group conversation from a 1:1 one — the same
	// distinction every adapter's routing rules use for their GroupsOnly/
	// DirectsOnly shape filters.
	IsGroup bool
	// GuildID is populated by Discord only (empty for a DM/group DM, and
	// always empty for Google Messages).
	GuildID      string
	Participants []Participant

	// Kind, OutgoingID, LastActivity and Unread are gmessages-only metadata,
	// kept as adapter-opaque plain values (not gmproto types) the same way
	// gmessages.Conversation already does, so transport stays free of any
	// one adapter's wire types. All are zero for Discord.
	Kind         string
	OutgoingID   string
	LastActivity time.Time
	Unread       bool
}

// OtherParticipants returns everyone but the bridged identity itself, which
// is who the conversation is actually with.
func (c Conversation) OtherParticipants() []Participant {
	out := make([]Participant, 0, len(c.Participants))
	for _, p := range c.Participants {
		if !p.IsMe {
			out = append(out, p)
		}
	}
	return out
}

// Title is the name to show in Mattermost: the conversation's own name when
// it has one, and otherwise the other participants' display names (falling
// back to their phone number when a gmessages participant has no name).
func (c Conversation) Title() string {
	if c.Name != "" {
		return c.Name
	}
	others := c.OtherParticipants()
	switch len(others) {
	case 0:
		return c.ID
	case 1:
		return others[0].label()
	default:
		names := make([]string, 0, len(others))
		for _, p := range others {
			names = append(names, p.label())
		}
		return joinAnd(names)
	}
}

func (p Participant) label() string {
	if p.DisplayName != "" {
		return p.DisplayName
	}
	return p.Phone
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

// Attachment is a file already on the backend, as carried by an inbound
// message.
type Attachment struct {
	ID       string
	Name     string
	MimeType string
	// URL is a public HTTPS URL for a backend (Discord) that offers one;
	// empty for a backend (Google Messages) that requires an authenticated
	// fetch instead — see Transport.Download.
	URL  string
	Size int64
	// Opaque is adapter-private data the generic core never reads or sets.
	// gmessages' adapter uses it to carry a *gmessages.Media (which has
	// decryption-key fields with no cross-adapter meaning) through to its
	// own Download implementation.
	Opaque any
}

// Upload is a file on its way out to a backend.
type Upload struct {
	Name     string
	MimeType string
	Data     []byte
}

// Message is one message in a conversation.
type Message struct {
	ID             string
	ConversationID string
	// GuildID is populated by Discord only.
	GuildID string

	SenderID string
	// SenderName is the display name to show as the message's author.
	SenderName string
	// SenderPhone is populated by gmessages only; empty for Discord.
	SenderPhone string
	// IsFromMe marks a message sent by the bridged identity itself (the
	// phone, on another device; never true for Discord, which filters the
	// bot's own messages out before they ever reach this layer).
	IsFromMe bool

	Timestamp time.Time
	Text      string
	// Subject is a gmessages-only MMS subject line; always "" for Discord.
	Subject string
	// Kind labels what carried the message (gmessages: "SMS"/"MMS"/"RCS"/
	// "unknown"; always "" for Discord, which has no such distinction).
	Kind string

	Attachments []Attachment
	// ReplyToID is the message being replied to, if any.
	ReplyToID string
	// IsOld marks a message replayed from history rather than one that just
	// arrived (gmessages: set while catching up after a reconnect; always
	// false for Discord).
	IsOld bool
}

// HasContent reports whether there is anything worth posting. A status-only
// update, or a message that is only a Discord embed/sticker, has neither
// text nor an attachment and is treated as nothing to post.
func (m Message) HasContent() bool {
	return m.Text != "" || m.Subject != "" || len(m.Attachments) > 0
}
