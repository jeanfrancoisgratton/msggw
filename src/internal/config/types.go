// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 19:43:27
// Original filename: src/internal/config/types.go

package config

import "msggw/internal/secrets"

// Destination kinds. Where a conversation ends up in Mattermost is entirely an
// operator decision, so the daemon supports every shape Mattermost offers
// rather than assuming a single #messages channel.
const (
	// DestChannel posts into a named channel of a named team.
	DestChannel = "channel"
	// DestChannelID posts into a channel identified by its raw 26-character ID,
	// which is the only way to name a channel whose URL name may change.
	DestChannelID = "channel_id"
	// DestDirect posts into the direct-message channel between the bot and one
	// user. This is the "everything shows up in my DMs with the bot" layout.
	DestDirect = "direct"
	// DestGroup posts into a group direct-message channel between the bot and
	// several users.
	DestGroup = "group"
)

// Database driver names. SQLite is the only one that has been exercised
// against real message volume — see docs/SOLUTION.md — but PostgreSQL is a
// full alternative for an operator who wants the state store on a separate
// server or under its own backup/HA story.
const (
	DatabaseDriverSQLite   = "sqlite"
	DatabaseDriverPostgres = "postgres"
)

// Config is the whole daemon configuration, read from a JSON file.
type Config struct {
	// StateDir holds everything the daemon persists that is not a secret: the
	// SQLite database (when Backend.Driver is sqlite), and downloaded media
	// while it is in flight.
	StateDir string `json:"state_dir"`
	// Backend selects and configures the storage backend.
	Backend BackendConfig `json:"backend,omitempty"`

	Log        LogConfig           `json:"log"`
	Vault      secrets.VaultConfig `json:"vault,omitempty"`
	Mattermost MattermostConfig    `json:"mattermost"`
	Listener   ListenerConfig      `json:"listener,omitempty"`

	// Users is one entry per paired phone — see docs/MULTI-TENANCY.md. There
	// is always at least one: a single-user deployment is just a Users slice
	// of length one, not a separate shape.
	Users []UserConfig `json:"users"`

	// path records where this config was read from, for diagnostics.
	path string
}

// UserConfig is one tenant: one paired phone, with its own session and its
// own routing. Everything else (Mattermost, the database, logging) is
// shared across every tenant in Users.
type UserConfig struct {
	// Name identifies this tenant — in pairing ("msg-gw pair NAME"), in
	// status output, and as the tenant column's value in storage. It must be
	// unique among Users.
	Name string `json:"name"`

	GMessages GMessagesConfig `json:"gmessages"`
	Routing   RoutingConfig   `json:"routing"`
}

// BackendConfig selects the storage backend. Both the SQLite and Postgres
// blocks may be filled in at the same time — only the one Driver names is
// ever read, so an operator can switch backends by changing Driver alone,
// without having to first go write the other block's settings.
type BackendConfig struct {
	// Driver selects the storage backend: DatabaseDriverSQLite (default) or
	// DatabaseDriverPostgres.
	Driver string `json:"driver,omitempty"`
	// SQLite holds settings used only when Driver is sqlite.
	SQLite SQLiteBackendConfig `json:"sqlite,omitempty"`
	// Postgres holds settings used only when Driver is postgres.
	Postgres PostgresBackendConfig `json:"postgres,omitempty"`
}

// SQLiteBackendConfig is consulted only when BackendConfig.Driver is
// DatabaseDriverSQLite.
type SQLiteBackendConfig struct {
	// Path is the SQLite file. Relative paths resolve inside StateDir.
	Path string `json:"path,omitempty"`
}

// PostgresBackendConfig is consulted only when BackendConfig.Driver is
// DatabaseDriverPostgres.
type PostgresBackendConfig struct {
	// DSNRef is a secret reference resolving to the PostgreSQL DSN
	// (e.g. "postgres://user:pass@host:5432/dbname?sslmode=disable"). It is
	// a reference rather than a plain string so the password does not sit in
	// the config file, the same way mattermost.token_ref works.
	DSNRef string `json:"dsn_ref,omitempty"`
}

// LogConfig controls the daemon's own logging. libgm logs through zerolog and
// the rest of the daemon through slog; both are driven from here.
type LogConfig struct {
	// Level is one of debug, info, warn, error.
	Level string `json:"level,omitempty"`
	// Format is "text" or "json".
	Format string `json:"format,omitempty"`
}

// GMessagesConfig covers the Google Messages side of the bridge.
type GMessagesConfig struct {
	// SessionRef is where the paired-device session lives. It must be a
	// writable reference — file:, encoded: or vault: — because libgm refreshes
	// its auth token while running and the new one has to be persisted.
	SessionRef string `json:"session_ref"`

	// PingIntervalSeconds is how often libgm pings the phone. libgm clamps
	// this to [1 minute, 4 hours]; 0 keeps its default of one minute.
	PingIntervalSeconds int `json:"ping_interval_seconds,omitempty"`

	// ForceRCS asks Google Messages to send over RCS rather than falling back
	// to SMS when the conversation supports both. It only has an effect on
	// conversations that are already RCS-capable.
	ForceRCS bool `json:"force_rcs,omitempty"`

	// MarkReadOnBridge marks a conversation read on the phone once its message
	// has been posted to Mattermost. Off by default: it makes the phone's own
	// notifications disappear, which is not always wanted.
	MarkReadOnBridge bool `json:"mark_read_on_bridge,omitempty"`

	// BackfillCount is how many recent messages to fetch for a conversation the
	// first time it is bridged. 0 disables backfill.
	BackfillCount int `json:"backfill_count,omitempty"`
}

// MattermostConfig covers the Mattermost side of the bridge.
//
// The daemon talks to Mattermost as a bot account over the REST and WebSocket
// APIs. An incoming webhook would be enough to push messages one way, but not
// to read replies, upload files or edit posts, so it is not an alternative
// here — see docs/SOLUTION.md.
type MattermostConfig struct {
	// URL is the server root, e.g. https://mattermost.example.net.
	URL string `json:"url"`
	// TokenRef resolves to the bot account's personal access token.
	TokenRef string `json:"token_ref"`
	// InsecureSkipVerify disables TLS verification. Only for a server with a
	// self-signed certificate that is not in the host trust store.
	InsecureSkipVerify bool `json:"insecure_skip_verify,omitempty"`
	// ReconnectBackoffSeconds is the initial delay before reconnecting a
	// dropped WebSocket. It doubles up to a minute. 0 means 5 seconds.
	ReconnectBackoffSeconds int `json:"reconnect_backoff_seconds,omitempty"`
	// RequestTimeoutSeconds bounds a single REST call. 0 means 30 seconds.
	RequestTimeoutSeconds int `json:"request_timeout_seconds,omitempty"`
}

// RoutingConfig decides where in Mattermost a Google Messages conversation
// shows up. Default applies to everything that no rule matches.
type RoutingConfig struct {
	Default Destination `json:"default"`
	// Rules are evaluated in order; the first match wins.
	Rules []Rule `json:"rules,omitempty"`

	// ThreadPerConversation maps each Google Messages conversation to one
	// Mattermost root post, with every message in that conversation as a reply
	// in its thread. This is the layout described in docs/SOLUTION.md.
	//
	// When false, every message becomes its own top-level post. That is only
	// sensible for a destination dedicated to a single conversation.
	ThreadPerConversation *bool `json:"thread_per_conversation,omitempty"`

	// PostDeliveryStatus reflects RCS delivery state as a reaction on the
	// Mattermost post (✅ delivered, 👀 read, ⚠️ failed) instead of leaving it
	// invisible.
	PostDeliveryStatus bool `json:"post_delivery_status,omitempty"`

	// JoinChannels makes the daemon add the bot to a resolved channel it is
	// not yet a member of, or create it if it does not exist yet at all,
	// rather than failing. A bot cannot post to a channel it has not joined.
	JoinChannels bool `json:"join_channels,omitempty"`
}

// ThreadPerConversationEnabled reads the tri-state ThreadPerConversation
// flag after defaults have been applied (nil only before Config.Load runs
// applyDefaults).
func (r RoutingConfig) ThreadPerConversationEnabled() bool {
	return r.ThreadPerConversation == nil || *r.ThreadPerConversation
}

// ListenerConfig configures the daemon's HTTP(S) listener. It exists for
// "client mode" pairing: a client running on the operator's own device (not
// this host) registers pairing material without ever sending it to the
// daemon's host over an unencrypted channel — see docs/MULTI-TENANCY.md.
//
// The listener is disabled unless Port is set; there is no separate "enabled"
// flag, the same way GMessagesConfig.BackfillCount uses 0 to mean off.
type ListenerConfig struct {
	// Port is what the listener binds to, on all interfaces. 0 disables it.
	Port int `json:"port,omitempty"`
	// CertFile and KeyFile are the TLS certificate and private key, as plain
	// filesystem paths. Missing or unusable, the listener falls back to plain
	// HTTP rather than refusing to start — loudly, since that is a
	// consequential thing for it to do silently. See internal/listener.
	CertFile string `json:"cert_file,omitempty"`
	KeyFile  string `json:"key_file,omitempty"`
}

// Destination is one place in Mattermost.
type Destination struct {
	// Type is one of the Dest* constants.
	Type string `json:"type"`

	// Team and Channel name a channel by its URL name, for Type == channel.
	Team    string `json:"team,omitempty"`
	Channel string `json:"channel,omitempty"`

	// ChannelID names a channel directly, for Type == channel_id.
	ChannelID string `json:"channel_id,omitempty"`

	// User is the Mattermost username the bot direct-messages, for
	// Type == direct.
	User string `json:"user,omitempty"`

	// Users are the Mattermost usernames in the group message, for
	// Type == group. Mattermost allows between 2 and 7 members besides the bot.
	Users []string `json:"users,omitempty"`
}

// Rule sends conversations matching any of its criteria to a destination.
// An empty rule matches nothing, so that a half-written rule cannot capture
// every conversation.
type Rule struct {
	// Name is a label for logs. It has no effect on matching.
	Name string `json:"name,omitempty"`

	// ConversationIDs matches the Google Messages conversation ID exactly.
	ConversationIDs []string `json:"conversation_ids,omitempty"`
	// Phones matches any participant's number. Comparison ignores spaces,
	// dashes, dots and parentheses, so "+1 514 555-1212" matches "+15145551212".
	Phones []string `json:"phones,omitempty"`
	// NamePattern is a regular expression matched against the conversation's
	// display name (the contact name, or the group title).
	NamePattern string `json:"name_pattern,omitempty"`
	// GroupsOnly restricts the rule to group conversations, DirectsOnly to
	// one-to-one ones. Setting both matches nothing.
	GroupsOnly  bool `json:"groups_only,omitempty"`
	DirectsOnly bool `json:"directs_only,omitempty"`

	Destination Destination `json:"destination"`
}
