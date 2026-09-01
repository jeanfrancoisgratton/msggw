// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/internal/config/mutate_test.go

package config

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// TestMutateAppendsAndPersists covers the basic round trip: fn's change is
// both reflected in the returned Config and durably on disk.
func TestMutateAppendsAndPersists(t *testing.T) {
	path := writeConfig(t, minimalConfig)

	cfg, err := Mutate(path, func(c *Config) error {
		c.Users[0].Routing.Rules = append(c.Users[0].Routing.Rules, Rule{
			Name:        "added",
			Phones:      []string{"+15145551212"},
			Destination: Destination{Type: DestDirect, User: "jf"},
		})
		return nil
	})
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if len(cfg.Users[0].Routing.Rules) != 1 || cfg.Users[0].Routing.Rules[0].Name != "added" {
		t.Fatalf("returned Config does not reflect the mutation: %+v", cfg.Users[0].Routing.Rules)
	}

	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("reloading %s: %v", path, err)
	}
	if len(reloaded.Users[0].Routing.Rules) != 1 || reloaded.Users[0].Routing.Rules[0].Name != "added" {
		t.Fatalf("the change was not persisted to disk: %+v", reloaded.Users[0].Routing.Rules)
	}
}

// TestMutateDoesNotMaterializeDefaults guards the whole reason Mutate
// operates on the raw decode rather than a Load'd (defaulted) Config: a
// value-typed field (string, slice, pointer) the operator never wrote must
// still be absent afterward, not expanded into an explicit default on the
// first mutation to touch the file.
func TestMutateDoesNotMaterializeDefaults(t *testing.T) {
	path := writeConfig(t, minimalConfig)

	if _, err := Mutate(path, func(c *Config) error {
		c.Users[0].Routing.Rules = append(c.Users[0].Routing.Rules, Rule{
			GroupsOnly:  true,
			Destination: Destination{Type: DestDirect, User: "jf"},
		})
		return nil
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	body := string(raw)
	if strings.Contains(body, "state_dir") {
		t.Errorf("state_dir was materialized into the file, though the operator never set it:\n%s", body)
	}
	if strings.Contains(body, "thread_per_conversation") {
		t.Errorf("thread_per_conversation was materialized into the file, though the operator never set it:\n%s", body)
	}
}

// TestMutatePreservesAlreadySetValues covers the realistic case (a
// deployment whose top-level sections are already filled in, per the
// sample): a mutation to one user's routing must not disturb any value
// already present elsewhere in the file, in either shape or content.
func TestMutatePreservesAlreadySetValues(t *testing.T) {
	body := `{
	  "state_dir": "/var/lib/msggw",
	  "root_dir": "/data",
	  "backend": {"driver": "sqlite", "sqlite": {"path": "msggw.db"}},
	  "log": {"level": "debug", "format": "json"},
	  "vault": {"address": "https://vault.example.net:8200", "token_ref": "env:VAULT_TOKEN"},
	  "mattermost": {"url": "https://mm.example.net", "token_ref": "env:TOKEN"},
	  "listener": {"port": 8443, "cert_file": "/etc/msggw/tls/cert", "key_file": "/etc/msggw/tls/key"},
	  "users": [
	    {"name": "u1",
	     "gmessages": {"force_rcs": true},
	     "routing": {"default_direct": {"type": "channel", "team": "t", "channel": "c"}}},
	    {"name": "u2",
	     "routing": {"default_direct": {"type": "direct", "user": "u2"}}}
	  ]}`
	path := writeConfig(t, body)

	cfg, err := Mutate(path, func(c *Config) error {
		c.Users[0].Routing.Rules = append(c.Users[0].Routing.Rules, Rule{
			Name:        "added",
			Phones:      []string{"+15145551212"},
			Destination: Destination{Type: DestDirect, User: "jf"},
		})
		return nil
	})
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}

	if cfg.Log.Level != "debug" || cfg.Log.Format != "json" {
		t.Errorf("log settings changed: %+v", cfg.Log)
	}
	if cfg.Vault.Address != "https://vault.example.net:8200" {
		t.Errorf("vault.address changed: %q", cfg.Vault.Address)
	}
	if cfg.Listener.Port != 8443 {
		t.Errorf("listener.port changed: %d", cfg.Listener.Port)
	}
	if !cfg.Users[0].GMessages.ForceRCS {
		t.Errorf("users[0].gmessages.force_rcs changed: %+v", cfg.Users[0].GMessages)
	}
	if want := (Destination{Type: DestDirect, User: "u2"}); !reflect.DeepEqual(cfg.Users[1].Routing.DefaultDirect, want) {
		t.Errorf("the untouched second user's routing changed: %+v", cfg.Users[1].Routing.DefaultDirect)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if !strings.Contains(string(raw), `"level": "debug"`) {
		t.Errorf("log.level was not preserved verbatim on disk:\n%s", raw)
	}
}

// TestMutateRejectsInvalidResult covers fn producing a configuration Load
// would refuse: the original file must survive untouched.
func TestMutateRejectsInvalidResult(t *testing.T) {
	path := writeConfig(t, minimalConfig)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	_, err = Mutate(path, func(c *Config) error {
		// A rule with no criteria never matches — Validate rejects it.
		c.Users[0].Routing.Rules = append(c.Users[0].Routing.Rules, Rule{
			Name:        "broken",
			Destination: Destination{Type: DestDirect, User: "jf"},
		})
		return nil
	})
	if err == nil {
		t.Fatal("a mutation producing an invalid configuration was accepted")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if string(after) != string(before) {
		t.Error("the original file was modified despite the mutation being rejected")
	}
}

// TestMutateFnErrorLeavesFileUntouched covers fn refusing the mutation
// itself (e.g. "no rule named that"), before Mutate ever gets to disk.
func TestMutateFnErrorLeavesFileUntouched(t *testing.T) {
	path := writeConfig(t, minimalConfig)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	sentinel := errWithMessage("fn refused")
	_, err = Mutate(path, func(c *Config) error { return sentinel })
	if err != sentinel {
		t.Fatalf("Mutate() error = %v, want the fn error unchanged", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if string(after) != string(before) {
		t.Error("the file was modified even though fn returned an error")
	}
}

// TestMutateDetectsConcurrentChange covers a second writer racing Mutate:
// fn simulates that writer by changing the file on disk mid-mutation, which
// Mutate must notice and refuse to clobber.
func TestMutateDetectsConcurrentChange(t *testing.T) {
	path := writeConfig(t, minimalConfig)

	_, err := Mutate(path, func(c *Config) error {
		if err := os.WriteFile(path, []byte(strings.Replace(minimalConfig, `"name": "u1"`, `"name": "raced"`, 1)), 0o644); err != nil {
			t.Fatalf("simulating a concurrent write: %v", err)
		}
		c.Users[0].Routing.Rules = append(c.Users[0].Routing.Rules, Rule{
			Name:        "added",
			GroupsOnly:  true,
			Destination: Destination{Type: DestDirect, User: "jf"},
		})
		return nil
	})
	if err == nil {
		t.Fatal("a concurrent change to the file went undetected")
	}

	reloaded, loadErr := Load(path)
	if loadErr != nil {
		t.Fatalf("the concurrently-written file should still load: %v", loadErr)
	}
	if reloaded.Users[0].Name != "raced" {
		t.Error("Mutate overwrote the concurrent writer's change instead of refusing to proceed")
	}
}

// TestMutatePreservesFileMode covers that Mutate does not loosen or tighten
// permissions on the file it replaces.
func TestMutatePreservesFileMode(t *testing.T) {
	path := writeConfig(t, minimalConfig)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	if _, err := Mutate(path, func(c *Config) error { return nil }); err != nil {
		t.Fatalf("Mutate: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, want 0600 preserved", info.Mode().Perm())
	}
}

// errWithMessage is a trivial error type distinct from errors.New's, only so
// TestMutateFnErrorLeavesFileUntouched can compare by identity.
type errWithMessage string

func (e errWithMessage) Error() string { return string(e) }
