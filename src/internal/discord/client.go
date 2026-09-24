// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/internal/discord/client.go

// Package discord wraps github.com/bwmarrin/discordgo into the small
// surface this bridge needs: connect, receive events, send messages,
// download attachments. See docs/discord-adapter-design.md for how this
// package's shape was derived from internal/gmessages.
//
// Everything that knows about the Discord gateway/REST protocol lives here.
// The bridge above it deals only in the types declared in types.go.
package discord

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/bwmarrin/discordgo"
)

// Config is what a Client needs to exist.
type Config struct {
	// Token is the bot account's token, already resolved from
	// config.DiscordConfig.TokenRef.
	Token string
	// Logger is the daemon's logger.
	Logger *slog.Logger
}

// Client is a connected Discord bot.
type Client struct {
	session *discordgo.Session
	log     *slog.Logger
	cfg     Config

	events *queue
}

// New builds a client. It does not talk to Discord yet — see Connect.
func New(cfg Config) (*Client, error) {
	if cfg.Token == "" {
		return nil, fmt.Errorf("discord: a token is required")
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}

	session, err := discordgo.New("Bot " + cfg.Token)
	if err != nil {
		return nil, fmt.Errorf("discord: building the session: %w", err)
	}
	// GuildMessages + DirectMessages so the bot sees messages in both guild
	// channels and DMs; MessageContent is required since API v10 to read a
	// message's actual text rather than just metadata about it.
	session.Identify.Intents = discordgo.IntentGuildMessages | discordgo.IntentDirectMessages | discordgo.IntentMessageContent
	// State tracking is what makes session.State.User (the bot's own
	// identity, used for loop prevention below) available after Ready.
	session.StateEnabled = true

	c := &Client{session: session, log: log, cfg: cfg, events: newQueue()}
	session.AddHandler(c.onReady)
	session.AddHandler(c.onConnect)
	session.AddHandler(c.onDisconnect)
	session.AddHandler(c.onResumed)
	session.AddHandler(c.onMessageCreate)
	return c, nil
}

// Connect opens the gateway websocket. It returns once the connection is
// established; a ReadyEvent follows on the event stream once the bot's own
// identity is known.
func (c *Client) Connect(ctx context.Context) error {
	if err := c.session.Open(); err != nil {
		return fmt.Errorf("connecting to Discord: %w", err)
	}
	return nil
}

// Disconnect closes the gateway websocket.
func (c *Client) Disconnect() {
	_ = c.session.Close()
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

func (c *Client) emit(evt Event) { c.events.push(evt) }

// Send posts a message to a Discord channel, replying to replyToID when it
// is not empty. Discord's REST call returns the created message
// synchronously — unlike Google Messages, there is no temporary-ID/echo
// dance: the caller has the real message ID as soon as this returns.
func (c *Client) Send(ctx context.Context, channelID, replyToID, text string, uploads []Upload) (Message, error) {
	send := &discordgo.MessageSend{Content: text}
	if replyToID != "" {
		send.Reference = &discordgo.MessageReference{
			MessageID: replyToID,
			ChannelID: channelID,
		}
	}
	for _, u := range uploads {
		send.Files = append(send.Files, &discordgo.File{
			Name:        u.Name,
			ContentType: u.MimeType,
			Reader:      bytes.NewReader(u.Data),
		})
	}

	msg, err := c.session.ChannelMessageSendComplex(channelID, send, discordgo.WithContext(ctx))
	if err != nil {
		return Message{}, fmt.Errorf("sending to Discord channel %s: %w", channelID, err)
	}
	return convertMessage(msg), nil
}

// Download fetches an attachment's bytes from its public URL. Unlike Google
// Messages media, this needs no auth or decryption.
func (c *Client) Download(ctx context.Context, att Attachment) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, att.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("building a request for attachment %s: %w", att.Name, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading attachment %s: %w", att.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading attachment %s: server returned %s", att.Name, resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading attachment %s: %w", att.Name, err)
	}
	return data, nil
}

// Conversation describes a Discord channel: a guild text channel, a DM, or
// a group DM. Participants is best-effort for a guild channel — see
// Conversation's own doc comment in types.go — but complete for a DM/group
// DM, where Discord hands over the recipient list directly.
func (c *Client) Conversation(ctx context.Context, channelID string) (Conversation, error) {
	ch, err := c.session.Channel(channelID, discordgo.WithContext(ctx))
	if err != nil {
		return Conversation{}, fmt.Errorf("fetching Discord channel %s: %w", channelID, err)
	}

	conv := Conversation{ID: ch.ID, GuildID: ch.GuildID}

	switch ch.Type {
	case discordgo.ChannelTypeDM:
		conv.IsGroup = false
		conv.Participants = participantsOf(ch.Recipients)
	case discordgo.ChannelTypeGroupDM:
		conv.IsGroup = true
		conv.Name = ch.Name
		conv.Participants = participantsOf(ch.Recipients)
	default:
		// A guild channel of some kind (text, news, thread, ...) — every
		// such channel behaves as a "group" conversation for routing
		// purposes, the same as a group SMS/RCS chat.
		conv.IsGroup = true
		conv.Name = ch.Name
	}

	return conv, nil
}

func participantsOf(users []*discordgo.User) []Participant {
	out := make([]Participant, 0, len(users))
	for _, u := range users {
		out = append(out, Participant{ID: u.ID, DisplayName: authorName(u)})
	}
	return out
}

// convertMessage turns discordgo's message into ours.
func convertMessage(m *discordgo.Message) Message {
	msg := Message{
		ID:             m.ID,
		ConversationID: m.ChannelID,
		GuildID:        m.GuildID,
		Text:           m.Content,
		Timestamp:      m.Timestamp,
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}
	if m.Author != nil {
		msg.AuthorID = m.Author.ID
		msg.AuthorName = authorName(m.Author)
	}
	if m.MessageReference != nil {
		msg.ReplyToID = m.MessageReference.MessageID
	}
	for _, a := range m.Attachments {
		msg.Attachments = append(msg.Attachments, Attachment{
			ID:       a.ID,
			Name:     a.Filename,
			MimeType: a.ContentType,
			URL:      a.URL,
			Size:     int64(a.Size),
		})
	}
	return msg
}

// authorName prefers a user's display name (Discord's "global name") over
// their account username, the same fallback gmessages.convertParticipant
// uses between a contact's full name and first name.
func authorName(u *discordgo.User) string {
	if u.GlobalName != "" {
		return u.GlobalName
	}
	return u.Username
}

// ---------------------------------------------------------------------------
// discordgo handlers — these run on discordgo's own goroutines, so they do
// no work beyond translation and enqueueing, mirroring gmessages'
// handleLibgmEvent.
// ---------------------------------------------------------------------------

func (c *Client) onReady(s *discordgo.Session, r *discordgo.Ready) {
	var userID, username string
	if r.User != nil {
		userID, username = r.User.ID, r.User.Username
	}
	c.log.Info("the Discord session is ready", "user", username, "user_id", userID, "guilds", len(r.Guilds))
	c.emit(ReadyEvent{UserID: userID, Username: username})
}

func (c *Client) onConnect(s *discordgo.Session, _ *discordgo.Connect) {
	c.emit(ConnectionEvent{State: ConnConnected})
}

func (c *Client) onDisconnect(s *discordgo.Session, _ *discordgo.Disconnect) {
	c.emit(ConnectionEvent{State: ConnDisconnected})
}

func (c *Client) onResumed(s *discordgo.Session, _ *discordgo.Resumed) {
	c.emit(ConnectionEvent{State: ConnResumed})
}

// onMessageCreate is discordgo's handler for every new message the bot can
// see, including the gateway's own echo of a message this daemon just sent
// through Send. Filtering that out here — rather than in the bridge — keeps
// loop prevention entirely inside this package, the same way
// mattermost.decodePost filters posts by the bridge's own bot user ID
// before they ever become a mattermost.Event.
func (c *Client) onMessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if s.State != nil && s.State.User != nil && m.Author != nil && m.Author.ID == s.State.User.ID {
		return
	}
	if m.Message == nil {
		return
	}
	c.emit(MessageEvent{Message: convertMessage(m.Message)})
}
