// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 20:08:04
// Original filename: src/cmd/root.go

package cmd

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	hftx "github.com/jeanfrancoisgratton/helperFunctions/v5/terminalfx"
	"github.com/spf13/cobra"
)

// configPath is the --config flag, shared by every subcommand that needs the
// configuration file.
var configPath string

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "message-gateway",
	Short: "Google Messages (SMS/MMS/RCS) to Mattermost gateway",
	Long: `message-gateway bridges the SMS, MMS and RCS conversations of an Android phone
running Google Messages into a Mattermost server, and sends replies typed in
Mattermost back out through Google Messages.

The daemon pairs with the phone the same way Google Messages for Web does, so
it carries real RCS traffic rather than downgrading messages to SMS.

Getting started (no root needed — this writes to your own
~/.config/JFG/msggw/config.json):

  message-gateway config sample > ~/.config/JFG/msggw/config.json
  $EDITOR ~/.config/JFG/msggw/config.json
  message-gateway config check
  message-gateway pair
  message-gateway daemon`,
}

// buildVersion and buildDate are set via -ldflags -X at package-build time.
// Each __*/ builder reads its own already-authoritative version field
// (APKBUILD's pkgver, PKGBUILD's pkgver, control's Version minus the Debian
// revision, the spec's %{_version}); src/build.sh, __macos/build-macos.sh and
// __windows/Makefile read msggw.json since they aren't tied to any one
// distro's packaging file. The fallbacks below are what you get from a plain
// `go build .` with no ldflags, e.g. local development.
var buildVersion = "dev"
var buildDate = "unknown"

var versionCmd = &cobra.Command{
	Use:     "version",
	Example: "msg-gw version",
	Short:   "Shows the software version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(hftx.White("msg-gw v" + buildVersion + " (" + buildDate + "), Go version = v" + strings.TrimPrefix(runtime.Version(), "go") + " (" + runtime.GOARCH + ")"))
	},
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
		"configuration file (default ~/.config/JFG/msggw/config.json, then /etc/msggw/config.json)")

	rootCmd.AddCommand(completionCmd, configCmd, pairCmd, daemonCmd, reloadCmd, statusCmd, logoutCmd, rulesCmd, backfillCmd, tokengenCmd, versionCmd)
}
