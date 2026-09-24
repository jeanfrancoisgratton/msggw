// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/internal/transport/types_test.go

package transport

import "testing"

func TestConversationTitlePrefersOwnName(t *testing.T) {
	conv := Conversation{Name: "#general", Participants: []Participant{{DisplayName: "Alice"}}}
	if got := conv.Title(); got != "#general" {
		t.Errorf("Title() = %q, want %q", got, "#general")
	}
}

func TestConversationTitleFallsBackToTheOtherParticipant(t *testing.T) {
	conv := Conversation{
		ID: "conv1",
		Participants: []Participant{
			{DisplayName: "Me", IsMe: true},
			{DisplayName: "Bob"},
		},
	}
	if got := conv.Title(); got != "Bob" {
		t.Errorf("Title() = %q, want %q", got, "Bob")
	}
}

func TestConversationTitleFallsBackToPhoneWhenUnnamed(t *testing.T) {
	conv := Conversation{
		ID:           "conv1",
		Participants: []Participant{{Phone: "+15145551212"}},
	}
	if got := conv.Title(); got != "+15145551212" {
		t.Errorf("Title() = %q, want the phone number", got)
	}
}

func TestConversationTitleJoinsMultipleOthers(t *testing.T) {
	conv := Conversation{
		Participants: []Participant{
			{DisplayName: "Me", IsMe: true},
			{DisplayName: "Alice"},
			{DisplayName: "Bob"},
			{DisplayName: "Carol"},
		},
	}
	if got := conv.Title(); got != "Alice, Bob and Carol" {
		t.Errorf("Title() = %q, want %q", got, "Alice, Bob and Carol")
	}
}

func TestConversationTitleFallsBackToIDWithNoOtherParticipants(t *testing.T) {
	conv := Conversation{ID: "conv1", Participants: []Participant{{DisplayName: "Me", IsMe: true}}}
	if got := conv.Title(); got != "conv1" {
		t.Errorf("Title() = %q, want the conversation ID", got)
	}
}

func TestOtherParticipantsExcludesSelf(t *testing.T) {
	conv := Conversation{Participants: []Participant{
		{ID: "me", IsMe: true},
		{ID: "them"},
	}}
	others := conv.OtherParticipants()
	if len(others) != 1 || others[0].ID != "them" {
		t.Errorf("OtherParticipants() = %+v, want just %q", others, "them")
	}
}

func TestMessageHasContent(t *testing.T) {
	tests := []struct {
		name string
		msg  Message
		want bool
	}{
		{"text", Message{Text: "hello"}, true},
		{"subject only", Message{Subject: "a subject"}, true},
		{"attachment only", Message{Attachments: []Attachment{{ID: "a1"}}}, true},
		{"nothing", Message{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.msg.HasContent(); got != tc.want {
				t.Errorf("HasContent() = %v, want %v", got, tc.want)
			}
		})
	}
}
