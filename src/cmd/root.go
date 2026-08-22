// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 20:08:04
// Original filename: src/cmd/root.go

package cmd

import (
	"os"
	"runtime"

	"github.com/spf13/cobra"
)

// configPath is the --config flag, shared by every subcommand that needs the
// configuration file.
var configPath string

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:     "message-gateway",
	Short:   "Google Messages (SMS/MMS/RCS) to Mattermost gateway",
	Version: "0.2.0 (2026.08.22), Go version = " + runtime.Version(),
	Long: `message-gateway bridges the SMS, MMS and RCS conversations of an Android phone
running Google Messages into a Mattermost server, and sends replies typed in
Mattermost back out through Google Messages.

The daemon pairs with the phone the same way Google Messages for Web does, so
it carries real RCS traffic rather than downgrading messages to SMS.

Getting started:

  message-gateway config sample > /etc/msggw/config.json
  $EDITOR /etc/msggw/config.json
  message-gateway config check
  message-gateway pair
  message-gateway daemon`,
}

func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.DisableAutoGenTag = true
	rootCmd.CompletionOptions.DisableDefaultCmd = true

	rootCmd.PersistentFlags().StringVarP(&configPath, "config", "c", "",
		"configuration file (default /etc/msggw/config.json, then $XDG_CONFIG_HOME/msggw/config.json)")

	rootCmd.AddCommand(completionCmd)
	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(pairCmd)
	rootCmd.AddCommand(daemonCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(logoutCmd)
}
