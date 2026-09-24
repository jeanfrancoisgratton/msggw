// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/internal/discordbridge/routing.go

package discordbridge

import (
	"fmt"
	"regexp"

	"msggw/internal/config"
	"msggw/internal/discord"
)

// Router decides where in Mattermost a Discord channel belongs.
//
// This mirrors bridge.Router's structure exactly, but cannot literally be
// the same type: bridge.Router.Route is typed to gmessages.Conversation.
// See docs/discord-adapter-design.md.
type Router struct {
	cfg   config.DiscordRoutingConfig
	rules []compiledRule
}

// compiledRule is a config.DiscordRule with its matching work done up
// front, so that a regular expression is not recompiled for every message.
type compiledRule struct {
	rule config.DiscordRule
	name string

	guildIDs    map[string]struct{}
	channelIDs  map[string]struct{}
	namePattern *regexp.Regexp
}

// NewRouter compiles the routing rules. It fails on a rule that cannot be
// compiled rather than silently ignoring it, since a rule that never
// matches sends messages somewhere the operator did not intend.
func NewRouter(cfg config.DiscordRoutingConfig) (*Router, error) {
	r := &Router{cfg: cfg}

	for i, rule := range cfg.Rules {
		name := rule.Name
		if name == "" {
			name = fmt.Sprintf("rule %d", i+1)
		}

		compiled := compiledRule{rule: rule, name: name}

		if len(rule.GuildIDs) > 0 {
			compiled.guildIDs = make(map[string]struct{}, len(rule.GuildIDs))
			for _, id := range rule.GuildIDs {
				compiled.guildIDs[id] = struct{}{}
			}
		}
		if len(rule.ChannelIDs) > 0 {
			compiled.channelIDs = make(map[string]struct{}, len(rule.ChannelIDs))
			for _, id := range rule.ChannelIDs {
				compiled.channelIDs[id] = struct{}{}
			}
		}
		if rule.ChannelNamePattern != "" {
			pattern, err := regexp.Compile(rule.ChannelNamePattern)
			if err != nil {
				return nil, fmt.Errorf("routing %s: channel_name_pattern: %w", name, err)
			}
			compiled.namePattern = pattern
		}

		r.rules = append(r.rules, compiled)
	}

	return r, nil
}

// Route returns the destination for a channel and the name of the rule that
// chose it, which is "default (direct)" or "default (group)" when no rule
// matched.
func (r *Router) Route(conv discord.Conversation) (config.Destination, string) {
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

// matches reports whether a channel satisfies a rule.
//
// The shape filters (groups_only, directs_only) are conditions that must
// hold; the identity criteria (guild IDs, channel IDs, name pattern) are
// alternatives, any one of which is enough. A rule with only a shape filter
// matches every channel of that shape, mirroring bridge.compiledRule.matches.
func (c compiledRule) matches(conv discord.Conversation) bool {
	if c.rule.GroupsOnly && !conv.IsGroup {
		return false
	}
	if c.rule.DirectsOnly && conv.IsGroup {
		return false
	}

	hasIdentityCriteria := c.guildIDs != nil || c.channelIDs != nil || c.namePattern != nil
	if !hasIdentityCriteria {
		// Only a shape filter, and it held.
		return c.rule.GroupsOnly || c.rule.DirectsOnly
	}

	if c.guildIDs != nil {
		if _, ok := c.guildIDs[conv.GuildID]; ok {
			return true
		}
	}
	if c.channelIDs != nil {
		if _, ok := c.channelIDs[conv.ID]; ok {
			return true
		}
	}
	if c.namePattern != nil && c.namePattern.MatchString(conv.Title()) {
		return true
	}

	return false
}
