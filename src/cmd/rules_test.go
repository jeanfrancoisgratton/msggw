// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/cmd/rules_test.go

package cmd

import (
	"strings"
	"testing"

	"msggw/internal/config"
)

func TestDestinationFromFlags(t *testing.T) {
	tests := []struct {
		name                     string
		channel, channelID, user string
		users                    []string
		want                     config.Destination
		wantErrSubstring         string
	}{
		{
			name:    "channel",
			channel: "myteam/messages",
			want:    config.Destination{Type: config.DestChannel, Team: "myteam", Channel: "messages"},
		},
		{
			name:      "channel id",
			channelID: "abc123",
			want:      config.Destination{Type: config.DestChannelID, ChannelID: "abc123"},
		},
		{
			name: "direct",
			user: "jfgratton",
			want: config.Destination{Type: config.DestDirect, User: "jfgratton"},
		},
		{
			name:  "group",
			users: []string{"a", "b"},
			want:  config.Destination{Type: config.DestGroup, Users: []string{"a", "b"}},
		},
		{
			name:             "nothing given",
			wantErrSubstring: "a destination is required",
		},
		{
			name:             "two given",
			channel:          "t/c",
			user:             "jf",
			wantErrSubstring: "only one of",
		},
		{
			name:             "channel missing the slash",
			channel:          "notaslash",
			wantErrSubstring: "TEAM/CHANNEL",
		},
		{
			name:             "channel with an empty team",
			channel:          "/messages",
			wantErrSubstring: "TEAM/CHANNEL",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := destinationFromFlags(tc.channel, tc.channelID, tc.user, tc.users)
			if tc.wantErrSubstring != "" {
				if err == nil {
					t.Fatalf("destinationFromFlags() = %v, nil, want an error containing %q", got, tc.wantErrSubstring)
				}
				return
			}
			if err != nil {
				t.Fatalf("destinationFromFlags(): %v", err)
			}
			if got.Type != tc.want.Type || got.Team != tc.want.Team || got.Channel != tc.want.Channel ||
				got.ChannelID != tc.want.ChannelID || got.User != tc.want.User ||
				len(got.Users) != len(tc.want.Users) {
				t.Errorf("destinationFromFlags() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestRuleLabel(t *testing.T) {
	if got := ruleLabel(config.Rule{}); got != "(unnamed)" {
		t.Errorf("ruleLabel(unnamed) = %q, want %q", got, "(unnamed)")
	}
	if got := ruleLabel(config.Rule{Name: "family"}); got != "family" {
		t.Errorf("ruleLabel(named) = %q, want %q", got, "family")
	}
}

func TestRuleCriteria(t *testing.T) {
	if got := ruleCriteria(config.Rule{}); got != "(no criteria)" {
		t.Errorf("ruleCriteria(empty) = %q, want %q", got, "(no criteria)")
	}

	rule := config.Rule{
		Phones:      []string{"+15145551212"},
		GroupsOnly:  true,
		NamePattern: "^Acme",
	}
	got := ruleCriteria(rule)
	for _, want := range []string{"phones=+15145551212", "groups_only", `name_pattern="^Acme"`} {
		if !strings.Contains(got, want) {
			t.Errorf("ruleCriteria() = %q, want it to contain %q", got, want)
		}
	}
}
