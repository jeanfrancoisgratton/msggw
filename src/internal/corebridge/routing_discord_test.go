// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/internal/corebridge/routing_discord_test.go

package corebridge

import (
	"reflect"
	"testing"

	"msggw/internal/config"
	"msggw/internal/transport"
)

var discordDefaultDest = config.Destination{Type: config.DestChannel, Team: "team", Channel: "messages"}

// sameDest compares destinations. Destination holds a slice, so it is not
// comparable with ==.
func sameDest(a, b config.Destination) bool { return reflect.DeepEqual(a, b) }

func discordBridgeConfig(cfg config.DiscordRoutingConfig) BridgeConfig {
	return BridgeConfig{
		DefaultDirect:         cfg.DefaultDirect,
		DefaultGroup:          cfg.DefaultGroup,
		Rules:                 RulesFromDiscord(cfg.Rules),
		ThreadPerConversation: cfg.ThreadPerConversationEnabled(),
		JoinChannels:          cfg.JoinChannels,
	}
}

func guildConversation(channelID, guildID, name string) transport.Conversation {
	return transport.Conversation{ID: channelID, GuildID: guildID, Name: name, IsGroup: true}
}

func dmConversation(channelID string, others ...string) transport.Conversation {
	conv := transport.Conversation{ID: channelID}
	for i, name := range others {
		conv.Participants = append(conv.Participants, transport.Participant{
			ID:          channelID + "-p" + string(rune('a'+i)),
			DisplayName: name,
		})
	}
	if len(others) > 1 {
		conv.IsGroup = true
	}
	return conv
}

func TestDiscordRouteFallsBackToDefault(t *testing.T) {
	router, err := NewRouter(discordBridgeConfig(config.DiscordRoutingConfig{DefaultDirect: discordDefaultDest}))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	dest, rule := router.Route(dmConversation("c1", "Alice"))
	if !sameDest(dest, discordDefaultDest) {
		t.Errorf("Route() = %v, want the default destination", dest)
	}
	if rule != "default (direct)" {
		t.Errorf("rule = %q, want %q", rule, "default (direct)")
	}
}

func TestDiscordRouteMatchesChannelID(t *testing.T) {
	dmDest := config.Destination{Type: config.DestDirect, User: "jfgratton"}
	router, err := NewRouter(discordBridgeConfig(config.DiscordRoutingConfig{
		DefaultDirect: discordDefaultDest,
		Rules: []config.DiscordRule{{
			Name:        "specific channel",
			ChannelIDs:  []string{"channel-123"},
			Destination: dmDest,
		}},
	}))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	dest, rule := router.Route(guildConversation("channel-123", "guild-1", "general"))
	if !sameDest(dest, dmDest) {
		t.Errorf("Route() = %v, want the matched destination", dest)
	}
	if rule != "specific channel" {
		t.Errorf("rule = %q, want %q", rule, "specific channel")
	}
}

func TestDiscordRouteMatchesGuildID(t *testing.T) {
	target := config.Destination{Type: config.DestChannel, Team: "team", Channel: "guild-traffic"}
	router, err := NewRouter(discordBridgeConfig(config.DiscordRoutingConfig{
		DefaultDirect: discordDefaultDest,
		Rules: []config.DiscordRule{{
			Name:        "one guild",
			GuildIDs:    []string{"guild-1"},
			Destination: target,
		}},
	}))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	if dest, _ := router.Route(guildConversation("c1", "guild-1", "general")); !sameDest(dest, target) {
		t.Errorf("Route() = %v, want %v", dest, target)
	}
	if dest, _ := router.Route(guildConversation("c2", "guild-2", "general")); !sameDest(dest, discordDefaultDest) {
		t.Errorf("a different guild matched the rule: %v", dest)
	}
}

func TestDiscordRouteDefaultGroupFallsBackToDefaultDirect(t *testing.T) {
	router, err := NewRouter(discordBridgeConfig(config.DiscordRoutingConfig{DefaultDirect: discordDefaultDest}))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	dest, rule := router.Route(guildConversation("g1", "guild-1", "general"))
	if !sameDest(dest, discordDefaultDest) {
		t.Errorf("Route() = %v, want DefaultDirect as the fallback", dest)
	}
	if rule != "default (direct)" {
		t.Errorf("rule = %q, want %q", rule, "default (direct)")
	}
}

func TestDiscordRouteUsesDefaultGroupWhenSet(t *testing.T) {
	groupDest := config.Destination{Type: config.DestChannel, Team: "team", Channel: "groups"}
	router, err := NewRouter(discordBridgeConfig(config.DiscordRoutingConfig{
		DefaultDirect: discordDefaultDest,
		DefaultGroup:  groupDest,
	}))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	dest, rule := router.Route(guildConversation("g1", "guild-1", "general"))
	if !sameDest(dest, groupDest) {
		t.Errorf("Route() = %v, want DefaultGroup", dest)
	}
	if rule != "default (group)" {
		t.Errorf("rule = %q, want %q", rule, "default (group)")
	}

	if dest, _ := router.Route(dmConversation("c1", "Alice")); !sameDest(dest, discordDefaultDest) {
		t.Errorf("a DM was routed to %v, want DefaultDirect", dest)
	}
}

func TestDiscordRouteGroupsOnly(t *testing.T) {
	groupDest := config.Destination{Type: config.DestChannel, Team: "team", Channel: "groups"}
	router, err := NewRouter(discordBridgeConfig(config.DiscordRoutingConfig{
		DefaultDirect: discordDefaultDest,
		Rules: []config.DiscordRule{{
			Name:        "groups",
			GroupsOnly:  true,
			Destination: groupDest,
		}},
	}))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	if dest, _ := router.Route(guildConversation("g1", "guild-1", "general")); !sameDest(dest, groupDest) {
		t.Errorf("guild channel routed to %v, want the group destination", dest)
	}
	if dest, _ := router.Route(dmConversation("c1", "Alice")); !sameDest(dest, discordDefaultDest) {
		t.Errorf("a DM routed to %v, want the default", dest)
	}
}

// TestDiscordRouteShapeFilterNarrowsIdentityCriteria checks that a rule
// combining a shape filter with an identity criterion needs both, rather
// than either.
func TestDiscordRouteShapeFilterNarrowsIdentityCriteria(t *testing.T) {
	target := config.Destination{Type: config.DestChannel, Team: "team", Channel: "special"}
	router, err := NewRouter(discordBridgeConfig(config.DiscordRoutingConfig{
		DefaultDirect: discordDefaultDest,
		Rules: []config.DiscordRule{{
			Name:        "guild-1 group",
			GroupsOnly:  true,
			GuildIDs:    []string{"guild-1"},
			Destination: target,
		}},
	}))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	// Right guild, wrong shape: a DM has no GuildID at all, so it can never
	// satisfy a guild_ids criterion regardless of the shape filter.
	if dest, _ := router.Route(dmConversation("c1", "Alice")); !sameDest(dest, discordDefaultDest) {
		t.Errorf("a DM matched a groups-only rule: %v", dest)
	}

	if dest, _ := router.Route(guildConversation("g1", "guild-1", "general")); !sameDest(dest, target) {
		t.Errorf("a guild-1 channel routed to %v, want %v", dest, target)
	}
	if dest, _ := router.Route(guildConversation("g2", "guild-2", "general")); !sameDest(dest, discordDefaultDest) {
		t.Errorf("a different guild's channel matched the rule: %v", dest)
	}
}

func TestDiscordRouteFirstMatchingRuleWins(t *testing.T) {
	first := config.Destination{Type: config.DestDirect, User: "first"}
	second := config.Destination{Type: config.DestDirect, User: "second"}

	router, err := NewRouter(discordBridgeConfig(config.DiscordRoutingConfig{
		DefaultDirect: discordDefaultDest,
		Rules: []config.DiscordRule{
			{Name: "first", ChannelIDs: []string{"c1"}, Destination: first},
			{Name: "second", ChannelNamePattern: "general", Destination: second},
		},
	}))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	dest, rule := router.Route(guildConversation("c1", "guild-1", "general"))
	if !sameDest(dest, first) || rule != "first" {
		t.Errorf("Route() = %v (%s), want the first rule to win", dest, rule)
	}
}

func TestDiscordRouteChannelNamePatternMatchesTitle(t *testing.T) {
	target := config.Destination{Type: config.DestChannel, Team: "team", Channel: "support"}
	router, err := NewRouter(discordBridgeConfig(config.DiscordRoutingConfig{
		DefaultDirect: discordDefaultDest,
		Rules: []config.DiscordRule{{
			Name:               "support",
			ChannelNamePattern: "(?i)^support",
			Destination:        target,
		}},
	}))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	if dest, _ := router.Route(guildConversation("c1", "guild-1", "support-tickets")); !sameDest(dest, target) {
		t.Errorf("a matching channel name routed to %v, want %v", dest, target)
	}
	if dest, _ := router.Route(guildConversation("c2", "guild-1", "general")); !sameDest(dest, discordDefaultDest) {
		t.Errorf("a non-matching channel name routed to %v, want the default", dest)
	}
}

func TestNewDiscordRouterRejectsABadPattern(t *testing.T) {
	_, err := NewRouter(discordBridgeConfig(config.DiscordRoutingConfig{
		DefaultDirect: discordDefaultDest,
		Rules: []config.DiscordRule{{
			Name:               "broken",
			ChannelNamePattern: "([",
			Destination:        discordDefaultDest,
		}},
	}))
	if err == nil {
		t.Fatal("an invalid regular expression was accepted")
	}
}
