// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 19:44:03
// Original filename: src/internal/config/config.go

// Package config reads and validates the daemon's JSON configuration.
//
// Nothing in here reaches out to Vault, Mattermost or Google: Load only proves
// that the configuration is internally coherent. Credentials are resolved, and
// therefore fail, at the point they are used.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"msggw/internal/secrets"
)

// DefaultPath is where the daemon looks when no --config is given.
const DefaultPath = "/etc/msggw/config.json"

// UserPath is the per-user fallback, used when DefaultPath does not exist.
// It lets the daemon run unprivileged without an /etc entry.
func UserPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "msggw", "config.json")
}

// Load reads the configuration from path, applies defaults and validates it.
// An empty path means DefaultPath, falling back to UserPath.
func Load(path string) (*Config, error) {
	if path == "" {
		var err error
		if path, err = discover(); err != nil {
			return nil, err
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading configuration %s: %w", path, err)
	}

	var cfg Config
	dec := json.NewDecoder(strings.NewReader(string(data)))
	// A typo in a key name would otherwise be silently ignored and leave the
	// daemon running with a default nobody asked for.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parsing configuration %s: %w", path, err)
	}
	cfg.path = path

	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("configuration %s: %w", path, err)
	}
	return &cfg, nil
}

func discover() (string, error) {
	if _, err := os.Stat(DefaultPath); err == nil {
		return DefaultPath, nil
	}
	if userPath := UserPath(); userPath != "" {
		if _, err := os.Stat(userPath); err == nil {
			return userPath, nil
		}
		return "", fmt.Errorf("no configuration found at %s or %s (see --config)", DefaultPath, userPath)
	}
	return "", fmt.Errorf("no configuration found at %s (see --config)", DefaultPath)
}

// Path returns the file this configuration was read from.
func (c *Config) Path() string { return c.path }

func (c *Config) applyDefaults() {
	if c.StateDir == "" {
		c.StateDir = "/var/lib/msggw"
	}
	if c.Backend.Driver == "" {
		c.Backend.Driver = DatabaseDriverSQLite
	}
	// The SQLite path is defaulted unconditionally, not only when it is the
	// active driver: both backend blocks are meant to be always ready to go,
	// so switching Driver back to sqlite later must not find an empty path.
	if c.Backend.SQLite.Path == "" {
		c.Backend.SQLite.Path = "msggw.db"
	}
	// A reference (vault:, file:, ...) is left alone here: it has to be
	// resolved before we can tell whether the result is relative, which
	// SQLitePath does at the point of use.
	if !secrets.LooksLikeReference(c.Backend.SQLite.Path) && !filepath.IsAbs(c.Backend.SQLite.Path) {
		c.Backend.SQLite.Path = filepath.Join(c.StateDir, c.Backend.SQLite.Path)
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	if c.Log.Format == "" {
		c.Log.Format = "text"
	}
	if c.Mattermost.ReconnectBackoffSeconds == 0 {
		c.Mattermost.ReconnectBackoffSeconds = 5
	}
	if c.Mattermost.RequestTimeoutSeconds == 0 {
		c.Mattermost.RequestTimeoutSeconds = 30
	}
	for i := range c.Users {
		if c.Users[i].Routing.ThreadPerConversation == nil {
			enabled := true
			c.Users[i].Routing.ThreadPerConversation = &enabled
		}
	}
}

// Validate reports every problem it can find, so that fixing a config file
// does not turn into one restart per mistake.
func (c *Config) Validate() error {
	var problems []error

	if c.RootDir == "" {
		problems = append(problems, errors.New("root_dir is required"))
	} else if !filepath.IsAbs(c.RootDir) {
		problems = append(problems, fmt.Errorf("root_dir %q must be an absolute path", c.RootDir))
	}

	switch c.Backend.Driver {
	case DatabaseDriverSQLite:
		// The postgres block, if present, is simply not read.
	case DatabaseDriverPostgres:
		if c.Backend.Postgres.DSNRef == "" {
			problems = append(problems, errors.New("backend.postgres.dsn_ref is required when backend.driver is \"postgres\""))
		}
	default:
		problems = append(problems, fmt.Errorf("backend.driver %q must be %q or %q",
			c.Backend.Driver, DatabaseDriverSQLite, DatabaseDriverPostgres))
	}

	if len(c.Users) == 0 {
		problems = append(problems, errors.New("users must have at least one entry"))
	}
	seenNames := make(map[string]bool, len(c.Users))
	for i, user := range c.Users {
		label := fmt.Sprintf("users[%d]", i)
		if user.Name != "" {
			label = fmt.Sprintf("users[%d] (%s)", i, user.Name)
		}

		if user.Name == "" {
			problems = append(problems, fmt.Errorf("%s: name is required", label))
		} else if strings.Contains(user.Name, "/") {
			problems = append(problems, fmt.Errorf(
				"%s: name %q must not contain \"/\": it is used as a filesystem path component in the derived session path", label, user.Name))
		} else if seenNames[user.Name] {
			problems = append(problems, fmt.Errorf("%s: name %q is used by more than one user", label, user.Name))
		} else {
			seenNames[user.Name] = true
		}

		for _, problem := range validateRules(user.Routing.DefaultDirect, user.Routing.DefaultGroup, user.Routing.Rules) {
			problems = append(problems, fmt.Errorf("%s: %w", label, problem))
		}
	}

	if c.Mattermost.URL == "" {
		problems = append(problems, errors.New("mattermost.url is required"))
	} else if !secrets.LooksLikeReference(c.Mattermost.URL) &&
		!strings.HasPrefix(c.Mattermost.URL, "http://") && !strings.HasPrefix(c.Mattermost.URL, "https://") {
		// A reference's resolved shape can't be checked without I/O, which
		// Load does not do; MattermostURL re-checks this after resolving.
		problems = append(problems, fmt.Errorf("mattermost.url %q must start with http:// or https://", c.Mattermost.URL))
	}
	if c.Mattermost.TokenRef == "" {
		problems = append(problems, errors.New("mattermost.token_ref is required"))
	}

	if c.Listener.Port != 0 && (c.Listener.Port < 1 || c.Listener.Port > 65535) {
		problems = append(problems, fmt.Errorf("listener.port %d must be between 1 and 65535", c.Listener.Port))
	}

	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		problems = append(problems, fmt.Errorf("log.level %q must be debug, info, warn or error", c.Log.Level))
	}
	switch c.Log.Format {
	case "text", "json":
	default:
		problems = append(problems, fmt.Errorf("log.format %q must be text or json", c.Log.Format))
	}

	return errors.Join(problems...)
}

// Validate checks that a destination names something Mattermost can resolve.
func (d Destination) Validate() error {
	switch d.Type {
	case DestChannel:
		if d.Team == "" || d.Channel == "" {
			return errors.New(`type "channel" needs both "team" and "channel"`)
		}
	case DestChannelID:
		if d.ChannelID == "" {
			return errors.New(`type "channel_id" needs "channel_id"`)
		}
	case DestDirect:
		if d.User == "" {
			return errors.New(`type "direct" needs "user"`)
		}
	case DestGroup:
		if len(d.Users) < 2 {
			return errors.New(`type "group" needs at least two entries in "users"`)
		}
		if len(d.Users) > 7 {
			return fmt.Errorf(`type "group" allows at most 7 entries in "users", got %d`, len(d.Users))
		}
	case "":
		return fmt.Errorf(`"type" is required: one of %q, %q, %q, %q`,
			DestChannel, DestChannelID, DestDirect, DestGroup)
	default:
		return fmt.Errorf(`unknown type %q: expected one of %q, %q, %q, %q`,
			d.Type, DestChannel, DestChannelID, DestDirect, DestGroup)
	}
	return nil
}

// String renders a destination for logs.
func (d Destination) String() string {
	switch d.Type {
	case DestChannel:
		return "~" + d.Team + "/" + d.Channel
	case DestChannelID:
		return "channel " + d.ChannelID
	case DestDirect:
		return "DM with @" + d.User
	case DestGroup:
		return "group DM with @" + strings.Join(d.Users, ", @")
	default:
		return "invalid destination"
	}
}

// ValidateRules checks one user's routing.default_direct, routing.default_group
// and routing.rules in isolation from the rest of config.json — the same
// checks Validate applies per-user, factored out so a client can validate a
// rules document locally (see rulesproto.RulesDocument) before submitting it,
// without needing the rest of config.json to do it.
func ValidateRules(defaultDirect, defaultGroup Destination, rules []Rule) error {
	return errors.Join(validateRules(defaultDirect, defaultGroup, rules)...)
}

// validateRules is ValidateRules' implementation, returning each problem as
// a separate error so Validate can prefix every one individually with the
// user it belongs to, instead of collapsing them all behind one shared label.
func validateRules(defaultDirect, defaultGroup Destination, rules []Rule) []error {
	var problems []error

	if err := defaultDirect.Validate(); err != nil {
		problems = append(problems, fmt.Errorf("routing.default_direct: %w", err))
	}
	if defaultGroup.Type != "" {
		if err := defaultGroup.Validate(); err != nil {
			problems = append(problems, fmt.Errorf("routing.default_group: %w", err))
		}
	}
	problems = append(problems, validateRuleList(rules)...)

	return problems
}

// ValidateRuleList checks a list of rules in isolation from any
// default_direct/default_group — each rule's own criteria, shape filters,
// name_pattern and destination. This is the narrower check for a rules-only
// push (see rulesproto.RulesUpdate): unlike ValidateRules, it says nothing
// about whether a fallback destination is configured, since a rules-only
// push cannot change that — default_direct/default_group are operator-set
// and deliberately outside what "msg-gw rules push" can touch, so that a
// user's own (or a generated) rules change can never leave them without a
// working fallback that guarantees message delivery.
func ValidateRuleList(rules []Rule) error {
	return errors.Join(validateRuleList(rules)...)
}

func validateRuleList(rules []Rule) []error {
	var problems []error
	for j, rule := range rules {
		ruleLabel := fmt.Sprintf("routing.rules[%d]", j)
		if rule.Name != "" {
			ruleLabel = fmt.Sprintf("routing.rules[%d] (%s)", j, rule.Name)
		}
		if err := rule.Validate(); err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", ruleLabel, err))
		}
	}
	return problems
}

// Validate checks that a rule is well-formed in isolation: it has at least
// one matching criterion, its shape filters aren't mutually exclusive, its
// name_pattern (if any) compiles, and its destination is valid.
func (r Rule) Validate() error {
	var problems []error

	if len(r.ConversationIDs) == 0 && len(r.Phones) == 0 && r.NamePattern == "" &&
		!r.GroupsOnly && !r.DirectsOnly {
		problems = append(problems, errors.New("has no criteria, so it would never match"))
	}
	if r.GroupsOnly && r.DirectsOnly {
		problems = append(problems, errors.New("sets both groups_only and directs_only, so it can never match"))
	}
	if r.NamePattern != "" {
		if _, err := regexp.Compile(r.NamePattern); err != nil {
			problems = append(problems, fmt.Errorf("name_pattern is not a valid regular expression: %w", err))
		}
	}
	if err := r.Destination.Validate(); err != nil {
		problems = append(problems, fmt.Errorf("destination: %w", err))
	}

	return errors.Join(problems...)
}

// SessionRef derives where a user's Google Messages session lives: always
// a local encoded: file under RootDir, since libgm rewrites it roughly
// hourly and it therefore cannot live behind Vault or Postgres. It is a
// pure function of RootDir and user.Name — both already validated
// non-empty and path-safe by Validate — so unlike SQLitePath/MattermostURL
// this does no I/O and cannot fail.
func (c *Config) SessionRef(user UserConfig) string {
	return "encoded:" + filepath.Join(c.RootDir, "gmessages", user.Name+"_session.enc")
}

// SQLitePath resolves backend.sqlite.path — which may be a plain path or a
// secret reference — and, if the result is relative, joins it under
// StateDir. Reaching out to Vault, a file or the environment means this does
// I/O, unlike everything in this package's Load path.
func (c *Config) SQLitePath() (string, error) {
	path, err := secrets.MaybeResolve(c.Backend.SQLite.Path, c.Vault)
	if err != nil {
		return "", fmt.Errorf("backend.sqlite.path: %w", err)
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(c.StateDir, path)
	}
	return path, nil
}

// PIDFile is where "daemon" records its own process ID, so that "reload" can
// find it without the operator having to track it separately. It is always
// under StateDir — not configurable, since one daemon has exactly one
// StateDir and there is no scenario where the two need to move independently.
func (c *Config) PIDFile() string {
	return filepath.Join(c.StateDir, "msggw.pid")
}

// MattermostURL resolves mattermost.url — which may be a plain URL or a
// secret reference — and checks that the resolved value is still a URL.
func (c *Config) MattermostURL() (string, error) {
	url, err := secrets.MaybeResolve(c.Mattermost.URL, c.Vault)
	if err != nil {
		return "", fmt.Errorf("mattermost.url: %w", err)
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return "", fmt.Errorf("mattermost.url resolved to %q, which must start with http:// or https://", url)
	}
	return url, nil
}

// RequestTimeout is the per-REST-call bound.
func (c *Config) RequestTimeout() time.Duration {
	return time.Duration(c.Mattermost.RequestTimeoutSeconds) * time.Second
}

// ReconnectBackoff is the initial WebSocket reconnect delay.
func (c *Config) ReconnectBackoff() time.Duration {
	return time.Duration(c.Mattermost.ReconnectBackoffSeconds) * time.Second
}

// PingInterval is how often libgm should ping the phone; 0 keeps its default.
func (g GMessagesConfig) PingInterval() time.Duration {
	return time.Duration(g.PingIntervalSeconds) * time.Second
}

// SlogLevel maps the configured level onto slog's.
func (c *Config) SlogLevel() slog.Level {
	switch c.Log.Level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// NewLogger builds the daemon's logger from the log section.
func (c *Config) NewLogger(w *os.File) *slog.Logger {
	opts := &slog.HandlerOptions{Level: c.SlogLevel()}
	if c.Log.Format == "json" {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}
