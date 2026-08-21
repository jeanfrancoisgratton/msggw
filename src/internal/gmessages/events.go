// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 19:49:15
// Original filename: src/internal/gmessages/events.go

package gmessages

import (
	"fmt"
	"sync"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/events"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

// Event is what the bridge consumes. The libgm event set is translated into
// this smaller vocabulary so that a protocol change shows up here rather than
// throughout the bridge.
type Event interface{ isGMEvent() }

// ReadyEvent is emitted once the paired session is up and the conversation
// list has been received.
type ReadyEvent struct {
	SessionID     string
	Conversations []Conversation
}

// MessageEvent carries one message, incoming or outgoing. Google Messages
// reports its own sent messages the same way, which is how a reply typed on
// the phone still reaches Mattermost.
type MessageEvent struct {
	Message Message
}

// ConversationEvent reports a conversation's metadata changing: a new
// conversation, a renamed group, a changed participant list.
type ConversationEvent struct {
	Conversation Conversation
}

// PairedEvent is emitted when pairing completes.
type PairedEvent struct {
	PhoneID string
}

// LoggedOutEvent means the session is gone and the daemon has to be re-paired.
// Reason is human-readable.
type LoggedOutEvent struct {
	Reason string
}

// ConnectionEvent reports the health of the link to the phone, so the daemon
// can log it and the status command can report it.
type ConnectionEvent struct {
	// State is one of the Conn* constants.
	State string
	// Err is set for the error states.
	Err error
}

// Connection states.
const (
	ConnPhoneUnreachable = "phone_unreachable"
	ConnPhoneReachable   = "phone_reachable"
	ConnTemporaryError   = "temporary_error"
	ConnRecovered        = "recovered"
	ConnFatalError       = "fatal_error"
	ConnNoData           = "no_data_received"
)

// AlertEvent passes through the phone's own status alerts — RCS came up, the
// phone is syncing its database, another browser session took over.
type AlertEvent struct {
	Type gmproto.AlertType
}

func (ReadyEvent) isGMEvent()        {}
func (MessageEvent) isGMEvent()      {}
func (ConversationEvent) isGMEvent() {}
func (PairedEvent) isGMEvent()       {}
func (LoggedOutEvent) isGMEvent()    {}
func (ConnectionEvent) isGMEvent()   {}
func (AlertEvent) isGMEvent()        {}

// ---------------------------------------------------------------------------
// translation
// ---------------------------------------------------------------------------

// convertConversation turns the protocol's conversation into ours.
func convertConversation(c *gmproto.Conversation) Conversation {
	conv := Conversation{
		ID:         c.GetConversationID(),
		Name:       c.GetName(),
		IsGroup:    c.GetIsGroupChat(),
		Type:       c.GetType(),
		SendMode:   c.GetSendMode(),
		OutgoingID: c.GetDefaultOutgoingID(),
		Unread:     c.GetUnread(),
	}
	if ts := c.GetLastMessageTimestamp(); ts != 0 {
		conv.LastActivity = microseconds(ts)
	}
	for _, p := range c.GetParticipants() {
		conv.Participants = append(conv.Participants, convertParticipant(p))
	}
	return conv
}

func convertParticipant(p *gmproto.Participant) Participant {
	name := p.GetFullName()
	if name == "" {
		name = p.GetFirstName()
	}
	phone := p.GetFormattedNumber()
	if phone == "" {
		phone = p.GetID().GetNumber()
	}
	return Participant{
		ID:          p.GetID().GetParticipantID(),
		Phone:       phone,
		DisplayName: name,
		IsMe:        p.GetIsMe(),
	}
}

// convertMessage turns a protocol message into ours. conv may be the zero
// value: it only supplies the fallback message kind and the sender's name when
// the message itself does not carry a participant.
func convertMessage(m *gmproto.Message, isOld bool, conv Conversation) Message {
	msg := Message{
		ID:             m.GetMessageID(),
		ConversationID: m.GetConversationID(),
		TmpID:          m.GetTmpID(),
		ParticipantID:  m.GetParticipantID(),
		Status:         Status(m.GetMessageStatus().GetStatus()),
		Kind:           kindOf(m.GetType(), conv.Type),
		Subject:        m.GetSubject(),
		ReplyToID:      m.GetReplyMessage().GetMessageID(),
		IsOld:          isOld,
	}
	if ts := m.GetTimestamp(); ts != 0 {
		msg.Timestamp = microseconds(ts)
	} else {
		msg.Timestamp = time.Now()
	}

	// A message's own senderParticipant is authoritative; the conversation's
	// participant list is the fallback, and matters for group chats where the
	// sender is not the person the conversation is named after.
	if sender := m.GetSenderParticipant(); sender != nil {
		p := convertParticipant(sender)
		msg.SenderName, msg.SenderPhone, msg.IsFromMe = p.DisplayName, p.Phone, p.IsMe
	} else {
		for _, p := range conv.Participants {
			if p.ID == msg.ParticipantID {
				msg.SenderName, msg.SenderPhone, msg.IsFromMe = p.DisplayName, p.Phone, p.IsMe
				break
			}
		}
	}
	// An outgoing status on a message with no participant match still tells us
	// the message is ours.
	if msg.Status.Outgoing() {
		msg.IsFromMe = true
	}

	for _, info := range m.GetMessageInfo() {
		switch data := info.GetData().(type) {
		case *gmproto.MessageInfo_MessageContent:
			if text := data.MessageContent.GetContent(); text != "" {
				if msg.Text != "" {
					msg.Text += "\n"
				}
				msg.Text += text
			}
		case *gmproto.MessageInfo_MediaContent:
			msg.Media = append(msg.Media, convertMedia(data.MediaContent))
		}
	}

	return msg
}

func convertMedia(m *gmproto.MediaContent) Media {
	return Media{
		ID:                     m.GetMediaID(),
		ThumbnailID:            m.GetThumbnailMediaID(),
		Name:                   m.GetMediaName(),
		MimeType:               m.GetMimeType(),
		Size:                   m.GetSize(),
		Width:                  m.GetDimensions().GetWidth(),
		Height:                 m.GetDimensions().GetHeight(),
		DecryptionKey:          m.GetDecryptionKey(),
		ThumbnailDecryptionKey: m.GetThumbnailDecryptionKey(),
	}
}

// microseconds converts a Google Messages timestamp. They are microseconds
// since the epoch, not milliseconds — a message would otherwise be dated some
// time in the year 3000.
func microseconds(ts int64) time.Time { return time.UnixMicro(ts) }

// handleLibgmEvent is libgm's event handler. It runs on libgm's long-polling
// goroutine, so it does no work beyond translation and enqueueing.
func (c *Client) handleLibgmEvent(raw any) {
	switch evt := raw.(type) {
	case *events.ClientReady:
		conversations := make([]Conversation, 0, len(evt.Conversations))
		for _, gmConv := range evt.Conversations {
			conv := convertConversation(gmConv)
			c.cacheConversation(gmConv)
			conversations = append(conversations, conv)
		}
		c.setConnected(true)
		c.emit(ReadyEvent{SessionID: evt.SessionID, Conversations: conversations})

	case *events.AuthTokenRefreshed:
		// The refreshed token is already in AuthData; persisting it here is
		// what makes a restart cheap instead of a re-pairing.
		if err := c.saveSession(); err != nil {
			c.log.Error("persisting refreshed Google Messages session", "error", err)
		} else {
			c.log.Debug("persisted refreshed Google Messages session")
		}

	case *events.PairSuccessful:
		c.session.SetPhoneID(evt.PhoneID)
		if err := c.saveSession(); err != nil {
			c.log.Error("persisting Google Messages session after pairing", "error", err)
		}
		c.emit(PairedEvent{PhoneID: evt.PhoneID})

	case *libgm.WrappedMessage:
		conv, _ := c.conversation(evt.GetConversationID())
		c.emit(MessageEvent{Message: convertMessage(evt.Message, evt.IsOld, conv)})

	case *gmproto.Conversation:
		c.cacheConversation(evt)
		c.emit(ConversationEvent{Conversation: convertConversation(evt)})

	case *gmproto.UserAlertEvent:
		c.emit(AlertEvent{Type: evt.GetAlertType()})

	case *gmproto.Settings:
		// Settings carry the SIM cards. They are cached so that outgoing
		// messages can name the right SIM on a dual-SIM phone.
		c.cacheSettings(evt)

	case *gmproto.RevokePairData:
		c.setConnected(false)
		c.emit(LoggedOutEvent{Reason: fmt.Sprintf("the pairing was revoked from %s",
			evt.GetRevokedDevice().GetSourceID())})

	case *events.GaiaLoggedOut:
		c.setConnected(false)
		c.emit(LoggedOutEvent{Reason: "the Google account session was logged out"})

	case *events.AccountChange:
		if !evt.GetEnabled() {
			c.setConnected(false)
			c.emit(LoggedOutEvent{Reason: "the Google account was switched or disabled on the phone"})
		}

	case *events.ListenFatalError:
		c.setConnected(false)
		c.emit(ConnectionEvent{State: ConnFatalError, Err: evt.Error})

	case *events.ListenTemporaryError:
		c.emit(ConnectionEvent{State: ConnTemporaryError, Err: evt.Error})

	case *events.ListenRecovered:
		c.emit(ConnectionEvent{State: ConnRecovered})

	case *events.PhoneNotResponding:
		c.emit(ConnectionEvent{State: ConnPhoneUnreachable})

	case *events.PhoneRespondingAgain:
		c.emit(ConnectionEvent{State: ConnPhoneReachable})

	case *events.NoDataReceived:
		c.emit(ConnectionEvent{State: ConnNoData})

	case *events.PingFailed:
		c.emit(ConnectionEvent{State: ConnTemporaryError, Err: evt.Error})

	case *gmproto.TypingData:
		// Typing indicators are Phase 7 work; ignoring them keeps the log
		// readable in the meantime.

	default:
		c.log.Debug("ignoring unhandled Google Messages event", "type", fmt.Sprintf("%T", raw))
	}
}

// ---------------------------------------------------------------------------
// event queue
// ---------------------------------------------------------------------------

// queue is an unbounded FIFO between libgm's goroutine and the bridge.
//
// It is unbounded on purpose. A fixed channel would force a choice between
// dropping messages when Mattermost is slow — unacceptable for a message
// bridge — and blocking libgm's long-polling goroutine, which would stall its
// pings and make the phone consider the session dead.
type queue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	items  []Event
	closed bool
}

func newQueue() *queue {
	q := &queue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *queue) push(evt Event) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.items = append(q.items, evt)
	q.cond.Signal()
}

// pop blocks until an event is available, and returns ok == false once the
// queue is closed and drained.
func (q *queue) pop() (Event, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) == 0 && !q.closed {
		q.cond.Wait()
	}
	if len(q.items) == 0 {
		return nil, false
	}
	evt := q.items[0]
	q.items[0] = nil
	q.items = q.items[1:]
	return evt, true
}

func (q *queue) close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	q.cond.Broadcast()
}

func (q *queue) depth() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}
