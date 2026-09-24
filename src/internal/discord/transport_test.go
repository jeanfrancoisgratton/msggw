// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/internal/discord/transport_test.go

package discord

import (
	"testing"
	"time"

	"msggw/internal/transport"
)

func TestToTransportMessage(t *testing.T) {
	ts := time.Now()
	msg := Message{
		ID:             "m1",
		ConversationID: "c1",
		GuildID:        "g1",
		AuthorID:       "u1",
		AuthorName:     "Alice",
		Timestamp:      ts,
		Text:           "hello",
		ReplyToID:      "m0",
		Attachments:    []Attachment{{ID: "a1", Name: "cat.png", MimeType: "image/png", URL: "https://example/cat.png", Size: 42}},
	}

	got := toTransportMessage(msg)

	want := transport.Message{
		ID:             "m1",
		ConversationID: "c1",
		GuildID:        "g1",
		SenderID:       "u1",
		SenderName:     "Alice",
		Timestamp:      ts,
		Text:           "hello",
		ReplyToID:      "m0",
		Attachments:    []transport.Attachment{{ID: "a1", Name: "cat.png", MimeType: "image/png", URL: "https://example/cat.png", Size: 42}},
	}
	if got.ID != want.ID || got.ConversationID != want.ConversationID || got.GuildID != want.GuildID ||
		got.SenderID != want.SenderID || got.SenderName != want.SenderName || got.Text != want.Text ||
		got.ReplyToID != want.ReplyToID || len(got.Attachments) != 1 || got.Attachments[0] != want.Attachments[0] {
		t.Errorf("toTransportMessage() = %+v, want %+v", got, want)
	}
}

func TestToTransportConversation(t *testing.T) {
	conv := Conversation{
		ID:      "c1",
		GuildID: "g1",
		Name:    "#general",
		IsGroup: true,
		Participants: []Participant{
			{ID: "u1", DisplayName: "Alice", IsMe: false},
			{ID: "bot", DisplayName: "Bot", IsMe: true},
		},
	}

	got := toTransportConversation(conv)

	if got.ID != "c1" || got.GuildID != "g1" || got.Name != "#general" || !got.IsGroup {
		t.Errorf("toTransportConversation() = %+v, unexpected core fields", got)
	}
	if len(got.Participants) != 2 {
		t.Fatalf("Participants = %+v, want 2", got.Participants)
	}
	others := got.OtherParticipants()
	if len(others) != 1 || others[0].DisplayName != "Alice" {
		t.Errorf("OtherParticipants() = %+v, want just Alice", others)
	}
}

func TestConvertEventTypes(t *testing.T) {
	if r, ok := convertEvent(ReadyEvent{UserID: "u1", Username: "bot"}).(transport.ReadyEvent); !ok || r.UserID != "u1" || r.Username != "bot" {
		t.Errorf("convertEvent(ReadyEvent) = %#v", convertEvent(ReadyEvent{UserID: "u1", Username: "bot"}))
	}
	if m, ok := convertEvent(MessageEvent{Message: Message{ID: "m1"}}).(transport.MessageEvent); !ok || m.Message.ID != "m1" {
		t.Errorf("convertEvent(MessageEvent) did not carry the message through")
	}
	if c, ok := convertEvent(ConnectionEvent{State: ConnResumed}).(transport.ConnectionEvent); !ok || c.State != transport.ConnResumed {
		t.Errorf("convertEvent(ConnectionEvent) = %#v, want state %q", convertEvent(ConnectionEvent{State: ConnResumed}), transport.ConnResumed)
	}
}
