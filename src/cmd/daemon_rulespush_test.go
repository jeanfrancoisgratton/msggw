// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>

package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"msggw/internal/config"
	"msggw/internal/rulesproto"
)

// writeRulesPushTestConfig is writeReloadTestConfig plus a user configured
// for remote rules management, so a test can push to it over the listener.
func writeRulesPushTestConfig(t *testing.T, mmURL string, listenerPort int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{
	  "state_dir": "` + dir + `",
	  "root_dir": "` + dir + `",
	  "mattermost": {"url": "` + mmURL + `", "token_ref": "env:MSGGW_TEST_TOKEN"},
	  "listener": {"port": ` + strconv.Itoa(listenerPort) + `},
	  "users": [
	    {"name": "nouser",
	     "remote_rules": {"token_ref": "literal:rules-secret"},
	     "routing": {"default_direct": {"type": "channel", "team": "t", "channel": "c"}}}
	  ]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing test config: %v", err)
	}
	return path
}

func rulesPut(t *testing.T, baseURL, name, token string, update rulesproto.RulesUpdate) (*http.Response, rulesproto.RulesDocument, rulesproto.ErrorResponse) {
	t.Helper()
	payload, err := json.Marshal(update)
	if err != nil {
		t.Fatalf("encoding request: %v", err)
	}
	req, err := http.NewRequest(http.MethodPut, baseURL+"/rules/"+name, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /rules/%s: %v", name, err)
	}
	defer resp.Body.Close()

	var okBody rulesproto.RulesDocument
	var errBody rulesproto.ErrorResponse
	decoder := json.NewDecoder(resp.Body)
	if resp.StatusCode == http.StatusOK {
		_ = decoder.Decode(&okBody)
	} else {
		_ = decoder.Decode(&errBody)
	}
	return resp, okBody, errBody
}

// newFlakyMattermostServer answers GetMe like fakeMattermostServer, except
// its failOnCall'th request (1-indexed) fails with 503 — used to simulate a
// transient Mattermost outage landing on exactly one generation's connect
// attempt while its neighbors succeed.
func newFlakyMattermostServer(t *testing.T, failOnCall int32) *httptest.Server {
	t.Helper()
	var calls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/users/me", func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == failOnCall {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "bot-id", "username": "msggw-bot", "is_bot": true})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestDaemonRulesPushAppliesAndReloads drives a real daemon (real listener,
// real generation loop) through a full remote rules push: the very request
// that triggers the reload is served by the listener belonging to the
// generation that reload tears down — this is exactly the scenario the
// persistent-listener design (see daemon.go, startPersistentListener) exists
// to make safe, so it must be exercised end to end, not just unit-tested in
// pieces.
func TestDaemonRulesPushAppliesAndReloads(t *testing.T) {
	t.Setenv("MSGGW_TEST_TOKEN", "test-token")
	mm := fakeMattermostServer(t)
	port := freeTCPPort(t)
	cfgPath := writeRulesPushTestConfig(t, mm.URL, port)

	oldConfigPath := configPath
	configPath = cfgPath
	defer func() { configPath = oldConfigPath }()

	done := make(chan error, 1)
	go func() {
		done <- daemonCmd.RunE(daemonCmd, nil)
	}()

	pidPath := filepath.Join(filepath.Dir(cfgPath), "msggw.pid")
	waitForFile(t, pidPath, 5*time.Second)
	waitForListener(t, port, 5*time.Second)

	baseURL := "http://127.0.0.1:" + strconv.Itoa(port)
	update := rulesproto.RulesUpdate{
		Rules: []config.Rule{
			{Name: "pushed", Phones: []string{"+15145551212"}, Destination: config.Destination{Type: config.DestChannel, Team: "t", Channel: "c2"}},
		},
	}

	resp, okBody, errBody := rulesPut(t, baseURL, "nouser", "rules-secret", update)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /rules/nouser: status = %d, error = %+v", resp.StatusCode, errBody)
	}
	if len(okBody.Rules) != 1 || okBody.Rules[0].Name != "pushed" {
		t.Errorf("response Rules = %+v, want the pushed rule echoed back", okBody.Rules)
	}

	onDisk, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("re-loading config.json: %v", err)
	}
	if len(onDisk.Users[0].Routing.Rules) != 1 || onDisk.Users[0].Routing.Rules[0].Name != "pushed" {
		t.Errorf("config.json was not updated by the push: rules = %+v", onDisk.Users[0].Routing.Rules)
	}

	// The persistent listener must still answer after the reload the push
	// triggered — proof it survived its own generation being torn down.
	waitForListener(t, port, 5*time.Second)

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("sending SIGTERM: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("daemonCmd.RunE returned an error on shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not shut down within 10s of SIGTERM")
	}
}

// TestDaemonRulesPushRollsBackOnTransientFailure covers the rollback path: a
// pushed document that is itself perfectly valid can still fail to go live
// if the new generation's Mattermost reconnect hits a transient outage. The
// daemon must revert to the previous, working generation rather than dying,
// and the pushed rules must still be reported as saved-but-not-applied
// rather than rejected outright.
func TestDaemonRulesPushRollsBackOnTransientFailure(t *testing.T) {
	t.Setenv("MSGGW_TEST_TOKEN", "test-token")
	// Call 1: initial startup's Connect — succeeds.
	// Call 2: the pushed reload's new-generation Connect — fails.
	// Call 3: the rollback generation's Connect — succeeds again.
	mm := newFlakyMattermostServer(t, 2)
	port := freeTCPPort(t)
	cfgPath := writeRulesPushTestConfig(t, mm.URL, port)

	oldConfigPath := configPath
	configPath = cfgPath
	defer func() { configPath = oldConfigPath }()

	done := make(chan error, 1)
	go func() {
		done <- daemonCmd.RunE(daemonCmd, nil)
	}()

	pidPath := filepath.Join(filepath.Dir(cfgPath), "msggw.pid")
	waitForFile(t, pidPath, 5*time.Second)
	waitForListener(t, port, 5*time.Second)

	baseURL := "http://127.0.0.1:" + strconv.Itoa(port)
	update := rulesproto.RulesUpdate{
		Rules: []config.Rule{
			{Name: "pushed", Phones: []string{"+15145551212"}, Destination: config.Destination{Type: config.DestChannel, Team: "t", Channel: "c2"}},
		},
	}

	resp, _, errBody := rulesPut(t, baseURL, "nouser", "rules-secret", update)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("PUT /rules/nouser: status = %d, want %d (saved but not applied)", resp.StatusCode, http.StatusAccepted)
	}
	if errBody.Applied {
		t.Error("Applied = true despite the simulated Mattermost outage")
	}
	if !strings.Contains(errBody.Error, "previous configuration is still running") {
		t.Errorf("error message does not explain the rollback: %q", errBody.Error)
	}

	// The rules were saved to config.json regardless of the failed reload.
	onDisk, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("re-loading config.json: %v", err)
	}
	if len(onDisk.Users[0].Routing.Rules) != 1 || onDisk.Users[0].Routing.Rules[0].Name != "pushed" {
		t.Errorf("config.json was not updated despite reload failing: rules = %+v", onDisk.Users[0].Routing.Rules)
	}

	// The daemon must still be alive and serving on the rolled-back
	// generation, not dead or hung.
	waitForListener(t, port, 5*time.Second)

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("sending SIGTERM: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("daemonCmd.RunE returned an error on shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not shut down within 10s of SIGTERM (rollback likely left it in a bad state)")
	}
}
