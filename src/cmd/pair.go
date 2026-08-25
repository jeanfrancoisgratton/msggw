// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 20:09:01
// Original filename: src/cmd/pair.go

package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"msggw/internal/gmessages"
)

var pairCookiesFile string

// requiredCookies is Google's own minimum set for the Gaia pairing handshake;
// __Secure-1PSIDTS is usually present too but is not load-bearing the way
// these are.
var requiredCookies = []string{"SID", "HSID", "SSID", "OSID", "APISID", "SAPISID"}

var pairCmd = &cobra.Command{
	Use:   "pair",
	Short: "Pair the daemon with Google Messages on your phone",
	Long: `Pair the daemon with the Google Messages app on your Android phone.

Google killed QR-code device pairing, so this authenticates as your Google
account instead. Sign into https://messages.google.com/web in a private
browser window, open devtools, and copy the SID, HSID, SSID, OSID, APISID and
SAPISID cookies (and __Secure-1PSIDTS if present) into a JSON file:

  {"SID": "...", "HSID": "...", "SSID": "...", "OSID": "...",
   "APISID": "...", "SAPISID": "...", "__Secure-1PSIDTS": "..."}

Pass that file with --cookies-file, or pipe it to stdin. The daemon then shows
an emoji; tap the matching one on Google Messages on the phone to confirm.

Once paired, the session is stored at gmessages.session_ref and the daemon can
reconnect without pairing again.`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		log := newLogger(cfg)

		gmCfg, err := newGMessagesConfig(cfg, log)
		if err != nil {
			return err
		}

		cookies, err := readCookies(cmd)
		if err != nil {
			return err
		}

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		pairing := gmessages.NewPairing(gmCfg)
		defer pairing.Close()

		out := cmd.OutOrStdout()

		emoji, err := pairing.Start(ctx, cookies)
		if err != nil {
			return err
		}

		fmt.Fprintf(out, "\nOn the phone, open Google Messages and tap this emoji when it's offered:\n\n  %s\n\n", emoji)
		fmt.Fprintln(out, "Waiting for confirmation...")

		phoneID, err := pairing.Wait(ctx)
		if err != nil {
			return err
		}

		fmt.Fprintf(out, "\nPaired with phone %s.\n", phoneID)
		fmt.Fprintf(out, "Session stored at %s.\n", gmCfg.Session.Describe())
		return verifyPairing(ctx, out, pairing)
	},
}

// readCookies loads the Google account cookies from --cookies-file, or from
// stdin when that flag is not given.
func readCookies(cmd *cobra.Command) (map[string]string, error) {
	var r io.Reader
	if pairCookiesFile != "" {
		f, err := os.Open(pairCookiesFile)
		if err != nil {
			return nil, fmt.Errorf("opening cookies file: %w", err)
		}
		defer f.Close()
		r = f
	} else {
		r = cmd.InOrStdin()
	}

	var cookies map[string]string
	if err := json.NewDecoder(r).Decode(&cookies); err != nil {
		return nil, fmt.Errorf("reading cookies as JSON: %w", err)
	}

	var missing []string
	for _, name := range requiredCookies {
		if cookies[name] == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required cookies: %s", strings.Join(missing, ", "))
	}
	return cookies, nil
}

// verifyPairing proves the session works by reconnecting with it and waiting
// for the phone to send its conversation list — the success criterion phase 1
// of docs/SOLUTION.md sets.
func verifyPairing(ctx context.Context, out io.Writer, pairing *gmessages.Pairing) error {
	fmt.Fprintln(out, "\nVerifying the session by reconnecting...")

	conversations, err := pairing.Verify(ctx)
	if err != nil {
		return fmt.Errorf("the pairing was stored, but reconnecting with it failed: %w", err)
	}

	fmt.Fprintf(out, "The phone sent %d conversations:\n\n", len(conversations))
	shown := len(conversations)
	if shown > 10 {
		shown = 10
	}
	for _, conv := range conversations[:shown] {
		kind := "SMS"
		if conv.Type.String() == "RCS" {
			kind = "RCS"
		}
		fmt.Fprintf(out, "  %-40s  %s\n", conv.Title(), kind)
	}
	if len(conversations) > shown {
		fmt.Fprintf(out, "  ... and %d more\n", len(conversations)-shown)
	}

	fmt.Fprintln(out, "\nPairing complete. Start the bridge with \"msg-gw daemon\".")
	return nil
}

func init() {
	pairCmd.Flags().StringVar(&pairCookiesFile, "cookies-file", "",
		"JSON file with Google account cookies (reads stdin if omitted)")
}
