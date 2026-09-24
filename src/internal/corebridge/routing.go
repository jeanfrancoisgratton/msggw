// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/internal/corebridge/routing.go

package corebridge

import (
	"fmt"
	"regexp"

	"msggw/internal/config"
	"msggw/internal/storage"
	"msggw/internal/transport"
)

// GenericRule is the router's own criteria vocabulary, independent of
// config.Rule/config.DiscordRule's JSON field names — each concrete config
// type is adapted to a []GenericRule (see RulesFromDiscord/RulesFromGMessages)
// before NewRouter ever sees it, so this file is the only place a rule's
// matching logic lives, even though config.Rule and config.DiscordRule stay
// separate, hand-written types (see docs/discord-adapter-design.md and
// docs/msggw-rcs-mattermost-discord.md for why the config types themselves
// are not unified: config.Rule has a whole remote-push CLI/HTTP surface
// Discord has no equivalent of).
type GenericRule struct {
	Name        string
	Destination config.Destination
	GroupsOnly  bool
	DirectsOnly bool
	// IDs matches transport.Conversation.ID exactly (gmessages:
	// ConversationIDs; Discord: ChannelIDs).
	IDs []string
	// SecondaryIDs matches transport.Conversation.GuildID (Discord only;
	// always empty for gmessages, which has no such identifier).
	SecondaryIDs []string
	// Phones matches a participant's phone number, punctuation-insensitive
	// (gmessages only; always empty for Discord, whose participants have no
	// phone number).
	Phones []string
	// NamePattern is a regular expression matched against
	// transport.Conversation.Title().
	NamePattern string
}

// BridgeConfig is what Router/Bridge are built from, translated from either
// config.RoutingConfig (gmessages) or config.DiscordRoutingConfig (Discord)
// by a small helper in cmd/ — see bridgeConfigFromDiscord/
// bridgeConfigFromGMessages.
type BridgeConfig struct {
	DefaultDirect         config.Destination
	DefaultGroup          config.Destination
	Rules                 []GenericRule
	ThreadPerConversation bool
	JoinChannels          bool
	// PostDeliveryStatus is ignored by a transport that does not implement
	// transport.StatusReporter (Discord, in full).
	PostDeliveryStatus bool
}

// Router decides where in Mattermost a conversation belongs. It replaces
// what were two hand-duplicated, structurally identical implementations
// (internal/bridge.Router for gmessages.Conversation, internal/discordbridge.Router
// for discord.Conversation) with one, operating on transport.Conversation.
type Router struct {
	cfg   BridgeConfig
	rules []compiledRule
}

// compiledRule is a GenericRule with its matching work done up front, so a
// regular expression is not recompiled for every message.
type compiledRule struct {
	rule GenericRule
	name string

	ids          map[string]struct{}
	secondaryIDs map[string]struct{}
	phones       map[string]struct{}
	namePattern  *regexp.Regexp
}

// NewRouter compiles the routing rules. It fails on a rule that cannot be
// compiled rather than silently ignoring it, since a rule that never matches
// sends messages somewhere the operator did not intend.
func NewRouter(cfg BridgeConfig) (*Router, error) {
	r := &Router{cfg: cfg}

	for i, rule := range cfg.Rules {
		name := rule.Name
		if name == "" {
			name = fmt.Sprintf("rule %d", i+1)
		}

		compiled := compiledRule{rule: rule, name: name}

		if len(rule.IDs) > 0 {
			compiled.ids = make(map[string]struct{}, len(rule.IDs))
			for _, id := range rule.IDs {
				compiled.ids[id] = struct{}{}
			}
		}
		if len(rule.SecondaryIDs) > 0 {
			compiled.secondaryIDs = make(map[string]struct{}, len(rule.SecondaryIDs))
			for _, id := range rule.SecondaryIDs {
				compiled.secondaryIDs[id] = struct{}{}
			}
		}
		if len(rule.Phones) > 0 {
			compiled.phones = make(map[string]struct{}, len(rule.Phones))
			for _, phone := range rule.Phones {
				normalized := storage.NormalizePhone(phone)
				if normalized == "" {
					return nil, fmt.Errorf("routing %s: phone %q contains no digits", name, phone)
				}
				compiled.phones[normalized] = struct{}{}
			}
		}
		if rule.NamePattern != "" {
			pattern, err := regexp.Compile(rule.NamePattern)
			if err != nil {
				return nil, fmt.Errorf("routing %s: name_pattern: %w", name, err)
			}
			compiled.namePattern = pattern
		}

		r.rules = append(r.rules, compiled)
	}

	return r, nil
}

// Route returns the destination for a conversation and the name of the rule
// that chose it, which is "default (direct)" or "default (group)" when no
// rule matched.
func (r *Router) Route(conv transport.Conversation) (config.Destination, string) {
	for _, rule := range r.rules {
		if rule.matches(conv) {
			return rule.rule.Destination, rule.name
		}
	}
	if conv.IsGroup && r.cfg.DefaultGroup.Type != "" {
		return r.cfg.DefaultGroup, "default (group)"
	}
	return r.cfg.DefaultDirect, "default (direct)"
}

// matches reports whether a conversation satisfies a rule.
//
// The shape filters (groups_only, directs_only) are conditions that must
// hold; the identity criteria (IDs, secondary IDs, phones, name pattern) are
// alternatives, any one of which is enough. A rule with only a shape filter
// matches every conversation of that shape.
func (c compiledRule) matches(conv transport.Conversation) bool {
	if c.rule.GroupsOnly && !conv.IsGroup {
		return false
	}
	if c.rule.DirectsOnly && conv.IsGroup {
		return false
	}

	hasIdentityCriteria := c.ids != nil || c.secondaryIDs != nil || c.phones != nil || c.namePattern != nil
	if !hasIdentityCriteria {
		// Only a shape filter, and it held.
		return c.rule.GroupsOnly || c.rule.DirectsOnly
	}

	if c.ids != nil {
		if _, ok := c.ids[conv.ID]; ok {
			return true
		}
	}
	if c.secondaryIDs != nil {
		if _, ok := c.secondaryIDs[conv.GuildID]; ok {
			return true
		}
	}
	if c.phones != nil {
		for _, p := range conv.OtherParticipants() {
			if _, ok := c.phones[storage.NormalizePhone(p.Phone)]; ok {
				return true
			}
		}
	}
	if c.namePattern != nil && c.namePattern.MatchString(conv.Title()) {
		return true
	}

	return false
}

// RulesFromDiscord adapts config.DiscordRule's field names to GenericRule's.
// It is the only place a Discord rule's field names are known outside
// package config.
func RulesFromDiscord(rules []config.DiscordRule) []GenericRule {
	out := make([]GenericRule, len(rules))
	for i, r := range rules {
		out[i] = GenericRule{
			Name:         r.Name,
			Destination:  r.Destination,
			GroupsOnly:   r.GroupsOnly,
			DirectsOnly:  r.DirectsOnly,
			IDs:          r.ChannelIDs,
			SecondaryIDs: r.GuildIDs,
			NamePattern:  r.ChannelNamePattern,
		}
	}
	return out
}
