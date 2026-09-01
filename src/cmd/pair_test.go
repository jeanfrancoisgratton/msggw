// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/cmd/pair_test.go

package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"msggw/internal/config"
)

// writePairTestConfig writes a minimal, valid configuration with one
// existing user ("existing") and returns the loaded Config.
func writePairTestConfig(t *testing.T) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{
	  "root_dir": "/tmp",
	  "mattermost": {"url": "https://mm.example.net", "token_ref": "env:TOKEN"},
	  "users": [
	    {"name": "existing",
	     "routing": {"default_direct": {"type": "channel", "team": "t", "channel": "c"}}}
	  ]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing test config: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("loading test config: %v", err)
	}
	return cfg
}

// resetPairFlags clears the pair command's package-level flag variables
// between subtests, since cobra flags are plain package state.
func resetPairFlags() {
	pairEmail = ""
	pairMattermostServer = ""
	pairMattermostUser = ""
}

func TestProvisionUserRequiresMattermostUserForNewName(t *testing.T) {
	resetPairFlags()
	cfg := writePairTestConfig(t)

	_, _, err := provisionUser(&bytes.Buffer{}, cfg, "brandnew")
	if err == nil {
		t.Fatal("provisioning a new user without --mattermost-user was accepted")
	}
	if !strings.Contains(err.Error(), "--mattermost-user") {
		t.Errorf("error does not mention --mattermost-user: %v", err)
	}

	reloaded, err := config.Load(cfg.Path())
	if err != nil {
		t.Fatalf("reloading: %v", err)
	}
	if len(reloaded.Users) != 1 {
		t.Errorf("a user was created despite the rejected request: %d users", len(reloaded.Users))
	}
}

func TestProvisionUserCreatesNewUser(t *testing.T) {
	resetPairFlags()
	defer resetPairFlags()
	cfg := writePairTestConfig(t)
	pairMattermostUser = "newkid"
	pairEmail = "newkid@example.com"

	newCfg, user, err := provisionUser(&bytes.Buffer{}, cfg, "newkid")
	if err != nil {
		t.Fatalf("provisionUser: %v", err)
	}
	if user.Name != "newkid" {
		t.Errorf("user.Name = %q, want %q", user.Name, "newkid")
	}
	if user.GMessages.GoogleAccount != "newkid@example.com" {
		t.Errorf("GoogleAccount = %q, want the --email value", user.GMessages.GoogleAccount)
	}
	if user.Routing.DefaultDirect.Type != config.DestDirect || user.Routing.DefaultDirect.User != "newkid" {
		t.Errorf("DefaultDirect = %+v, want a direct message with @newkid", user.Routing.DefaultDirect)
	}

	reloaded, err := config.Load(newCfg.Path())
	if err != nil {
		t.Fatalf("reloading: %v", err)
	}
	if len(reloaded.Users) != 2 {
		t.Fatalf("len(Users) = %d, want 2 (the existing user plus the new one)", len(reloaded.Users))
	}
}

func TestProvisionUserIgnoresFlagsForExistingUser(t *testing.T) {
	resetPairFlags()
	defer resetPairFlags()
	cfg := writePairTestConfig(t)
	pairMattermostUser = "someone-else"
	pairEmail = "someone@example.com"

	var out bytes.Buffer
	_, user, err := provisionUser(&out, cfg, "existing")
	if err != nil {
		t.Fatalf("provisionUser: %v", err)
	}
	if user.Routing.DefaultDirect.Team != "t" || user.Routing.DefaultDirect.Channel != "c" {
		t.Errorf("the existing user's routing was overwritten: %+v", user.Routing.DefaultDirect)
	}
	if user.GMessages.GoogleAccount != "" {
		t.Errorf("GoogleAccount was set on an existing user: %q", user.GMessages.GoogleAccount)
	}
	if !strings.Contains(out.String(), "already exists") {
		t.Errorf("no notice was printed about the ignored flags: %q", out.String())
	}
}

func TestProvisionUserValidatesMattermostServerForExistingUser(t *testing.T) {
	resetPairFlags()
	defer resetPairFlags()
	cfg := writePairTestConfig(t)
	pairMattermostServer = "https://wrong.example.net"

	if _, _, err := provisionUser(&bytes.Buffer{}, cfg, "existing"); err == nil {
		t.Fatal("a mismatched --mattermost-server was accepted for an existing user")
	}
}

func TestCheckMattermostServer(t *testing.T) {
	resetPairFlags()
	cfg := writePairTestConfig(t)

	if err := checkMattermostServer(cfg, "https://mm.example.net"); err != nil {
		t.Errorf("an exact match was rejected: %v", err)
	}
	if err := checkMattermostServer(cfg, "https://mm.example.net/"); err != nil {
		t.Errorf("a match differing only by a trailing slash was rejected: %v", err)
	}
	if err := checkMattermostServer(cfg, "https://other.example.net"); err == nil {
		t.Error("a mismatched server was accepted")
	}
}
