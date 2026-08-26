// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>

package mattermost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"

	"msggw/internal/config"
)

// fakeServer builds a Mattermost REST server just capable enough to drive
// ResolveDestination's DestChannel path: authenticate the bot, report a
// named channel missing, then (when the test allows it) create one.
type fakeServer struct {
	t             *testing.T
	channelExists bool
	created       *model.Channel
}

func (f *fakeServer) start() *httptest.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v4/users/me", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, &model.User{Id: "bot-id", Username: "msggw-bot", IsBot: true})
	})

	mux.HandleFunc("/api/v4/teams/name/myteam", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, &model.Team{Id: "team-id", Name: "myteam"})
	})

	mux.HandleFunc("/api/v4/teams/name/myteam/channels/name/newchan", func(w http.ResponseWriter, r *http.Request) {
		if !f.channelExists {
			writeAppError(w, http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, &model.Channel{Id: "existing-channel-id", Name: "newchan"})
	})

	mux.HandleFunc("/api/v4/channels", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var ch model.Channel
		if err := json.NewDecoder(r.Body).Decode(&ch); err != nil {
			f.t.Fatalf("decoding CreateChannel request body: %v", err)
		}
		ch.Id = "created-channel-id"
		f.created = &ch
		writeJSON(w, http.StatusCreated, &ch)
	})

	// The channel creator is already a member server-side, so the
	// membership check that follows creation succeeds immediately.
	mux.HandleFunc("/api/v4/channels/created-channel-id/members/bot-id", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, &model.ChannelMember{ChannelId: "created-channel-id", UserId: "bot-id"})
	})
	mux.HandleFunc("/api/v4/channels/existing-channel-id/members/bot-id", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, &model.ChannelMember{ChannelId: "existing-channel-id", UserId: "bot-id"})
	})

	return httptest.NewServer(mux)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeAppError writes a body shaped the way model.AppErrorFromJSON expects,
// with StatusCode set from the JSON body itself (not just the HTTP status),
// which is what lets isNotFound recognise it.
func writeAppError(w http.ResponseWriter, status int) {
	writeJSON(w, status, &model.AppError{StatusCode: status, Id: "not_found", Message: "not found"})
}

func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	client, err := New(Config{URL: srv.URL, Token: "test-token"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return client
}

func TestResolveDestinationCreatesMissingChannelWhenAllowed(t *testing.T) {
	fs := &fakeServer{t: t, channelExists: false}
	srv := fs.start()
	defer srv.Close()

	client := newTestClient(t, srv)
	dest := config.Destination{Type: config.DestChannel, Team: "myteam", Channel: "newchan"}

	channelID, err := client.ResolveDestination(context.Background(), dest, true)
	if err != nil {
		t.Fatalf("ResolveDestination: %v", err)
	}
	if channelID != "created-channel-id" {
		t.Errorf("channelID = %q, want the created channel's id", channelID)
	}
	if fs.created == nil {
		t.Fatal("CreateChannel was never called")
	}
	if fs.created.Type != model.ChannelTypePrivate {
		t.Errorf("created channel type = %q, want private", fs.created.Type)
	}
	if fs.created.TeamId != "team-id" {
		t.Errorf("created channel team id = %q, want team-id", fs.created.TeamId)
	}
}

func TestResolveDestinationDoesNotCreateWhenJoinChannelsIsOff(t *testing.T) {
	fs := &fakeServer{t: t, channelExists: false}
	srv := fs.start()
	defer srv.Close()

	client := newTestClient(t, srv)
	dest := config.Destination{Type: config.DestChannel, Team: "myteam", Channel: "newchan"}

	if _, err := client.ResolveDestination(context.Background(), dest, false); err == nil {
		t.Fatal("ResolveDestination succeeded with join_channels off and no existing channel")
	}
	if fs.created != nil {
		t.Error("CreateChannel was called even though join_channels is off")
	}
}

func TestResolveDestinationUsesExistingChannelWithoutCreating(t *testing.T) {
	fs := &fakeServer{t: t, channelExists: true}
	srv := fs.start()
	defer srv.Close()

	client := newTestClient(t, srv)
	dest := config.Destination{Type: config.DestChannel, Team: "myteam", Channel: "newchan"}

	channelID, err := client.ResolveDestination(context.Background(), dest, true)
	if err != nil {
		t.Fatalf("ResolveDestination: %v", err)
	}
	if channelID != "existing-channel-id" {
		t.Errorf("channelID = %q, want the existing channel's id", channelID)
	}
	if fs.created != nil {
		t.Error("CreateChannel was called even though the channel already existed")
	}
}
