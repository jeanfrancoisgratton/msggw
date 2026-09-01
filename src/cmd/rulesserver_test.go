// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>

package cmd

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"msggw/internal/config"
	"msggw/internal/rulesproto"
)

// writeTestConfig writes a minimal, valid config.json (one user, remote
// rules management enabled) and returns the loaded *config.Config, path-set
// as config.Mutate/Load expect.
func writeTestConfig(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{
	  "root_dir": "` + dir + `",
	  "mattermost": {"url": "https://mm.example.net", "token_ref": "env:T"},
	  "users": [
	    {"name": "hastoken",
	     "remote_rules": {"token_ref": "literal:s3cret"},
	     "routing": {
	       "default_direct": {"type": "direct", "user": "alice"},
	       "thread_per_conversation": false,
	       "post_delivery_status": true,
	       "join_channels": true,
	       "rules": [{"name": "r1", "phones": ["+1"], "destination": {"type": "direct", "user": "bob"}}]
	     }},
	    {"name": "notoken", "routing": {"default_direct": {"type": "direct", "user": "carol"}}}
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

// testRulesServer builds a rulesServer backed by a real config.json (so
// handlePut's config.Mutate calls have a real file to write to), and a
// reloadRequests channel drained by a goroutine that always reports success
// unless onReload is given to customize the response.
func testRulesServer(t *testing.T, cfg *config.Config, onReload func(reloadRequest)) (*rulesServer, *atomic.Pointer[config.Config]) {
	t.Helper()
	sharedCfg := new(atomic.Pointer[config.Config])
	sharedCfg.Store(cfg)

	reloadRequests := make(chan reloadRequest)
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go func() {
		for {
			select {
			case req := <-reloadRequests:
				if onReload != nil {
					onReload(req)
				} else {
					req.respond(nil)
				}
			case <-done:
				return
			}
		}
	}()

	return newRulesServer(sharedCfg, slog.New(slog.NewTextHandler(discard{}, nil)), reloadRequests), sharedCfg
}

func TestRulesServerUnknownUser(t *testing.T) {
	s, _ := testRulesServer(t, writeTestConfig(t), nil)
	mux := http.NewServeMux()
	s.mount(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, newTestRequest(t, "GET", "/rules/ghost", "s3cret", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestRulesServerRemoteRulesDisabled(t *testing.T) {
	s, _ := testRulesServer(t, writeTestConfig(t), nil)
	mux := http.NewServeMux()
	s.mount(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, newTestRequest(t, "GET", "/rules/notoken", "anything", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestRulesServerWrongToken(t *testing.T) {
	s, _ := testRulesServer(t, writeTestConfig(t), nil)
	mux := http.NewServeMux()
	s.mount(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, newTestRequest(t, "GET", "/rules/hastoken", "wrong", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// TestRulesServerGetReturnsCurrentRules covers the fetch-before-edit
// requirement: a client must be able to pull exactly the current
// default_direct/default_group/rules before pushing a replacement.
func TestRulesServerGetReturnsCurrentRules(t *testing.T) {
	s, _ := testRulesServer(t, writeTestConfig(t), nil)
	mux := http.NewServeMux()
	s.mount(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, newTestRequest(t, "GET", "/rules/hastoken", "s3cret", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var doc rulesproto.RulesDocument
	if err := json.NewDecoder(rec.Body).Decode(&doc); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(doc.Rules) != 1 || doc.Rules[0].Name != "r1" {
		t.Errorf("Rules = %+v, want the one rule from the test config", doc.Rules)
	}
	if doc.DefaultDirect.User != "alice" {
		t.Errorf("DefaultDirect.User = %q, want %q", doc.DefaultDirect.User, "alice")
	}
}

// TestRulesServerPutRejectsInvalidDocument covers that an obviously invalid
// document (a rule with no criteria) is rejected before it ever touches
// config.json, using the same config.ValidateRuleList the daemon's own Load
// path uses.
func TestRulesServerPutRejectsInvalidDocument(t *testing.T) {
	cfg := writeTestConfig(t)
	s, _ := testRulesServer(t, cfg, nil)
	mux := http.NewServeMux()
	s.mount(mux)

	update := rulesproto.RulesUpdate{
		Rules: []config.Rule{{Name: "empty", Destination: config.Destination{Type: config.DestDirect, User: "bob"}}},
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, newTestRequest(t, "PUT", "/rules/hastoken", "s3cret", update))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusUnprocessableEntity, rec.Body.String())
	}

	// config.json itself must be untouched by a rejected push.
	onDisk, err := config.Load(cfg.Path())
	if err != nil {
		t.Fatalf("re-loading config.json: %v", err)
	}
	if len(onDisk.Users[0].Routing.Rules) != 1 {
		t.Errorf("config.json was modified by a rejected push: rules = %+v", onDisk.Users[0].Routing.Rules)
	}
}

// TestRulesServerPutReplacesOnlyRules is the core full-replace contract:
// routing.rules is overwritten wholesale, but default_direct/default_group
// (operator-set — see rulesproto.RulesUpdate), every other field of that
// user's routing.* and the other user entirely are left exactly as they
// were. A push cannot even express a default_direct/default_group change:
// rulesproto.RulesUpdate has no such fields, so this also proves the type
// itself enforces the restriction, not just handlePut's behavior.
func TestRulesServerPutReplacesOnlyRules(t *testing.T) {
	cfg := writeTestConfig(t)
	s, _ := testRulesServer(t, cfg, nil)
	mux := http.NewServeMux()
	s.mount(mux)

	update := rulesproto.RulesUpdate{
		Rules: []config.Rule{
			{Name: "new-rule", Phones: []string{"+15145551212"}, Destination: config.Destination{Type: config.DestDirect, User: "someone"}},
		},
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, newTestRequest(t, "PUT", "/rules/hastoken", "s3cret", update))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var respDoc rulesproto.RulesDocument
	if err := json.NewDecoder(rec.Body).Decode(&respDoc); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if respDoc.DefaultDirect.User != "alice" {
		t.Errorf("response DefaultDirect.User = %q, want the unchanged %q", respDoc.DefaultDirect.User, "alice")
	}

	onDisk, err := config.Load(cfg.Path())
	if err != nil {
		t.Fatalf("re-loading config.json: %v", err)
	}
	u := onDisk.Users[0]
	if u.Routing.DefaultDirect.User != "alice" {
		t.Errorf("DefaultDirect.User = %q, want it unchanged (%q) — a push must never be able to change the fallback", u.Routing.DefaultDirect.User, "alice")
	}
	if len(u.Routing.Rules) != 1 || u.Routing.Rules[0].Name != "new-rule" {
		t.Errorf("Rules = %+v, want only the pushed rule", u.Routing.Rules)
	}
	// Operator-level layout preferences must survive a rules push untouched.
	if u.Routing.ThreadPerConversationEnabled() {
		t.Error("thread_per_conversation was changed by a rules push, which only touches routing rules")
	}
	if !u.Routing.PostDeliveryStatus {
		t.Error("post_delivery_status was reset by a rules push")
	}
	if !u.Routing.JoinChannels {
		t.Error("join_channels was reset by a rules push")
	}
	// The other user must be completely unaffected.
	if onDisk.Users[1].Routing.DefaultDirect.User != "carol" {
		t.Errorf("an unrelated user's routing was modified: %+v", onDisk.Users[1].Routing)
	}
}

// TestRulesServerPutReportsReloadFailure covers the "saved but not applied"
// distinction: config.Mutate already committed the change, so a reload
// failure must not be reported the same way as an outright rejection.
func TestRulesServerPutReportsReloadFailure(t *testing.T) {
	cfg := writeTestConfig(t)
	s, _ := testRulesServer(t, cfg, func(req reloadRequest) {
		req.respond(errors.New("mattermost is unreachable"))
	})
	mux := http.NewServeMux()
	s.mount(mux)

	doc := rulesproto.RulesDocument{
		DefaultDirect: config.Destination{Type: config.DestDirect, User: "alice"},
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, newTestRequest(t, "PUT", "/rules/hastoken", "s3cret", doc))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}

	var apiErr rulesproto.ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&apiErr); err != nil {
		t.Fatalf("decoding error body: %v", err)
	}
	if apiErr.Applied {
		t.Error("Applied = true on a reload failure, want false")
	}

	// The rules were still saved to config.json even though the daemon
	// could not switch to them live.
	onDisk, err := config.Load(cfg.Path())
	if err != nil {
		t.Fatalf("re-loading config.json: %v", err)
	}
	if onDisk.Users[0].Routing.DefaultDirect.User != "alice" {
		t.Error("the pushed rules were not saved despite the reload failure")
	}
}

// TestRulesServerConcurrentPushesForDifferentUsersBothSucceed proves the
// in-process mutex around config.Mutate serializes concurrent pushes rather
// than letting them race Mutate's optimistic read-before-write check.
func TestRulesServerConcurrentPushesForDifferentUsersBothSucceed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{
	  "root_dir": "` + dir + `",
	  "mattermost": {"url": "https://mm.example.net", "token_ref": "env:T"},
	  "users": [
	    {"name": "u1", "remote_rules": {"token_ref": "literal:t1"},
	     "routing": {"default_direct": {"type": "direct", "user": "a"}}},
	    {"name": "u2", "remote_rules": {"token_ref": "literal:t2"},
	     "routing": {"default_direct": {"type": "direct", "user": "b"}}}
	  ]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing test config: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("loading test config: %v", err)
	}

	s, _ := testRulesServer(t, cfg, nil)
	mux := http.NewServeMux()
	s.mount(mux)

	results := make(chan int, 2)
	push := func(name, token, ruleName string) {
		update := rulesproto.RulesUpdate{
			Rules: []config.Rule{{Name: ruleName, Phones: []string{"+1"}, Destination: config.Destination{Type: config.DestDirect, User: "x"}}},
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, newTestRequest(t, "PUT", "/rules/"+name, token, update))
		results <- rec.Code
	}
	go push("u1", "t1", "rule-for-u1")
	go push("u2", "t2", "rule-for-u2")

	for i := 0; i < 2; i++ {
		if code := <-results; code != http.StatusOK {
			t.Errorf("concurrent push %d: status = %d, want %d", i, code, http.StatusOK)
		}
	}

	onDisk, err := config.Load(cfg.Path())
	if err != nil {
		t.Fatalf("re-loading config.json: %v", err)
	}
	if len(onDisk.Users[0].Routing.Rules) != 1 || onDisk.Users[0].Routing.Rules[0].Name != "rule-for-u1" {
		t.Errorf("u1's concurrent push was lost: rules = %+v", onDisk.Users[0].Routing.Rules)
	}
	if len(onDisk.Users[1].Routing.Rules) != 1 || onDisk.Users[1].Routing.Rules[0].Name != "rule-for-u2" {
		t.Errorf("u2's concurrent push was lost: rules = %+v", onDisk.Users[1].Routing.Rules)
	}
}
