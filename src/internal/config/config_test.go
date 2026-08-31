// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 20:10:51
// Original filename: src/internal/config/config_test.go

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfig writes body to a temporary file and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing the test configuration: %v", err)
	}
	return path
}

// TestSampleIsValid guards the sample the "config sample" command prints: an
// invalid sample turns every new deployment into a debugging session.
func TestSampleIsValid(t *testing.T) {
	cfg, err := Load(writeConfig(t, Sample()))
	if err != nil {
		t.Fatalf("the shipped sample configuration does not load: %v", err)
	}
	if len(cfg.Users) == 0 {
		t.Fatal("the sample has no users")
	}

	first := cfg.Users[0]
	if first.Routing.Default.Type != DestChannel {
		t.Errorf("sample default route type = %q, want %q", first.Routing.Default.Type, DestChannel)
	}
	if len(first.Routing.Rules) == 0 {
		t.Error("the sample's first user has no routing rules, so it does not demonstrate routing")
	}
	if !first.Routing.ThreadPerConversationEnabled() {
		t.Error("the sample turns threads off, which is not the documented default")
	}
}

// minimalConfig is the smallest configuration Load accepts: one user, one
// routing default, a Mattermost URL and token.
const minimalConfig = `{
  "root_dir": "/tmp",
  "mattermost": {"url": "https://mm.example.net", "token_ref": "env:TOKEN"},
  "users": [
    {"name": "u1",
     "routing": {"default": {"type": "channel", "team": "t", "channel": "c"}}}
  ]
}`

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.StateDir != "/var/lib/msggw" {
		t.Errorf("StateDir = %q, want the documented default", cfg.StateDir)
	}
	// A relative database name has to end up inside the state directory, or
	// the daemon writes it into whatever directory it was started from.
	if want := "/var/lib/msggw/msggw.db"; cfg.Backend.SQLite.Path != want {
		t.Errorf("Backend.SQLite.Path = %q, want %q", cfg.Backend.SQLite.Path, want)
	}
	if len(cfg.Users) != 1 {
		t.Fatalf("len(Users) = %d, want 1", len(cfg.Users))
	}
	if !cfg.Users[0].Routing.ThreadPerConversationEnabled() {
		t.Error("thread_per_conversation should default to on")
	}
	if cfg.RequestTimeout() == 0 || cfg.ReconnectBackoff() == 0 {
		t.Error("the Mattermost timeouts should have non-zero defaults")
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	body := `{"state_dir": "/tmp", "state_directory": "/tmp", "root_dir": "/tmp",
	  "mattermost": {"url": "https://mm", "token_ref": "env:T"},
	  "users": [{"name": "u1",
	             "routing": {"default": {"type": "channel", "team": "t", "channel": "c"}}}]}`

	_, err := Load(writeConfig(t, body))
	if err == nil {
		t.Fatal("a misspelled key was accepted; it should be rejected")
	}
	if !strings.Contains(err.Error(), "state_directory") {
		t.Errorf("the error does not name the offending key: %v", err)
	}
}

// TestRootDirIsRequired covers the field every user's derived session path
// now depends on: without it, SessionRef would silently join onto "", not
// fail loudly.
func TestRootDirIsRequired(t *testing.T) {
	body := strings.Replace(minimalConfig, `"root_dir": "/tmp",`, "", 1)
	_, err := Load(writeConfig(t, body))
	if err == nil {
		t.Fatal("a missing root_dir was accepted")
	}
	if !strings.Contains(err.Error(), "root_dir") {
		t.Errorf("the error does not mention root_dir: %v", err)
	}
}

// TestRootDirMustBeAbsolute covers a relative root_dir, which would resolve
// against whatever directory the daemon happens to be started from —
// silently undermining the whole point of deriving session paths
// deterministically.
func TestRootDirMustBeAbsolute(t *testing.T) {
	body := strings.Replace(minimalConfig, `"root_dir": "/tmp",`, `"root_dir": "data",`, 1)
	_, err := Load(writeConfig(t, body))
	if err == nil {
		t.Fatal("a relative root_dir was accepted")
	}
	if !strings.Contains(err.Error(), "root_dir") {
		t.Errorf("the error does not mention root_dir: %v", err)
	}
}

// TestSessionRefDerivedFromRootDirAndName guards the actual contract: a
// user's session always lives at a deterministic, collision-free path built
// from root_dir and their name, never something hand-written.
func TestSessionRefDerivedFromRootDirAndName(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := "encoded:/tmp/gmessages/u1_session.enc"
	if got := cfg.SessionRef(cfg.Users[0]); got != want {
		t.Errorf("SessionRef() = %q, want %q", got, want)
	}
}

// TestUserNameRejectsSlash covers the path-escape footgun a "/" in a user's
// name would otherwise be, now that Name feeds a filesystem path component
// via SessionRef.
func TestUserNameRejectsSlash(t *testing.T) {
	body := strings.Replace(minimalConfig, `"name": "u1"`, `"name": "u/1"`, 1)
	_, err := Load(writeConfig(t, body))
	if err == nil {
		t.Fatal("a user name containing \"/\" was accepted")
	}
	if !strings.Contains(err.Error(), `"u/1"`) {
		t.Errorf("the error does not name the offending value: %v", err)
	}
}

func TestDestinationValidate(t *testing.T) {
	tests := []struct {
		name string
		dest Destination
		ok   bool
	}{
		{"channel by name", Destination{Type: DestChannel, Team: "t", Channel: "c"}, true},
		{"channel missing team", Destination{Type: DestChannel, Channel: "c"}, false},
		{"channel by id", Destination{Type: DestChannelID, ChannelID: "abc"}, true},
		{"channel id missing", Destination{Type: DestChannelID}, false},
		{"direct", Destination{Type: DestDirect, User: "jf"}, true},
		{"direct missing user", Destination{Type: DestDirect}, false},
		{"group", Destination{Type: DestGroup, Users: []string{"a", "b"}}, true},
		{"group too small", Destination{Type: DestGroup, Users: []string{"a"}}, false},
		{"group too large", Destination{Type: DestGroup, Users: []string{"a", "b", "c", "d", "e", "f", "g", "h"}}, false},
		{"no type", Destination{}, false},
		{"unknown type", Destination{Type: "carrier pigeon"}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.dest.Validate()
			if tc.ok && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
			if !tc.ok && err == nil {
				t.Error("Validate() = nil, want an error")
			}
		})
	}
}

func TestRuleWithoutCriteriaIsRejected(t *testing.T) {
	body := `{
	  "root_dir": "/tmp",
	  "mattermost": {"url": "https://mm", "token_ref": "env:T"},
	  "users": [{"name": "u1",
	    "routing": {
	      "default": {"type": "channel", "team": "t", "channel": "c"},
	      "rules": [{"name": "oops", "destination": {"type": "direct", "user": "jf"}}]
	    }}]}`

	_, err := Load(writeConfig(t, body))
	if err == nil {
		t.Fatal("a rule with no criteria was accepted; it would never match")
	}
	if !strings.Contains(err.Error(), "oops") {
		t.Errorf("the error does not name the offending rule: %v", err)
	}
}

func TestDatabaseDriverDefaultsToSQLite(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Backend.Driver != DatabaseDriverSQLite {
		t.Errorf("Backend.Driver = %q, want %q", cfg.Backend.Driver, DatabaseDriverSQLite)
	}
}

func TestPostgresDriverRequiresDSNRef(t *testing.T) {
	body := strings.Replace(minimalConfig, `"mattermost"`, `"backend": {"driver": "postgres"}, "mattermost"`, 1)

	_, err := Load(writeConfig(t, body))
	if err == nil {
		t.Fatal("backend.driver \"postgres\" without backend.postgres.dsn_ref was accepted")
	}
	if !strings.Contains(err.Error(), "backend.postgres.dsn_ref") {
		t.Errorf("the error does not name the missing field: %v", err)
	}
}

func TestPostgresDriverIsAccepted(t *testing.T) {
	body := strings.Replace(minimalConfig, `"mattermost"`,
		`"backend": {"driver": "postgres", "postgres": {"dsn_ref": "env:PG_DSN"}}, "mattermost"`, 1)

	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Even though postgres is active, the SQLite block still gets its usual
	// default: both blocks stay ready to go, so switching Driver back to
	// sqlite later does not find an empty path.
	if want := "/var/lib/msggw/msggw.db"; cfg.Backend.SQLite.Path != want {
		t.Errorf("Backend.SQLite.Path = %q, want %q", cfg.Backend.SQLite.Path, want)
	}
}

// TestBothBackendBlocksCanCoexist covers the whole point of splitting the
// backend into two nested blocks: an operator can leave both fully filled in
// and switch storage backends by changing only backend.driver.
func TestBothBackendBlocksCanCoexist(t *testing.T) {
	body := strings.Replace(minimalConfig, `"mattermost"`,
		`"backend": {"driver": "sqlite", "sqlite": {"path": "msggw.db"}, "postgres": {"dsn_ref": "env:PG_DSN"}}, "mattermost"`, 1)

	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("a fully-populated postgres block alongside the active sqlite driver was rejected: %v", err)
	}
	if cfg.Backend.Postgres.DSNRef != "env:PG_DSN" {
		t.Errorf("Backend.Postgres.DSNRef = %q, want it preserved even though sqlite is active", cfg.Backend.Postgres.DSNRef)
	}
}

func TestUnknownDatabaseDriverIsRejected(t *testing.T) {
	body := strings.Replace(minimalConfig, `"mattermost"`, `"backend": {"driver": "mysql"}, "mattermost"`, 1)

	_, err := Load(writeConfig(t, body))
	if err == nil {
		t.Fatal("an unknown backend.driver was accepted")
	}
	if !strings.Contains(err.Error(), "backend.driver") {
		t.Errorf("the error does not name the offending field: %v", err)
	}
}

// TestSQLitePathResolvesReference covers backend.sqlite.path given as a
// secret reference: applyDefaults must leave it alone (it cannot tell
// whether a resolved reference will turn out relative), and SQLitePath must
// resolve it and then apply the usual StateDir join.
func TestSQLitePathResolvesReference(t *testing.T) {
	t.Setenv("SQLITE_PATH", "sqlite/from-env.db")
	body := strings.Replace(minimalConfig, `"mattermost"`,
		`"backend": {"sqlite": {"path": "env:SQLITE_PATH"}}, "mattermost"`, 1)

	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Backend.SQLite.Path != "env:SQLITE_PATH" {
		t.Errorf("Backend.SQLite.Path = %q, want the reference left untouched by Load", cfg.Backend.SQLite.Path)
	}

	path, err := cfg.SQLitePath()
	if err != nil {
		t.Fatalf("SQLitePath: %v", err)
	}
	if want := "/var/lib/msggw/sqlite/from-env.db"; path != want {
		t.Errorf("SQLitePath() = %q, want %q", path, want)
	}
}

// TestSQLitePathPlainValueUnchanged guards the common case: Load still joins
// a plain relative path under StateDir immediately, and SQLitePath is a
// no-op on top of that.
func TestSQLitePathPlainValueUnchanged(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	path, err := cfg.SQLitePath()
	if err != nil {
		t.Fatalf("SQLitePath: %v", err)
	}
	if want := "/var/lib/msggw/msggw.db"; path != want {
		t.Errorf("SQLitePath() = %q, want %q", path, want)
	}
}

// TestMattermostURLAcceptsReference covers the case Validate must let
// through statically (it cannot check a reference's resolved shape without
// I/O) and MattermostURL must check once it is resolved.
func TestMattermostURLAcceptsReference(t *testing.T) {
	t.Setenv("MM_URL", "https://mm.example.net")
	body := strings.Replace(minimalConfig, `"url": "https://mm.example.net"`, `"url": "env:MM_URL"`, 1)

	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	url, err := cfg.MattermostURL()
	if err != nil {
		t.Fatalf("MattermostURL: %v", err)
	}
	if want := "https://mm.example.net"; url != want {
		t.Errorf("MattermostURL() = %q, want %q", url, want)
	}
}

// TestMattermostURLRejectsBadResolvedValue covers a reference that resolves
// to something that still is not a URL: the check Validate skipped for
// references has to happen somewhere.
func TestMattermostURLRejectsBadResolvedValue(t *testing.T) {
	t.Setenv("MM_URL", "not-a-url")
	body := strings.Replace(minimalConfig, `"url": "https://mm.example.net"`, `"url": "env:MM_URL"`, 1)

	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := cfg.MattermostURL(); err == nil {
		t.Error("MattermostURL() accepted a resolved value that is not a URL")
	}
}

func TestRuleWithBothShapeFiltersIsRejected(t *testing.T) {
	body := `{
	  "root_dir": "/tmp",
	  "mattermost": {"url": "https://mm", "token_ref": "env:T"},
	  "users": [{"name": "u1",
	    "routing": {
	      "default": {"type": "channel", "team": "t", "channel": "c"},
	      "rules": [{"name": "both", "groups_only": true, "directs_only": true,
	                 "destination": {"type": "direct", "user": "jf"}}]
	    }}]}`

	if _, err := Load(writeConfig(t, body)); err == nil {
		t.Fatal("a rule that is both groups-only and directs-only was accepted")
	}
}

// TestListenerPortValidation covers that 0 (the documented "disabled") is
// accepted, but an out-of-range port is not — Load never dials the port
// itself, so a typo would otherwise only surface when the daemon fails to
// bind at startup.
func TestListenerPortValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		port    int
		wantErr bool
	}{
		{"zero disables it", 0, false},
		{"a normal port", 8443, false},
		{"negative", -1, true},
		{"above 65535", 70000, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.TrimSuffix(minimalConfig, "}") +
				fmt.Sprintf(`, "listener": {"port": %d}}`, tc.port)
			_, err := Load(writeConfig(t, body))
			if tc.wantErr && err == nil {
				t.Errorf("listener.port %d was accepted, want an error", tc.port)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("listener.port %d: %v", tc.port, err)
			}
		})
	}
}

// TestUsersMustHaveAtLeastOne covers the clean-break shape: there is no
// legacy single-user layout to fall back to, so an empty (or missing) users
// array is rejected rather than silently bridging nobody.
func TestUsersMustHaveAtLeastOne(t *testing.T) {
	body := `{"mattermost": {"url": "https://mm", "token_ref": "env:T"}, "users": []}`

	_, err := Load(writeConfig(t, body))
	if err == nil {
		t.Fatal("an empty users array was accepted")
	}
	if !strings.Contains(err.Error(), "users") {
		t.Errorf("the error does not mention users: %v", err)
	}
}

// TestUserNameMustBeUnique covers the tenant identity storage keys on: two
// users named the same thing would collide in the tenant column.
func TestUserNameMustBeUnique(t *testing.T) {
	body := `{
	  "root_dir": "/tmp",
	  "mattermost": {"url": "https://mm", "token_ref": "env:T"},
	  "users": [
	    {"name": "dup",
	     "routing": {"default": {"type": "channel", "team": "t", "channel": "a"}}},
	    {"name": "dup",
	     "routing": {"default": {"type": "channel", "team": "t", "channel": "b"}}}
	  ]}`

	_, err := Load(writeConfig(t, body))
	if err == nil {
		t.Fatal("two users named \"dup\" were accepted")
	}
	if !strings.Contains(err.Error(), `"dup"`) {
		t.Errorf("the error does not name the duplicate: %v", err)
	}
}

// TestUserNameIsRequired covers a user entry with no name at all — silently
// falling back to "" would collide with every other unnamed entry in the
// tenant column.
func TestUserNameIsRequired(t *testing.T) {
	body := `{
	  "root_dir": "/tmp",
	  "mattermost": {"url": "https://mm", "token_ref": "env:T"},
	  "users": [{
	    "routing": {"default": {"type": "channel", "team": "t", "channel": "a"}}}]}`

	_, err := Load(writeConfig(t, body))
	if err == nil {
		t.Fatal("a user with no name was accepted")
	}
	if !strings.Contains(err.Error(), "name is required") {
		t.Errorf("the error does not explain the problem: %v", err)
	}
}
