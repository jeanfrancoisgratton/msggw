// rcs_gateway
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 20:11:42
// Original filename: src/internal/bridge/routing_test.go

package bridge

import (
	"reflect"
	"testing"

	"rcs_gateway/internal/config"
	"rcs_gateway/internal/gmessages"
)

var defaultDest = config.Destination{Type: config.DestChannel, Team: "team", Channel: "messages"}

// sameDest compares destinations. Destination holds a slice, so it is not
// comparable with ==.
func sameDest(a, b config.Destination) bool { return reflect.DeepEqual(a, b) }

func directConversation(id, name string, phones ...string) gmessages.Conversation {
	conv := gmessages.Conversation{ID: id, Name: name}
	for i, phone := range phones {
		conv.Participants = append(conv.Participants, gmessages.Participant{
			ID:    id + "-p" + string(rune('a'+i)),
			Phone: phone,
		})
	}
	// Every conversation also has us in it; a rule must not match on our own
	// number.
	conv.Participants = append(conv.Participants, gmessages.Participant{
		ID: id + "-me", Phone: "+15140000000", IsMe: true,
	})
	return conv
}

func TestRouteFallsBackToDefault(t *testing.T) {
	router, err := NewRouter(config.RoutingConfig{Default: defaultDest})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	dest, rule := router.Route(directConversation("c1", "Alice", "+15145551212"))
	if !sameDest(dest, defaultDest) {
		t.Errorf("Route() = %v, want the default destination", dest)
	}
	if rule != "default" {
		t.Errorf("rule = %q, want %q", rule, "default")
	}
}

// TestRouteMatchesPhoneRegardlessOfFormatting is the case that decides whether
// routing works at all in practice: nobody writes the same number twice the
// same way.
func TestRouteMatchesPhoneRegardlessOfFormatting(t *testing.T) {
	dmDest := config.Destination{Type: config.DestDirect, User: "jfgratton"}
	router, err := NewRouter(config.RoutingConfig{
		Default: defaultDest,
		Rules: []config.Rule{{
			Name:        "family",
			Phones:      []string{"+1 (514) 555-1212"},
			Destination: dmDest,
		}},
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	for _, phone := range []string{"+15145551212", "+1 514 555 1212", "+1-514-555.1212"} {
		dest, rule := router.Route(directConversation("c1", "Alice", phone))
		if !sameDest(dest, dmDest) {
			t.Errorf("phone %q routed to %v, want the DM destination", phone, dest)
		}
		if rule != "family" {
			t.Errorf("phone %q matched rule %q, want %q", phone, rule, "family")
		}
	}
}

func TestRouteIgnoresOurOwnNumber(t *testing.T) {
	router, err := NewRouter(config.RoutingConfig{
		Default: defaultDest,
		Rules: []config.Rule{{
			Name:        "self",
			Phones:      []string{"+15140000000"},
			Destination: config.Destination{Type: config.DestDirect, User: "jfgratton"},
		}},
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	_, rule := router.Route(directConversation("c1", "Alice", "+15145551212"))
	if rule != "default" {
		t.Errorf("a rule matched on our own number: rule = %q", rule)
	}
}

func TestRouteGroupsOnly(t *testing.T) {
	groupDest := config.Destination{Type: config.DestChannel, Team: "team", Channel: "groups"}
	router, err := NewRouter(config.RoutingConfig{
		Default: defaultDest,
		Rules: []config.Rule{{
			Name:        "groups",
			GroupsOnly:  true,
			Destination: groupDest,
		}},
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	group := directConversation("g1", "Book club", "+15145551212", "+15145551213")
	group.IsGroup = true

	if dest, _ := router.Route(group); !sameDest(dest, groupDest) {
		t.Errorf("group conversation routed to %v, want the group destination", dest)
	}
	if dest, _ := router.Route(directConversation("c1", "Alice", "+15145551212")); !sameDest(dest, defaultDest) {
		t.Errorf("one-to-one conversation routed to %v, want the default", dest)
	}
}

// TestRouteShapeFilterNarrowsIdentityCriteria checks that a rule combining a
// shape filter with an identity criterion needs both, rather than either.
func TestRouteShapeFilterNarrowsIdentityCriteria(t *testing.T) {
	target := config.Destination{Type: config.DestChannel, Team: "team", Channel: "special"}
	router, err := NewRouter(config.RoutingConfig{
		Default: defaultDest,
		Rules: []config.Rule{{
			Name:        "group with Alice",
			GroupsOnly:  true,
			Phones:      []string{"+15145551212"},
			Destination: target,
		}},
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	// Right number, wrong shape.
	if dest, _ := router.Route(directConversation("c1", "Alice", "+15145551212")); !sameDest(dest, defaultDest) {
		t.Errorf("a one-to-one conversation matched a groups-only rule: %v", dest)
	}

	group := directConversation("g1", "Book club", "+15145551212")
	group.IsGroup = true
	if dest, _ := router.Route(group); !sameDest(dest, target) {
		t.Errorf("a group with the right number routed to %v, want %v", dest, target)
	}
}

func TestRouteFirstMatchingRuleWins(t *testing.T) {
	first := config.Destination{Type: config.DestDirect, User: "first"}
	second := config.Destination{Type: config.DestDirect, User: "second"}

	router, err := NewRouter(config.RoutingConfig{
		Default: defaultDest,
		Rules: []config.Rule{
			{Name: "first", Phones: []string{"+15145551212"}, Destination: first},
			{Name: "second", NamePattern: "Alice", Destination: second},
		},
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	dest, rule := router.Route(directConversation("c1", "Alice", "+15145551212"))
	if !sameDest(dest, first) || rule != "first" {
		t.Errorf("Route() = %v (%s), want the first rule to win", dest, rule)
	}
}

func TestRouteNamePatternMatchesTitle(t *testing.T) {
	target := config.Destination{Type: config.DestChannel, Team: "team", Channel: "work"}
	router, err := NewRouter(config.RoutingConfig{
		Default: defaultDest,
		Rules: []config.Rule{{
			Name:        "work",
			NamePattern: "(?i)^acme ",
			Destination: target,
		}},
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	if dest, _ := router.Route(directConversation("c1", "ACME Support", "+15145551212")); !sameDest(dest, target) {
		t.Errorf("a matching name routed to %v, want %v", dest, target)
	}
	if dest, _ := router.Route(directConversation("c2", "Alice", "+15145551213")); !sameDest(dest, defaultDest) {
		t.Errorf("a non-matching name routed to %v, want the default", dest)
	}
}

// TestRouteFallsBackToNumbersWhenUnnamed covers the conversation with no
// contact name, where the title is built from the participants' numbers.
func TestRouteFallsBackToNumbersWhenUnnamed(t *testing.T) {
	target := config.Destination{Type: config.DestDirect, User: "jfgratton"}
	router, err := NewRouter(config.RoutingConfig{
		Default: defaultDest,
		Rules: []config.Rule{{
			Name:        "unnamed",
			NamePattern: `^\+1 514`,
			Destination: target,
		}},
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	if dest, _ := router.Route(directConversation("c1", "", "+1 514 555-1212")); !sameDest(dest, target) {
		t.Errorf("an unnamed conversation routed to %v, want %v", dest, target)
	}
}

func TestNewRouterRejectsABadPattern(t *testing.T) {
	_, err := NewRouter(config.RoutingConfig{
		Default: defaultDest,
		Rules: []config.Rule{{
			Name:        "broken",
			NamePattern: "([",
			Destination: defaultDest,
		}},
	})
	if err == nil {
		t.Fatal("an invalid regular expression was accepted")
	}
}

func TestNewRouterRejectsAPhoneWithNoDigits(t *testing.T) {
	_, err := NewRouter(config.RoutingConfig{
		Default: defaultDest,
		Rules: []config.Rule{{
			Name:        "broken",
			Phones:      []string{"not a number"},
			Destination: defaultDest,
		}},
	})
	if err == nil {
		t.Fatal("a phone entry with no digits was accepted; it could never match")
	}
}
