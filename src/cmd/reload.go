// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/cmd/reload.go

package cmd

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
)

var reloadCmd = &cobra.Command{
	Use:   "reload",
	Short: "Ask a running daemon to reload its configuration",
	Long: `Send SIGHUP to a running "message-gateway daemon" process, asking it to
re-read config.json and, if it is still valid, restart every user's bridge
and the Mattermost connection against the new configuration — all inside
the same process. This is the intended way to pick up a "msg-gw rules
add/remove" change (or any other config.json edit) when the daemon was
started with "exec" as a container's entrypoint, where there is no
supervisor around to restart it for you. The listener (remote pairing,
remote rules management) is not part of this restart — it runs for the
whole lifetime of the process and keeps serving throughout.

This command finds the daemon by its pid file at "<state_dir>/msggw.pid",
resolved from the same "--config" (or default paths) you would give the
daemon itself — run it with the same configuration and there is nothing else
to point at the right process.

An invalid configuration is rejected by the daemon and never applied; it
keeps running on the configuration it already had. The same is true if the
configuration is valid but the daemon fails to actually start it (e.g.
Mattermost briefly unreachable): the daemon reverts to the configuration it
had running before. Sending the signal only proves the daemon received it —
check its logs for "reload:" to see whether the new configuration was
actually accepted.`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}

		pidPath := cfg.PIDFile()
		raw, err := os.ReadFile(pidPath)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("no pid file at %s: is the daemon running with this configuration?", pidPath)
			}
			return fmt.Errorf("reading %s: %w", pidPath, err)
		}

		pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil || pid <= 0 {
			return fmt.Errorf("%s does not contain a valid pid: %q", pidPath, strings.TrimSpace(string(raw)))
		}

		proc, err := os.FindProcess(pid)
		if err != nil {
			return fmt.Errorf("finding process %d: %w", pid, err)
		}
		if err := proc.Signal(syscall.SIGHUP); err != nil {
			return fmt.Errorf("signaling the daemon (pid %d): %w", pid, err)
		}

		fmt.Fprintf(cmd.OutOrStdout(),
			"Sent SIGHUP to the daemon (pid %d). Check its logs to confirm the reload succeeded.\n", pid)
		return nil
	},
}
