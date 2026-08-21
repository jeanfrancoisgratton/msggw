// rcs_gateway
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 19:47:53
// Original filename: src/internal/gmessages/types.go

package gmessages

import (
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

// Kind is what carried a message. The distinction matters because the whole
// point of this bridge is RCS: an operator looking at a post should be able to
// tell whether it actually travelled over RCS or fell back to SMS.
type Kind string

const (
	KindSMS     Kind = "SMS"
	KindMMS     Kind = "MMS"
	KindRCS     Kind = "RCS"
	KindUnknown Kind = "unknown"
)

// kindOf maps the message's type field. The values are the ones libgm
// documents in conversations.proto: 1 = SMS, 2 = downloaded MMS,
// 3 = undownloaded MMS, 4 = RCS.
func kindOf(messageType int64, conversationType gmproto.ConversationType) Kind {
	switch messageType {
	case 1:
		return KindSMS
	case 2, 3:
		return KindMMS
	case 4:
		return KindRCS
	}
	// Some events arrive without the type field set; the conversation's own
	// type is the next best answer.
	switch conversationType {
	case gmproto.ConversationType_RCS:
		return KindRCS
	case gmproto.ConversationType_SMS:
		return KindSMS
	}
	return KindUnknown
}

// Status is a message's delivery state, mirroring gmproto.MessageStatusType.
// Outgoing states are below 100 and incoming ones at or above it.
type Status int32

func (s Status) String() string { return gmproto.MessageStatusType(s).String() }

// Outgoing reports whether the status describes a message we sent.
func (s Status) Outgoing() bool { return s > 0 && s < 100 }

// Delivered reports whether the recipient's device acknowledged the message.
func (s Status) Delivered() bool {
	switch gmproto.MessageStatusType(s) {
	case gmproto.MessageStatusType_OUTGOING_DELIVERED,
		gmproto.MessageStatusType_OUTGOING_DISPLAYED:
		return true
	}
	return false
}

// Read reports whether the recipient displayed the message. RCS reports this;
// plain SMS does not.
func (s Status) Read() bool {
	return gmproto.MessageStatusType(s) == gmproto.MessageStatusType_OUTGOING_DISPLAYED
}

// Failed reports whether the message will not be delivered. The list covers
// every OUTGOING_FAILED_* state plus the cancellations, since from
// Mattermost's point of view they all mean "this did not reach the recipient".
func (s Status) Failed() bool {
	switch gmproto.MessageStatusType(s) {
	case gmproto.MessageStatusType_OUTGOING_FAILED_GENERIC,
		gmproto.MessageStatusType_OUTGOING_FAILED_EMERGENCY_NUMBER,
		gmproto.MessageStatusType_OUTGOING_CANCELED,
		gmproto.MessageStatusType_OUTGOING_FAILED_TOO_LARGE,
		gmproto.MessageStatusType_OUTGOING_FAILED_RECIPIENT_LOST_RCS,
		gmproto.MessageStatusType_OUTGOING_FAILED_NO_RETRY_NO_FALLBACK,
		gmproto.MessageStatusType_OUTGOING_FAILED_RECIPIENT_DID_NOT_DECRYPT,
		gmproto.MessageStatusType_OUTGOING_FAILED_RECIPIENT_LOST_ENCRYPTION,
		gmproto.MessageStatusType_OUTGOING_FAILED_RECIPIENT_DID_NOT_DECRYPT_NO_MORE_RETRY,
		gmproto.MessageStatusType_OUTGOING_FAILED_RECIPIENT_NEGATIVE_DELIVERY,
		gmproto.MessageStatusType_MESSAGE_STATUS_OUTGOING_FAILED_EMERGENCY_PROTOCOL_DETERMINATION_MESSAGE,
		gmproto.MessageStatusType_OUTGOING_FAILED_TO_ENCRYPT,
		gmproto.MessageStatusType_INCOMING_DOWNLOAD_FAILED,
		gmproto.MessageStatusType_INCOMING_DOWNLOAD_FAILED_TOO_LARGE,
		gmproto.MessageStatusType_INCOMING_DOWNLOAD_FAILED_SIM_HAS_NO_DATA,
		gmproto.MessageStatusType_INCOMING_FAILED_TO_DECRYPT,
		gmproto.MessageStatusType_INCOMING_EXPIRED_OR_NOT_AVAILABLE:
		return true
	}
	return false
}

// Pending reports whether the message is still on its way out, so that a
// status update can be ignored until it settles.
func (s Status) Pending() bool {
	switch gmproto.MessageStatusType(s) {
	case gmproto.MessageStatusType_OUTGOING_YET_TO_SEND,
		gmproto.MessageStatusType_OUTGOING_SENDING,
		gmproto.MessageStatusType_OUTGOING_RESENDING,
		gmproto.MessageStatusType_OUTGOING_AWAITING_RETRY,
		gmproto.MessageStatusType_OUTGOING_SEND_AFTER_PROCESSING,
		gmproto.MessageStatusType_OUTGOING_VALIDATING,
		gmproto.MessageStatusType_OUTGOING_SCHEDULED:
		return true
	}
	return false
}

// Participant is one party to a conversation.
type Participant struct {
	// ID is the Google Messages participant ID, which is what outgoing
	// messages and typing notifications are addressed with.
	ID string
	// Phone is the number as Google Messages formats it, e.g. "+1 514-555-1212".
	Phone       string
	DisplayName string
	// IsMe marks the phone's own identity, one per SIM.
	IsMe bool
}

// Conversation is one Google Messages thread.
type Conversation struct {
	ID   string
	Name string
	// IsGroup is true for group SMS/MMS and group RCS chats.
	IsGroup bool
	Type    gmproto.ConversationType
	// SendMode says whether the phone will latch this conversation to SMS.
	SendMode gmproto.ConversationSendMode
	// OutgoingID is the participant ID the phone expects as the sender of
	// messages in this conversation. On a dual-SIM phone it identifies the SIM.
	OutgoingID   string
	Participants []Participant
	LastActivity time.Time
	Unread       bool
}

// OtherParticipants returns everyone but us, which is who the conversation is
// actually with.
func (c Conversation) OtherParticipants() []Participant {
	out := make([]Participant, 0, len(c.Participants))
	for _, p := range c.Participants {
		if !p.IsMe {
			out = append(out, p)
		}
	}
	return out
}

// Title is the name to show in Mattermost: the conversation's own name when it
// has one (a contact name, or a group title), and otherwise the participants'
// numbers.
func (c Conversation) Title() string {
	if c.Name != "" {
		return c.Name
	}
	others := c.OtherParticipants()
	switch len(others) {
	case 0:
		return c.ID
	case 1:
		if others[0].DisplayName != "" {
			return others[0].DisplayName
		}
		return others[0].Phone
	default:
		names := make([]string, 0, len(others))
		for _, p := range others {
			if p.DisplayName != "" {
				names = append(names, p.DisplayName)
			} else {
				names = append(names, p.Phone)
			}
		}
		return joinAnd(names)
	}
}

// Media is one attachment on a message.
type Media struct {
	ID          string
	ThumbnailID string
	Name        string
	MimeType    string
	Size        int64
	Width       int64
	Height      int64

	DecryptionKey          []byte
	ThumbnailDecryptionKey []byte
}

// Pending reports whether the phone has not finished making the attachment
// available yet — an MMS it has not downloaded, typically. There is nothing to
// fetch in that state; a later update to the same message carries the real IDs.
func (m Media) Pending() bool { return m.ID == "" && m.ThumbnailID == "" }

// Message is one message in a conversation.
type Message struct {
	ID             string
	ConversationID string
	// TmpID is the temporary ID the sender chose. For messages this daemon
	// sent, it is how the echoed message is recognised as our own.
	TmpID string

	ParticipantID string
	SenderName    string
	SenderPhone   string
	IsFromMe      bool

	Timestamp time.Time
	Status    Status
	Kind      Kind

	Subject string
	Text    string
	Media   []Media

	// ReplyToID is the Google Messages message being replied to, for RCS
	// replies.
	ReplyToID string

	// IsOld marks a message replayed from the phone's history rather than one
	// that just arrived. libgm sets it while catching up after a reconnect.
	IsOld bool
}

// HasContent reports whether there is anything worth posting. Status-only
// updates arrive as messages with neither text nor media.
func (m Message) HasContent() bool { return m.Text != "" || m.Subject != "" || len(m.Media) > 0 }

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
