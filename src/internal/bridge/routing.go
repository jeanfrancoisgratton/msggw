// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 20:05:15
// Original filename: src/internal/bridge/routing.go

package bridge

import (
	"fmt"
	"regexp"

	"msggw/internal/config"
	"msggw/internal/gmessages"
	"msggw/internal/storage"
)

// Router decides where in Mattermost a Google Messages conversation belongs.
//
// The decision is entirely configuration-driven. Whether everything lands in
// one channel, whether family goes to a direct message and everyone else to a
// team channel, whether group chats get their own channel — all of that is a
// routing rule, not a code change.
type Router struct {
	cfg   config.RoutingConfig
	rules []compiledRule
}

// compiledRule is a config rule with its matching work done up front, so that
// a regular expression is not recompiled for every message.
type compiledRule struct {
	rule config.Rule
	name string

	conversationIDs map[string]struct{}
	phones          map[string]struct{}
	namePattern     *regexp.Regexp
}

// NewRouter compiles the routing rules. It fails on a rule that cannot be
// compiled rather than silently ignoring it, since a rule that never matches
// sends private messages somewhere the operator did not intend.
func NewRouter(cfg config.RoutingConfig) (*Router, error) {
	r := &Router{cfg: cfg}

	for i, rule := range cfg.Rules {
		name := rule.Name
		if name == "" {
			name = fmt.Sprintf("rule %d", i+1)
		}

		compiled := compiledRule{rule: rule, name: name}

		if len(rule.ConversationIDs) > 0 {
			compiled.conversationIDs = make(map[string]struct{}, len(rule.ConversationIDs))
			for _, id := range rule.ConversationIDs {
				compiled.conversationIDs[id] = struct{}{}
			}
		}
		if len(rule.Phones) > 0 {
			compiled.phones = make(map[string]struct{}, len(rule.Phones))
			for _, phone := range rule.Phones {
				normalized := storage.NormalizePhone(phone)
				if normalized == "" {
					return nil, fmt.Errorf("routing %s: %q contains no digits", name, phone)
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
func (r *Router) Route(conv gmessages.Conversation) (config.Destination, string) {
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
// The shape filters (groups_only, directs_only) are conditions that must hold;
// the identity criteria (IDs, phones, name) are alternatives, any one of which
// is enough. A rule with only a shape filter matches every conversation of
// that shape, which is what "group chats get their own channel" needs.
func (c compiledRule) matches(conv gmessages.Conversation) bool {
	if c.rule.GroupsOnly && !conv.IsGroup {
		return false
	}
	if c.rule.DirectsOnly && conv.IsGroup {
		return false
	}

	hasIdentityCriteria := c.conversationIDs != nil || c.phones != nil || c.namePattern != nil
	if !hasIdentityCriteria {
		// Only a shape filter, and it held.
		return c.rule.GroupsOnly || c.rule.DirectsOnly
	}

	if c.conversationIDs != nil {
		if _, ok := c.conversationIDs[conv.ID]; ok {
			return true
		}
	}
	if c.phones != nil {
		for _, participant := range conv.Participants {
			if participant.IsMe {
				continue
			}
			if _, ok := c.phones[storage.NormalizePhone(participant.Phone)]; ok {
				return true
			}
		}
	}
	if c.namePattern != nil && c.namePattern.MatchString(conv.Title()) {
		return true
	}

	return false
}
