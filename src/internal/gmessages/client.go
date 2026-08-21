// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 20:00:26
// Original filename: src/internal/gmessages/client.go

// Package gmessages wraps go.mau.fi/mautrix-gmessages/pkg/libgm into the small
// surface this bridge needs: pair, connect, receive events, send messages,
// move media.
//
// Everything that knows about the Google Messages protocol lives here. The
// bridge above it deals only in the types declared in types.go, so that when
// Google changes the protocol — which docs/SOLUTION.md calls the project's
// main maintenance risk — the damage is contained to this package.
package gmessages

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

// Config is what a Client needs to exist.
type Config struct {
	// Session persists the paired-device credentials.
	Session *SessionStore
	// Logger is the daemon's logger; libgm's own zerolog output is routed into
	// it so there is a single log stream.
	Logger *slog.Logger
	// LogLevel bounds what libgm itself logs. libgm is chatty at debug.
	LogLevel slog.Level
	// PingInterval is how often to ping the phone. 0 keeps libgm's default of
	// one minute; libgm ignores anything outside [1 minute, 4 hours].
	PingInterval time.Duration
	// ForceRCS asks the phone to send over RCS rather than latching to SMS,
	// where the conversation supports it.
	ForceRCS bool
}

// Client is a paired Google Messages client.
type Client struct {
	gm      *libgm.Client
	session *SessionStore
	log     *slog.Logger
	cfg     Config

	events *queue

	mu sync.RWMutex
	// conversations caches the protocol's own conversation objects. Sending a
	// message needs the conversation's SIM payload and outgoing participant
	// ID, and refetching those from the phone on every send would put a
	// round-trip in front of every reply.
	conversations map[string]*gmproto.Conversation
	settings      *gmproto.Settings
	connected     bool
	sessionID     string
}

// New builds a client from a stored session. It does not talk to Google.
func New(cfg Config) (*Client, error) {
	if cfg.Session == nil {
		return nil, fmt.Errorf("gmessages: a session store is required")
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}

	auth, err := cfg.Session.Load()
	if err != nil {
		return nil, err
	}

	c := &Client{
		session:       cfg.Session,
		log:           log,
		cfg:           cfg,
		events:        newQueue(),
		conversations: make(map[string]*gmproto.Conversation),
	}
	c.gm = libgm.NewClient(auth, nil, zerologFor(log, cfg.LogLevel))
	c.gm.SetEventHandler(c.handleLibgmEvent)
	if cfg.PingInterval > 0 {
		c.gm.SetPingInterval(cfg.PingInterval)
	}
	return c, nil
}

// NewUnpaired builds a client with a fresh, empty session, for the pairing
// flow. Nothing is written until pairing succeeds.
func NewUnpaired(cfg Config) *Client {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	c := &Client{
		session:       cfg.Session,
		log:           log,
		cfg:           cfg,
		events:        newQueue(),
		conversations: make(map[string]*gmproto.Conversation),
	}
	c.gm = libgm.NewClient(libgm.NewAuthData(), nil, zerologFor(log, cfg.LogLevel))
	c.gm.SetEventHandler(c.handleLibgmEvent)
	return c
}

// Connect brings up the long-polling session with the phone. It returns as
// soon as the connection is established; a ReadyEvent follows on the event
// stream once the phone has sent the conversation list.
func (c *Client) Connect(ctx context.Context) error {
	if err := c.gm.FetchConfig(ctx); err != nil {
		// The client config is advisory — libgm uses it to match the current
		// web client version — so a failure here is worth a warning, not an
		// abort.
		c.log.Warn("could not fetch the Google Messages client config", "error", err)
	}
	if err := c.gm.Connect(); err != nil {
		return fmt.Errorf("connecting to Google Messages: %w", err)
	}
	return nil
}

// Disconnect tears the session down without unpairing.
func (c *Client) Disconnect() {
	c.setConnected(false)
	c.gm.Disconnect()
	c.events.close()
}

// Events returns the event stream. Ranging over it ends when the client is
// disconnected.
func (c *Client) Events() <-chan Event {
	out := make(chan Event)
	go func() {
		defer close(out)
		for {
			evt, ok := c.events.pop()
			if !ok {
				return
			}
			out <- evt
		}
	}()
	return out
}

// PendingEvents is how many events are waiting to be handled, for the status
// command and for logging when the bridge falls behind.
func (c *Client) PendingEvents() int { return c.events.depth() }

// IsConnected reports whether the long-polling session is up.
func (c *Client) IsConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connected && c.gm.IsConnected()
}

// SessionID is the current Google Messages session, empty before ready.
func (c *Client) SessionID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sessionID
}

// PhoneID is the paired phone's identifier.
func (c *Client) PhoneID() string { return c.session.PhoneID() }

// Unpair revokes the pairing on the phone and clears the stored session. The
// session is cleared even when the revocation fails, since a session the
// operator has asked to be rid of should not be left on disk.
func (c *Client) Unpair(ctx context.Context) error {
	revokeErr := c.gm.Unpair(ctx)
	c.gm.Disconnect()
	c.events.close()

	if err := c.session.Clear(); err != nil {
		if revokeErr != nil {
			return fmt.Errorf("revoking the pairing failed (%v) and so did clearing the session: %w", revokeErr, err)
		}
		return err
	}
	if revokeErr != nil {
		return fmt.Errorf("the local session was cleared, but revoking the pairing on the phone failed: %w", revokeErr)
	}
	return nil
}

func (c *Client) emit(evt Event) { c.events.push(evt) }

func (c *Client) setConnected(connected bool) {
	c.mu.Lock()
	c.connected = connected
	if connected {
		c.sessionID = c.gm.CurrentSessionID()
	}
	c.mu.Unlock()
}

func (c *Client) saveSession() error {
	if c.session == nil {
		return nil
	}
	return c.session.Save(c.gm.AuthData)
}

// SaveSession persists the current auth data. The daemon calls it on shutdown,
// so that a token refreshed since the last event is not lost.
func (c *Client) SaveSession() error { return c.saveSession() }

func (c *Client) cacheConversation(conv *gmproto.Conversation) {
	if conv.GetConversationID() == "" {
		return
	}
	c.mu.Lock()
	c.conversations[conv.GetConversationID()] = conv
	c.mu.Unlock()
}

func (c *Client) cacheSettings(settings *gmproto.Settings) {
	c.mu.Lock()
	c.settings = settings
	c.mu.Unlock()
}

// conversation returns the cached conversation, converted.
func (c *Client) conversation(id string) (Conversation, bool) {
	c.mu.RLock()
	raw, ok := c.conversations[id]
	c.mu.RUnlock()
	if !ok {
		return Conversation{}, false
	}
	return convertConversation(raw), true
}

// rawConversation returns the cached protocol object, fetching it from the
// phone when it is not cached yet.
func (c *Client) rawConversation(ctx context.Context, id string) (*gmproto.Conversation, error) {
	c.mu.RLock()
	raw, ok := c.conversations[id]
	c.mu.RUnlock()
	if ok {
		return raw, nil
	}

	fetched, err := c.gm.GetConversation(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("fetching conversation %s: %w", id, err)
	}
	c.cacheConversation(fetched)
	return fetched, nil
}

// simPayload returns the SIM to send a conversation's messages through.
//
// The conversation carries its own SIM card on a dual-SIM phone; when it does
// not, the phone's first configured SIM is the only sensible default, and a
// single-SIM phone accepts a nil payload anyway.
func (c *Client) simPayload(conv *gmproto.Conversation) *gmproto.SIMPayload {
	if payload := conv.GetSimCard().GetSIMData().GetSIMPayload(); payload != nil {
		return payload
	}
	c.mu.RLock()
	settings := c.settings
	c.mu.RUnlock()
	for _, sim := range settings.GetSIMCards() {
		if payload := sim.GetSIMData().GetSIMPayload(); payload != nil {
			return payload
		}
	}
	return nil
}

// zerologFor routes libgm's zerolog output into the daemon's slog logger, so
// that a deployment has one log stream and one format to configure.
func zerologFor(log *slog.Logger, level slog.Level) zerolog.Logger {
	zlevel := zerolog.InfoLevel
	switch level {
	case slog.LevelDebug:
		zlevel = zerolog.DebugLevel
	case slog.LevelWarn:
		zlevel = zerolog.WarnLevel
	case slog.LevelError:
		zlevel = zerolog.ErrorLevel
	}
	return zerolog.New(&slogWriter{log: log.With("component", "libgm")}).
		Level(zlevel).
		With().
		Timestamp().
		Logger()
}

// slogWriter forwards zerolog's JSON lines to slog, keeping libgm's own
// severity so that one of its errors is not filed away as an info line.
//
// Only zerolog's three fixed keys are interpreted; everything else is passed
// through as a single "fields" attribute rather than flattened, since libgm's
// field set is its own and not something to track from here.
type slogWriter struct {
	log *slog.Logger
}

func (w *slogWriter) Write(p []byte) (int, error) {
	var line map[string]any
	if err := json.Unmarshal(p, &line); err != nil {
		w.log.Info("libgm", "line", string(bytes.TrimSpace(p)))
		return len(p), nil
	}

	level := slog.LevelInfo
	switch line["level"] {
	case "trace", "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error", "fatal", "panic":
		level = slog.LevelError
	}

	message, _ := line["message"].(string)
	delete(line, "level")
	delete(line, "message")
	delete(line, "time")

	if message == "" {
		message = "libgm"
	}
	if len(line) == 0 {
		w.log.Log(context.Background(), level, message)
		return len(p), nil
	}
	w.log.Log(context.Background(), level, message, "fields", line)
	return len(p), nil
}

var _ io.Writer = (*slogWriter)(nil)
