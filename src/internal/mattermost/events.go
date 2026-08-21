// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 20:04:01
// Original filename: src/internal/mattermost/events.go

package mattermost

import (
	"context"
	"encoding/json"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
)

// Event is what the bridge consumes from Mattermost.
type Event interface{ isMMEvent() }

// PostEvent is a post by someone other than the bridge itself.
type PostEvent struct {
	Post Post
}

// ConnectionEvent reports the state of the WebSocket.
type ConnectionEvent struct {
	State string
	Err   error
}

// Connection states.
const (
	ConnConnected    = "connected"
	ConnDisconnected = "disconnected"
	ConnFailed       = "failed"
)

func (PostEvent) isMMEvent()       {}
func (ConnectionEvent) isMMEvent() {}

// Post is a Mattermost post, reduced to what the bridge uses.
type Post struct {
	ID        string
	ChannelID string
	RootID    string
	UserID    string
	Username  string
	Message   string
	// Type is empty for a post a person wrote, and set for system posts such
	// as "user joined the channel".
	Type     string
	FileIDs  []string
	CreateAt time.Time
	// FromBridge marks a post the daemon itself created.
	FromBridge bool
}

// ThreadRoot is the post that owns the thread this post is in: itself, when it
// is not a reply.
func (p Post) ThreadRoot() string {
	if p.RootID != "" {
		return p.RootID
	}
	return p.ID
}

// maxReconnectBackoff caps the WebSocket retry delay. A minute is long enough
// not to hammer a server that is down, and short enough that a bridge is back
// within a minute of it returning.
const maxReconnectBackoff = time.Minute

// Listen watches the server for posts until ctx is cancelled, reconnecting on
// its own when the connection drops.
//
// Posts made by the bot itself are filtered out here rather than in the
// bridge, since treating one of them as a user's reply would send every
// bridged message straight back to the phone — the loop docs/SOLUTION.md warns
// about.
func (c *Client) Listen(ctx context.Context) <-chan Event {
	out := make(chan Event, 64)

	go func() {
		defer close(out)

		backoff := c.cfg.ReconnectBackoff
		if backoff <= 0 {
			backoff = 5 * time.Second
		}
		currentBackoff := backoff

		for ctx.Err() == nil {
			ws, err := model.NewWebSocketClient4(c.websocketURL(), c.cfg.Token)
			if err != nil {
				c.log.Warn("could not open the Mattermost WebSocket",
					"error", err, "retry_in", currentBackoff)
				emit(ctx, out, ConnectionEvent{State: ConnFailed, Err: err})
				if !sleep(ctx, currentBackoff) {
					return
				}
				currentBackoff = nextBackoff(currentBackoff)
				continue
			}

			// A connection that comes up resets the backoff, so a long outage
			// followed by a brief one does not start at a minute.
			currentBackoff = backoff
			emit(ctx, out, ConnectionEvent{State: ConnConnected})
			c.log.Info("connected to the Mattermost WebSocket")

			c.pump(ctx, ws, out)
			ws.Close()

			if ctx.Err() != nil {
				return
			}
			emit(ctx, out, ConnectionEvent{State: ConnDisconnected})
			c.log.Warn("the Mattermost WebSocket dropped", "retry_in", currentBackoff)
			if !sleep(ctx, currentBackoff) {
				return
			}
			currentBackoff = nextBackoff(currentBackoff)
		}
	}()

	return out
}

// pump reads one connection until it closes or ctx is cancelled.
func (c *Client) pump(ctx context.Context, ws *model.WebSocketClient, out chan<- Event) {
	go ws.Listen()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ws.PingTimeoutChannel:
			// The server stopped answering pings. Returning drops the
			// connection and the caller reconnects.
			c.log.Warn("the Mattermost WebSocket stopped responding to pings")
			return

		case resp, ok := <-ws.ResponseChannel:
			if !ok {
				return
			}
			if resp != nil && resp.Error != nil {
				c.log.Warn("the Mattermost WebSocket returned an error", "error", resp.Error)
			}

		case evt, ok := <-ws.EventChannel:
			if !ok {
				return
			}
			if evt == nil || evt.EventType() != model.WebsocketEventPosted {
				continue
			}
			post, ok := c.decodePost(evt)
			if !ok {
				continue
			}
			emit(ctx, out, PostEvent{Post: post})
		}
	}
}

// decodePost extracts a post from a "posted" event, dropping the ones the
// bridge must not act on.
func (c *Client) decodePost(evt *model.WebSocketEvent) (Post, bool) {
	// Mattermost sends the post as a JSON string inside the event data.
	raw, ok := evt.GetData()["post"].(string)
	if !ok {
		return Post{}, false
	}

	var mmPost model.Post
	if err := json.Unmarshal([]byte(raw), &mmPost); err != nil {
		c.log.Warn("could not decode a Mattermost post from a WebSocket event", "error", err)
		return Post{}, false
	}

	post := convertPost(&mmPost)
	if username, ok := evt.GetData()["sender_name"].(string); ok {
		post.Username = username
	}

	switch {
	case post.UserID == c.BotUserID():
		// Our own post, echoed back. This is the loop guard.
		return Post{}, false
	case post.FromBridge:
		// Belt and braces: a post carrying the bridge marker but a different
		// user ID means the bot account was changed under a running bridge.
		return Post{}, false
	case post.Type != "":
		// A system post: joins, leaves, header changes.
		return Post{}, false
	}

	return post, true
}

func convertPost(p *model.Post) Post {
	post := Post{
		ID:        p.Id,
		ChannelID: p.ChannelId,
		RootID:    p.RootId,
		UserID:    p.UserId,
		Message:   p.Message,
		Type:      p.Type,
		FileIDs:   p.FileIds,
	}
	if p.CreateAt != 0 {
		post.CreateAt = time.UnixMilli(p.CreateAt)
	}
	if props := p.GetProps(); props != nil {
		_, post.FromBridge = props[PropBridge]
	}
	return post
}

// emit sends an event unless the context is already done, so that a shutdown
// does not block on a channel nobody is reading any more.
func emit(ctx context.Context, out chan<- Event, evt Event) {
	select {
	case out <- evt:
	case <-ctx.Done():
	}
}

// sleep waits for d, reporting false if the context was cancelled first.
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func nextBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > maxReconnectBackoff {
		return maxReconnectBackoff
	}
	return next
}
