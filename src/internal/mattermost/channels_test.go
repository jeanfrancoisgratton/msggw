// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>

package mattermost

import (
	"strings"
	"testing"

	"msggw/internal/config"
	"msggw/internal/secrets"
)

func TestResolveDestinationRefsResolvesEachField(t *testing.T) {
	t.Setenv("RCS_TEST_TEAM", "myteam")
	t.Setenv("RCS_TEST_USER", "jfgratton")

	dest := config.Destination{
		Type:      config.DestGroup,
		Team:      "env:RCS_TEST_TEAM",
		Channel:   "plain-channel",
		ChannelID: "plain-id",
		User:      "env:RCS_TEST_USER",
		Users:     []string{"literal:a", "b", "env:RCS_TEST_USER"},
	}

	resolved, err := resolveDestinationRefs(dest, secrets.VaultConfig{})
	if err != nil {
		t.Fatalf("resolveDestinationRefs: %v", err)
	}

	if resolved.Team != "myteam" {
		t.Errorf("Team = %q, want %q", resolved.Team, "myteam")
	}
	if resolved.Channel != "plain-channel" {
		t.Errorf("Channel = %q, want it unchanged", resolved.Channel)
	}
	if resolved.ChannelID != "plain-id" {
		t.Errorf("ChannelID = %q, want it unchanged", resolved.ChannelID)
	}
	if resolved.User != "jfgratton" {
		t.Errorf("User = %q, want %q", resolved.User, "jfgratton")
	}
	wantUsers := []string{"a", "b", "jfgratton"}
	if len(resolved.Users) != len(wantUsers) {
		t.Fatalf("Users = %v, want %v", resolved.Users, wantUsers)
	}
	for i, want := range wantUsers {
		if resolved.Users[i] != want {
			t.Errorf("Users[%d] = %q, want %q", i, resolved.Users[i], want)
		}
	}
}

// TestResolveDestinationRefsDoesNotMutateCaller guards against a subtle
// aliasing bug: config.Destination.Users is a slice, so a copy of the
// Destination still shares its backing array with the caller's config
// (which may also be cached inside a Router's compiled rules). Resolving
// references must not corrupt that shared backing array.
func TestResolveDestinationRefsDoesNotMutateCaller(t *testing.T) {
	t.Setenv("RCS_TEST_USER", "resolved-user")

	original := config.Destination{
		Type:  config.DestGroup,
		Users: []string{"env:RCS_TEST_USER", "plain"},
	}

	if _, err := resolveDestinationRefs(original, secrets.VaultConfig{}); err != nil {
		t.Fatalf("resolveDestinationRefs: %v", err)
	}

	if original.Users[0] != "env:RCS_TEST_USER" {
		t.Errorf("the caller's Users[0] was mutated to %q", original.Users[0])
	}
}

func TestResolveDestinationRefsErrorNamesField(t *testing.T) {
	dest := config.Destination{Type: config.DestDirect, User: "env:RCS_TEST_DEFINITELY_UNSET"}

	_, err := resolveDestinationRefs(dest, secrets.VaultConfig{})
	if err == nil {
		t.Fatal("an unresolvable destination user was accepted")
	}
	if !strings.Contains(err.Error(), "destination user") {
		t.Errorf("the error does not name the offending field: %v", err)
	}
}
