// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>

// Package rulesproto is the wire format shared between the daemon's
// self-service rules-management HTTP endpoint (wired up in
// cmd/rulesserver.go) and msg-gw's own "rules pull/push --remote" client
// (internal/rulesclient) — see docs/RUNNING.md, "Remote rules management".
package rulesproto

import "msggw/internal/config"

// RulesDocument is what GET /rules/{name} returns: a tenant's full routing
// picture, for context while editing. DefaultDirect/DefaultGroup are shown
// read-only — see RulesUpdate for why a push cannot change them. Every other
// field of config.RoutingConfig (thread_per_conversation,
// post_delivery_status, join_channels) is an operator-level layout
// preference and is not part of this document at all, whether reading or
// writing.
type RulesDocument struct {
	// DefaultDirect is used for a one-to-one conversation that no rule
	// matches. Read-only: see RulesUpdate.
	DefaultDirect config.Destination `json:"default_direct"`
	// DefaultGroup is used for a group conversation that no rule matches.
	// Left unset, it falls back to DefaultDirect. Read-only: see
	// RulesUpdate.
	DefaultGroup config.Destination `json:"default_group,omitempty"`
	// Rules are evaluated in order; the first match wins.
	Rules []config.Rule `json:"rules,omitempty"`
}

// RulesUpdate is the body of PUT /rules/{name}: a full replacement of that
// tenant's routing.rules, and *only* routing.rules. default_direct and
// default_group are deliberately not fields here, not merely omitted —
// they are operator-set and cannot be changed by a rules push at all, so
// that the fallback destination guaranteeing message delivery when nothing
// in Rules matches can never be dropped or mistargeted by a user's own (or
// a generated) push. Fetch RulesDocument first (GET) to see the current
// fallback and rules before replacing the rules.
type RulesUpdate struct {
	Rules []config.Rule `json:"rules,omitempty"`
}

// ErrorResponse is the body of any non-200 response.
type ErrorResponse struct {
	Error string `json:"error"`
	// Applied is only meaningful on a reload failure: false means the rules
	// were still saved to config.json (they will take effect on the next
	// successful reload) even though the running daemon could not switch to
	// them just now — this is not a rejection of the submitted rules.
	Applied bool `json:"applied"`
}
