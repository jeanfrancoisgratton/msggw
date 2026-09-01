// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/cmd/daemon_reload_test.go

package cmd

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeMattermostServer is just enough of the Mattermost REST API for
// newMattermost's Connect call to succeed: one bot identity, always.
func fakeMattermostServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/users/me", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "bot-id", "username": "msggw-bot", "is_bot": true})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// freeTCPPort hands back a port nothing is listening on right now. There is
// an inherent TOCTOU gap between this and the daemon actually binding it,
// but it is the standard way to pick an ephemeral port for a test.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// writeReloadTestConfig writes a valid configuration with a listener enabled
// (on a free port) and one never-paired user, against mmURL as the
// Mattermost server.
func writeReloadTestConfig(t *testing.T, mmURL string, listenerPort int) string {
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
	     "routing": {"default_direct": {"type": "channel", "team": "t", "channel": "c"}}}
	  ]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing test config: %v", err)
	}
	return path
}

// TestDaemonReloadsOnSIGHUP runs the real daemonCmd.RunE against a config
// pointed at a fake Mattermost server, with no Google Messages session (that
// user's bridge just retries harmlessly in the background), and drives it
// through a full SIGHUP reload followed by a clean SIGTERM shutdown. This is
// the concurrency the reload feature actually depends on — the generation
// loop, the pid file, and the listener releasing its port before the next
// generation tries to rebind it — so it is worth exercising end to end
// rather than only unit-testing acquirePIDFile/removePIDFile in isolation.
func TestDaemonReloadsOnSIGHUP(t *testing.T) {
	t.Setenv("MSGGW_TEST_TOKEN", "test-token")
	mm := fakeMattermostServer(t)
	port := freeTCPPort(t)
	cfgPath := writeReloadTestConfig(t, mm.URL, port)

	oldConfigPath := configPath
	configPath = cfgPath
	defer func() { configPath = oldConfigPath }()

	done := make(chan error, 1)
	go func() {
		done <- daemonCmd.RunE(daemonCmd, nil)
	}()

	pidPath := filepath.Join(filepath.Dir(cfgPath), "msggw.pid")
	waitForFile(t, pidPath, 5*time.Second)

	rawPID, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("reading pid file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(rawPID)))
	if err != nil || pid != os.Getpid() {
		t.Fatalf("pid file = %q, want this test process's own pid %d", rawPID, os.Getpid())
	}

	// The listener binds asynchronously; give it a moment before reloading,
	// so the reload actually has to reclaim a bound port rather than a free
	// one — that ordering is the whole point of this test.
	waitForListener(t, port, 5*time.Second)

	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("sending SIGHUP: %v", err)
	}

	// After a reload, the listener should still (or again) be reachable on
	// the same port — proof the new generation successfully rebound it.
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

	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("pid file still present after shutdown: err = %v", err)
	}
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s did not appear within %s", path, timeout)
}

func waitForListener(t *testing.T, port int, timeout time.Duration) {
	t.Helper()
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("nothing listening on %s within %s", addr, timeout)
}
