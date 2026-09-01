// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/cmd/daemon_test.go

package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"msggw/internal/config"
)

// writeDaemonTestConfig writes a minimal, valid configuration whose
// state_dir is a fresh temp directory, so cfg.PIDFile() is always writable.
func writeDaemonTestConfig(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{
	  "state_dir": "` + dir + `",
	  "root_dir": "/tmp",
	  "mattermost": {"url": "https://mm.example.net", "token_ref": "env:TOKEN"},
	  "users": [
	    {"name": "existing",
	     "routing": {"default_direct": {"type": "channel", "team": "t", "channel": "c"}}}
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

// deadPID returns a process ID guaranteed not to belong to any running
// process: it starts a child, waits for it to exit and be reaped, then hands
// back that now-vacated PID.
func deadPID(t *testing.T) int {
	t.Helper()
	c := exec.Command("true")
	if err := c.Run(); err != nil {
		t.Fatalf("running a throwaway process: %v", err)
	}
	return c.Process.Pid
}

func TestAcquirePIDFileWritesOwnPID(t *testing.T) {
	cfg := writeDaemonTestConfig(t)

	if err := acquirePIDFile(cfg); err != nil {
		t.Fatalf("acquirePIDFile: %v", err)
	}

	raw, err := os.ReadFile(cfg.PIDFile())
	if err != nil {
		t.Fatalf("reading pid file: %v", err)
	}
	got, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("pid file does not contain a plain integer: %q", raw)
	}
	if got != os.Getpid() {
		t.Errorf("pid file contains %d, want this process's pid %d", got, os.Getpid())
	}
}

func TestAcquirePIDFileRefusesWhileAnotherIsAlive(t *testing.T) {
	cfg := writeDaemonTestConfig(t)

	// This test process is, definitionally, alive — standing in for another
	// daemon instance without needing to actually spawn one.
	if err := os.WriteFile(cfg.PIDFile(), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		t.Fatalf("seeding pid file: %v", err)
	}

	err := acquirePIDFile(cfg)
	if err == nil {
		t.Fatal("acquirePIDFile succeeded despite a live pid already in the file")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("error does not mention the daemon already running: %v", err)
	}
}

func TestAcquirePIDFileOverwritesStalePID(t *testing.T) {
	cfg := writeDaemonTestConfig(t)
	stale := deadPID(t)

	if err := os.WriteFile(cfg.PIDFile(), []byte(strconv.Itoa(stale)+"\n"), 0o644); err != nil {
		t.Fatalf("seeding pid file: %v", err)
	}

	if err := acquirePIDFile(cfg); err != nil {
		t.Fatalf("acquirePIDFile refused a stale, dead pid: %v", err)
	}

	raw, err := os.ReadFile(cfg.PIDFile())
	if err != nil {
		t.Fatalf("reading pid file: %v", err)
	}
	if strings.TrimSpace(string(raw)) != strconv.Itoa(os.Getpid()) {
		t.Errorf("pid file = %q, want this process's own pid", raw)
	}
}

func TestRemovePIDFile(t *testing.T) {
	cfg := writeDaemonTestConfig(t)
	if err := acquirePIDFile(cfg); err != nil {
		t.Fatalf("acquirePIDFile: %v", err)
	}

	log := newLogger(cfg)
	removePIDFile(cfg, log)
	if _, err := os.Stat(cfg.PIDFile()); !os.IsNotExist(err) {
		t.Errorf("pid file still present after removePIDFile: err = %v", err)
	}

	// Removing an already-absent pid file must not panic or otherwise
	// misbehave — the daemon calls this unconditionally on shutdown.
	removePIDFile(cfg, log)
}
