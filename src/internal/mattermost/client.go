// rcs_gateway
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 20:04:03
// Original filename: src/internal/mattermost/client.go

// Package mattermost wraps the Mattermost REST and WebSocket APIs into the
// operations this bridge needs: resolve a destination to a channel, post,
// upload files, react, and watch for replies.
//
// The daemon acts as a bot account rather than an incoming webhook. A webhook
// can only push messages one way; a bot can also read replies, upload
// attachments and edit posts, which is what makes the bridge bidirectional.
package mattermost

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
)

// Config is what a Client needs to exist.
type Config struct {
	// URL is the server root, e.g. https://mattermost.example.net.
	URL string
	// Token is the bot account's personal access token.
	Token string
	// InsecureSkipVerify disables TLS verification.
	InsecureSkipVerify bool
	// RequestTimeout bounds a single REST call.
	RequestTimeout time.Duration
	// ReconnectBackoff is the initial delay before reconnecting a dropped
	// WebSocket; it doubles up to maxReconnectBackoff.
	ReconnectBackoff time.Duration
	// JoinChannels lets the daemon add the bot to a channel it is not a member
	// of, instead of failing to post there.
	JoinChannels bool
	Logger       *slog.Logger
}

// Client is a connected Mattermost bot.
type Client struct {
	api *model.Client4
	cfg Config
	log *slog.Logger

	// me is the bot's own user, filled in by Connect. It is what makes loop
	// prevention possible: a post from this user ID is one the bridge itself
	// made, and must never be treated as a reply to send onward.
	me *model.User

	mu sync.RWMutex
	// channels caches destination-to-channel-ID resolutions. Resolving a
	// destination costs up to three REST calls, and it happens on every
	// message of a conversation the daemon has not seen since it started.
	channels map[string]string
	users    map[string]string
}

// New builds a client. It does not talk to the server.
func New(cfg Config) (*Client, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("mattermost: url is required")
	}
	if cfg.Token == "" {
		return nil, fmt.Errorf("mattermost: token is required")
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}

	api := model.NewAPIv4Client(strings.TrimRight(cfg.URL, "/"))
	api.SetToken(cfg.Token)

	httpClient := &http.Client{Timeout: cfg.RequestTimeout}
	if cfg.InsecureSkipVerify {
		httpClient.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}
	api.HTTPClient = httpClient

	return &Client{
		api:      api,
		cfg:      cfg,
		log:      log,
		channels: make(map[string]string),
		users:    make(map[string]string),
	}, nil
}

// Connect verifies the token and identifies the bot account.
func (c *Client) Connect(ctx context.Context) error {
	me, _, err := c.api.GetMe(ctx, "")
	if err != nil {
		return fmt.Errorf("authenticating with Mattermost at %s: %w", c.cfg.URL, err)
	}
	c.me = me
	c.log.Info("authenticated with Mattermost",
		"url", c.cfg.URL, "user", me.Username, "user_id", me.Id, "is_bot", me.IsBot)
	if !me.IsBot {
		// Not fatal — a personal access token for a regular account works —
		// but it is almost always a mistake, and it means every bridged
		// message will look as though the operator typed it.
		c.log.Warn("the Mattermost token belongs to a regular account, not a bot account",
			"user", me.Username)
	}
	return nil
}

// BotUserID is the account the daemon posts as.
func (c *Client) BotUserID() string {
	if c.me == nil {
		return ""
	}
	return c.me.Id
}

// BotUsername is the account's username, for logs and status output.
func (c *Client) BotUsername() string {
	if c.me == nil {
		return ""
	}
	return c.me.Username
}

// API exposes the underlying client for the few operations not wrapped here.
func (c *Client) API() *model.Client4 { return c.api }

// websocketURL turns the configured HTTP root into its WebSocket equivalent.
func (c *Client) websocketURL() string {
	url := strings.TrimRight(c.cfg.URL, "/")
	switch {
	case strings.HasPrefix(url, "https://"):
		return "wss://" + strings.TrimPrefix(url, "https://")
	case strings.HasPrefix(url, "http://"):
		return "ws://" + strings.TrimPrefix(url, "http://")
	default:
		return url
	}
}
