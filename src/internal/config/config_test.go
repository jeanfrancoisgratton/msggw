// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 20:10:51
// Original filename: src/internal/config/config_test.go

package config

import (
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

	if cfg.Routing.Default.Type != DestChannel {
		t.Errorf("sample default route type = %q, want %q", cfg.Routing.Default.Type, DestChannel)
	}
	if len(cfg.Routing.Rules) == 0 {
		t.Error("the sample has no routing rules, so it does not demonstrate routing")
	}
	if !cfg.ThreadPerConversationEnabled() {
		t.Error("the sample turns threads off, which is not the documented default")
	}
}

const minimalConfig = `{
  "gmessages": {"session_ref": "file:/tmp/session.json"},
  "mattermost": {"url": "https://mm.example.net", "token_ref": "env:TOKEN"},
  "routing": {"default": {"type": "channel", "team": "t", "channel": "c"}}
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
	if !cfg.ThreadPerConversationEnabled() {
		t.Error("thread_per_conversation should default to on")
	}
	if cfg.RequestTimeout() == 0 || cfg.ReconnectBackoff() == 0 {
		t.Error("the Mattermost timeouts should have non-zero defaults")
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	body := `{"state_dir": "/tmp", "state_directory": "/tmp",
	  "gmessages": {"session_ref": "file:/tmp/s"},
	  "mattermost": {"url": "https://mm", "token_ref": "env:T"},
	  "routing": {"default": {"type": "channel", "team": "t", "channel": "c"}}}`

	_, err := Load(writeConfig(t, body))
	if err == nil {
		t.Fatal("a misspelled key was accepted; it should be rejected")
	}
	if !strings.Contains(err.Error(), "state_directory") {
		t.Errorf("the error does not name the offending key: %v", err)
	}
}

// TestSessionRefMustBeWritable covers the trap that would otherwise cost a
// re-pairing on every restart: libgm refreshes its auth token and the new one
// has to go somewhere.
func TestSessionRefMustBeWritable(t *testing.T) {
	for _, ref := range []string{"env:GM_SESSION", "literal:{}"} {
		body := strings.Replace(minimalConfig, "file:/tmp/session.json", ref, 1)
		_, err := Load(writeConfig(t, body))
		if err == nil {
			t.Errorf("session_ref %q was accepted, but it cannot be written back", ref)
			continue
		}
		if !strings.Contains(err.Error(), "session_ref") {
			t.Errorf("session_ref %q: the error does not explain the problem: %v", ref, err)
		}
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
	  "gmessages": {"session_ref": "file:/tmp/s"},
	  "mattermost": {"url": "https://mm", "token_ref": "env:T"},
	  "routing": {
	    "default": {"type": "channel", "team": "t", "channel": "c"},
	    "rules": [{"name": "oops", "destination": {"type": "direct", "user": "jf"}}]
	  }}`

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
	body := strings.Replace(minimalConfig, `"gmessages"`, `"backend": {"driver": "postgres"}, "gmessages"`, 1)

	_, err := Load(writeConfig(t, body))
	if err == nil {
		t.Fatal("backend.driver \"postgres\" without backend.postgres.dsn_ref was accepted")
	}
	if !strings.Contains(err.Error(), "backend.postgres.dsn_ref") {
		t.Errorf("the error does not name the missing field: %v", err)
	}
}

func TestPostgresDriverIsAccepted(t *testing.T) {
	body := strings.Replace(minimalConfig, `"gmessages"`,
		`"backend": {"driver": "postgres", "postgres": {"dsn_ref": "env:PG_DSN"}}, "gmessages"`, 1)

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
	body := strings.Replace(minimalConfig, `"gmessages"`,
		`"backend": {"driver": "sqlite", "sqlite": {"path": "msggw.db"}, "postgres": {"dsn_ref": "env:PG_DSN"}}, "gmessages"`, 1)

	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("a fully-populated postgres block alongside the active sqlite driver was rejected: %v", err)
	}
	if cfg.Backend.Postgres.DSNRef != "env:PG_DSN" {
		t.Errorf("Backend.Postgres.DSNRef = %q, want it preserved even though sqlite is active", cfg.Backend.Postgres.DSNRef)
	}
}

func TestUnknownDatabaseDriverIsRejected(t *testing.T) {
	body := strings.Replace(minimalConfig, `"gmessages"`, `"backend": {"driver": "mysql"}, "gmessages"`, 1)

	_, err := Load(writeConfig(t, body))
	if err == nil {
		t.Fatal("an unknown backend.driver was accepted")
	}
	if !strings.Contains(err.Error(), "backend.driver") {
		t.Errorf("the error does not name the offending field: %v", err)
	}
}

func TestRuleWithBothShapeFiltersIsRejected(t *testing.T) {
	body := `{
	  "gmessages": {"session_ref": "file:/tmp/s"},
	  "mattermost": {"url": "https://mm", "token_ref": "env:T"},
	  "routing": {
	    "default": {"type": "channel", "team": "t", "channel": "c"},
	    "rules": [{"name": "both", "groups_only": true, "directs_only": true,
	               "destination": {"type": "direct", "user": "jf"}}]
	  }}`

	if _, err := Load(writeConfig(t, body)); err == nil {
		t.Fatal("a rule that is both groups-only and directs-only was accepted")
	}
}
