// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 20:12:29
// Original filename: src/internal/storage/storage_test.go

package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// testTenant is used everywhere a single-tenant test does not care about the
// tenant column beyond it being consistently non-empty. Tenant isolation
// itself is covered separately, in TestTenantIsolation.
const testTenant = "t1"

func openTestDB(t *testing.T) (*DB, context.Context) {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, ctx
}

// TestRebind covers the placeholder rewriting that lets every query in this
// package be written once, with "?" placeholders, and run against either
// backend.
func TestRebind(t *testing.T) {
	sqlite := &DB{backend: BackendSQLite}
	query := `SELECT value FROM meta WHERE key = ? AND value != ?`
	if got := sqlite.rebind(query); got != query {
		t.Errorf("sqlite rebind changed the query: got %q, want unchanged", got)
	}

	postgres := &DB{backend: BackendPostgres}
	want := `SELECT value FROM meta WHERE key = $1 AND value != $2`
	if got := postgres.rebind(query); got != want {
		t.Errorf("postgres rebind = %q, want %q", got, want)
	}
}

func TestNormalizePhone(t *testing.T) {
	tests := []struct{ in, want string }{
		{"+1 514 555-1212", "+15145551212"},
		{"+1 (514) 555.1212", "+15145551212"},
		{"+15145551212", "+15145551212"},
		{"514-555-1212", "5145551212"},
		{"", ""},
		{"no digits here", ""},
		// A plus that is not leading is punctuation, not a country prefix.
		{"514+555", "514555"},
	}

	for _, tc := range tests {
		if got := NormalizePhone(tc.in); got != tc.want {
			t.Errorf("NormalizePhone(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestConversationRoundTrip(t *testing.T) {
	db, ctx := openTestDB(t)

	conv := Conversation{
		ID:                    "conv-1",
		ChannelID:             "channel-1",
		RootPostID:            "post-root",
		DisplayName:           "Alice",
		OutgoingParticipantID: "me-1",
		Type:                  2,
		LastSeen:              time.Unix(1_700_000_000, 0),
		Participants: []Participant{
			{ID: "p-alice", Phone: "+1 514 555-1212", DisplayName: "Alice"},
			{ID: "p-me", Phone: "+1 514 000-0000", IsMe: true},
		},
	}
	if err := db.SaveConversation(ctx, testTenant, conv); err != nil {
		t.Fatalf("SaveConversation: %v", err)
	}

	got, err := db.GetConversation(ctx, testTenant, "conv-1")
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}
	if got.ChannelID != conv.ChannelID || got.RootPostID != conv.RootPostID {
		t.Errorf("GetConversation returned %+v, want channel %q and root %q",
			got, conv.ChannelID, conv.RootPostID)
	}
	if len(got.Participants) != 2 {
		t.Fatalf("got %d participants, want 2", len(got.Participants))
	}

	// The reverse lookup by root post is what turns a Mattermost reply back
	// into a conversation.
	byRoot, err := db.GetConversationByRootPost(ctx, testTenant, "post-root")
	if err != nil {
		t.Fatalf("GetConversationByRootPost: %v", err)
	}
	if byRoot.ID != "conv-1" {
		t.Errorf("GetConversationByRootPost returned %q, want conv-1", byRoot.ID)
	}

	// And the number lookup has to survive reformatting.
	byPhone, err := db.FindConversationByPhone(ctx, testTenant, "+15145551212")
	if err != nil {
		t.Fatalf("FindConversationByPhone: %v", err)
	}
	if byPhone.ID != "conv-1" {
		t.Errorf("FindConversationByPhone returned %q, want conv-1", byPhone.ID)
	}
}

func TestFindConversationByPhoneIgnoresOurOwnNumber(t *testing.T) {
	db, ctx := openTestDB(t)

	if err := db.SaveConversation(ctx, testTenant, Conversation{
		ID: "conv-1", ChannelID: "c", RootPostID: "r",
		Participants: []Participant{{ID: "p-me", Phone: "+15140000000", IsMe: true}},
	}); err != nil {
		t.Fatalf("SaveConversation: %v", err)
	}

	if _, err := db.FindConversationByPhone(ctx, testTenant, "+15140000000"); !errors.Is(err, ErrNotFound) {
		t.Errorf("FindConversationByPhone matched our own number, err = %v", err)
	}
}

func TestSaveConversationReplacesParticipants(t *testing.T) {
	db, ctx := openTestDB(t)

	base := Conversation{ID: "conv-1", ChannelID: "c", RootPostID: "r"}
	base.Participants = []Participant{{ID: "p1", Phone: "+15145551212"}}
	if err := db.SaveConversation(ctx, testTenant, base); err != nil {
		t.Fatalf("SaveConversation: %v", err)
	}

	// A group whose membership changed must not accumulate former members.
	base.Participants = []Participant{{ID: "p2", Phone: "+15145551213"}}
	if err := db.SaveConversation(ctx, testTenant, base); err != nil {
		t.Fatalf("SaveConversation (update): %v", err)
	}

	got, err := db.GetConversation(ctx, testTenant, "conv-1")
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}
	if len(got.Participants) != 1 || got.Participants[0].ID != "p2" {
		t.Errorf("participants = %+v, want only p2", got.Participants)
	}
}

func TestGetSoleConversationInChannel(t *testing.T) {
	db, ctx := openTestDB(t)

	if err := db.SaveConversation(ctx, testTenant, Conversation{ID: "conv-1", ChannelID: "shared"}); err != nil {
		t.Fatalf("SaveConversation: %v", err)
	}

	got, err := db.GetSoleConversationInChannel(ctx, testTenant, "shared")
	if err != nil {
		t.Fatalf("GetSoleConversationInChannel: %v", err)
	}
	if got.ID != "conv-1" {
		t.Errorf("got %q, want conv-1", got.ID)
	}

	// With a second conversation in the same channel there is no right answer,
	// and guessing would send a reply to the wrong person.
	if err := db.SaveConversation(ctx, testTenant, Conversation{ID: "conv-2", ChannelID: "shared"}); err != nil {
		t.Fatalf("SaveConversation: %v", err)
	}
	if _, err := db.GetSoleConversationInChannel(ctx, testTenant, "shared"); !errors.Is(err, ErrNotFound) {
		t.Errorf("with two conversations in a channel, err = %v, want ErrNotFound", err)
	}
}

// TestMessageDeduplication is the guard against the replayed-event problem:
// a message already mapped must be recognisable as such.
func TestMessageDeduplication(t *testing.T) {
	db, ctx := openTestDB(t)

	if _, err := db.GetMessage(ctx, testTenant, "msg-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetMessage on an empty database, err = %v, want ErrNotFound", err)
	}

	msg := Message{
		ID: "msg-1", PostID: "post-1", ConversationID: "conv-1",
		Direction: DirectionIn, Status: 100, CreatedAt: time.Unix(1_700_000_000, 0),
	}
	if err := db.SaveMessage(ctx, testTenant, msg); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}

	got, err := db.GetMessage(ctx, testTenant, "msg-1")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if got.PostID != "post-1" || got.Direction != DirectionIn {
		t.Errorf("GetMessage returned %+v", got)
	}

	byPost, err := db.GetMessageByPost(ctx, testTenant, "post-1")
	if err != nil {
		t.Fatalf("GetMessageByPost: %v", err)
	}
	if byPost.ID != "msg-1" {
		t.Errorf("GetMessageByPost returned %q, want msg-1", byPost.ID)
	}
}

func TestUpdateMessageStatusReportsChange(t *testing.T) {
	db, ctx := openTestDB(t)

	if err := db.SaveMessage(ctx, testTenant, Message{
		ID: "msg-1", PostID: "post-1", ConversationID: "conv-1",
		Direction: DirectionOut, Status: 1,
	}); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}

	changed, err := db.UpdateMessageStatus(ctx, testTenant, "msg-1", 2)
	if err != nil {
		t.Fatalf("UpdateMessageStatus: %v", err)
	}
	if !changed {
		t.Error("a new status was reported as unchanged")
	}

	// The phone repeats status updates; a repeat must not be reported as a
	// change, or the post's reactions would churn.
	changed, err = db.UpdateMessageStatus(ctx, testTenant, "msg-1", 2)
	if err != nil {
		t.Fatalf("UpdateMessageStatus (repeat): %v", err)
	}
	if changed {
		t.Error("a repeated status was reported as a change")
	}
}

// TestPendingOutbound covers the mechanism that ties the phone's echo of a
// message back to the Mattermost post it came from, and keeps the bridge from
// posting its own message a second time.
func TestPendingOutbound(t *testing.T) {
	db, ctx := openTestDB(t)

	if err := db.AddPendingOutbound(ctx, testTenant, "tmp-1", "post-1", "conv-1"); err != nil {
		t.Fatalf("AddPendingOutbound: %v", err)
	}

	postID, err := db.TakePendingOutbound(ctx, "tmp-1")
	if err != nil {
		t.Fatalf("TakePendingOutbound: %v", err)
	}
	if postID != "post-1" {
		t.Errorf("TakePendingOutbound = %q, want post-1", postID)
	}

	// Taking it consumes it: a second event with the same temporary ID is not
	// ours to claim again.
	if _, err := db.TakePendingOutbound(ctx, "tmp-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a consumed temporary ID was found again, err = %v", err)
	}

	// A message from the phone itself carries no temporary ID of ours.
	if _, err := db.TakePendingOutbound(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("an empty temporary ID matched something, err = %v", err)
	}
}

func TestPrunePendingOutbound(t *testing.T) {
	db, ctx := openTestDB(t)

	if err := db.AddPendingOutbound(ctx, testTenant, "tmp-1", "post-1", "conv-1"); err != nil {
		t.Fatalf("AddPendingOutbound: %v", err)
	}

	// Nothing is old enough yet.
	removed, err := db.PrunePendingOutbound(ctx, testTenant, time.Hour)
	if err != nil {
		t.Fatalf("PrunePendingOutbound: %v", err)
	}
	if removed != 0 {
		t.Errorf("pruned %d fresh entries, want 0", removed)
	}

	// A zero maximum age makes everything already written expired.
	removed, err = db.PrunePendingOutbound(ctx, testTenant, 0)
	if err != nil {
		t.Fatalf("PrunePendingOutbound: %v", err)
	}
	if removed != 1 {
		t.Errorf("pruned %d expired entries, want 1", removed)
	}
}

// TestMigrationIsIdempotent covers the restart path: reopening an existing
// database must not try to recreate its tables.
func TestMigrationIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := db.SaveConversation(ctx, testTenant, Conversation{ID: "conv-1", ChannelID: "c"}); err != nil {
		t.Fatalf("SaveConversation: %v", err)
	}
	db.Close()

	db, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer db.Close()

	if _, err := db.GetConversation(ctx, testTenant, "conv-1"); err != nil {
		t.Errorf("the conversation did not survive a reopen: %v", err)
	}
}

func TestMeta(t *testing.T) {
	db, ctx := openTestDB(t)

	value, err := db.GetMeta(ctx, "missing")
	if err != nil {
		t.Fatalf("GetMeta: %v", err)
	}
	if value != "" {
		t.Errorf("GetMeta on a missing key = %q, want \"\"", value)
	}

	if err := db.SetMeta(ctx, "k", "v1"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	if err := db.SetMeta(ctx, "k", "v2"); err != nil {
		t.Fatalf("SetMeta (overwrite): %v", err)
	}
	if value, err = db.GetMeta(ctx, "k"); err != nil || value != "v2" {
		t.Errorf("GetMeta = %q, %v, want \"v2\", nil", value, err)
	}
}

// TestTenantIsolation covers docs/SOLUTION.md's sharpest cross-tenant
// edge: two tenants whose contacts share a phone number, or who route into
// the same Mattermost channel, must never see each other's conversations.
func TestTenantIsolation(t *testing.T) {
	db, ctx := openTestDB(t)

	const alice, bob = "alice", "bob"

	// Both tenants have a contact at the same number, in their own
	// conversation. FindConversationByPhone must resolve each tenant to its
	// own conversation, never the other's.
	if err := db.SaveConversation(ctx, alice, Conversation{
		ID: "conv-shared-number", ChannelID: "alice-channel",
		Participants: []Participant{{ID: "p1", Phone: "+15145551212"}},
	}); err != nil {
		t.Fatalf("SaveConversation(alice): %v", err)
	}
	if err := db.SaveConversation(ctx, bob, Conversation{
		ID: "conv-shared-number", ChannelID: "bob-channel",
		Participants: []Participant{{ID: "p1", Phone: "+15145551212"}},
	}); err != nil {
		t.Fatalf("SaveConversation(bob): %v", err)
	}

	aliceConv, err := db.FindConversationByPhone(ctx, alice, "+15145551212")
	if err != nil {
		t.Fatalf("FindConversationByPhone(alice): %v", err)
	}
	if aliceConv.ChannelID != "alice-channel" {
		t.Errorf("FindConversationByPhone(alice) returned channel %q, want alice's own", aliceConv.ChannelID)
	}
	bobConv, err := db.FindConversationByPhone(ctx, bob, "+15145551212")
	if err != nil {
		t.Fatalf("FindConversationByPhone(bob): %v", err)
	}
	if bobConv.ChannelID != "bob-channel" {
		t.Errorf("FindConversationByPhone(bob) returned channel %q, want bob's own", bobConv.ChannelID)
	}

	// Two tenants sharing one destination channel: each has exactly one
	// conversation there, so GetSoleConversationInChannel must succeed for
	// both, scoped to their own conversation — not fail because the channel
	// holds two conversations in total.
	if err := db.SaveConversation(ctx, alice, Conversation{ID: "conv-a", ChannelID: "shared-channel"}); err != nil {
		t.Fatalf("SaveConversation(alice, shared): %v", err)
	}
	if err := db.SaveConversation(ctx, bob, Conversation{ID: "conv-b", ChannelID: "shared-channel"}); err != nil {
		t.Fatalf("SaveConversation(bob, shared): %v", err)
	}

	got, err := db.GetSoleConversationInChannel(ctx, alice, "shared-channel")
	if err != nil {
		t.Fatalf("GetSoleConversationInChannel(alice): %v", err)
	}
	if got.ID != "conv-a" {
		t.Errorf("GetSoleConversationInChannel(alice) = %q, want conv-a", got.ID)
	}
	got, err = db.GetSoleConversationInChannel(ctx, bob, "shared-channel")
	if err != nil {
		t.Fatalf("GetSoleConversationInChannel(bob): %v", err)
	}
	if got.ID != "conv-b" {
		t.Errorf("GetSoleConversationInChannel(bob) = %q, want conv-b", got.ID)
	}

	// ListConversations and CountMessages must also stay within one tenant.
	if convs, err := db.ListConversations(ctx, alice); err != nil {
		t.Fatalf("ListConversations(alice): %v", err)
	} else if len(convs) != 2 {
		t.Errorf("ListConversations(alice) = %d conversations, want 2 (conv-shared-number, conv-a)", len(convs))
	}
}
