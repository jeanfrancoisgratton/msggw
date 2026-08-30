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
	"time"

	"github.com/spf13/cobra"

	"msggw/internal/gmessages"
	"msggw/internal/pairclient"
)

var (
	pairCookiesFile        string
	pairRemote             string
	pairToken              string
	pairTokenFile          string
	pairInsecureSkipVerify bool
)

var pairCmd = &cobra.Command{
	Use:   "pair NAME",
	Short: "Pair the daemon with Google Messages on your phone",
	Long: `Pair the daemon with the Google Messages app on your Android phone.

NAME must match one of the "name" entries under "users" in the configuration —
this is which tenant the pairing is stored against.

Google killed QR-code device pairing, so this authenticates as your Google
account instead. Sign into https://messages.google.com/web in a private
browser window, open devtools, and copy the SID, HSID, SSID, OSID, APISID and
SAPISID cookies (and __Secure-1PSIDTS if present) into a JSON file:

  {"SID": "...", "HSID": "...", "SSID": "...", "OSID": "...",
   "APISID": "...", "SAPISID": "...", "__Secure-1PSIDTS": "..."}

Pass that file with --cookies-file, or pipe it to stdin. The daemon then shows
an emoji; tap the matching one on Google Messages on the phone to confirm.

Once paired, the session is stored at that user's gmessages.session_ref and the
daemon can reconnect without pairing again.

With --remote, this runs in "client mode" (see docs/MULTI-TENANCY.md): the
cookies never leave the machine this command runs on except over the network
to the daemon named by --remote. This is what a client's own copy of msg-gw
uses to pair without ever touching the daemon's configuration, and without the
Google sign-in itself happening from the daemon's host — a VPS's IP signing
into a Google account with no prior login history for it is exactly the
profile Google's fraud detection flags. --remote needs a bearer token for that
user, set on the daemon side under that user's remote_pairing.token_ref; pass
it here with --token, --token-file, or the MSGGW_PAIR_TOKEN environment
variable.`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		cookies, err := readCookies(cmd)
		if err != nil {
			return err
		}

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		out := cmd.OutOrStdout()

		if pairRemote != "" {
			return runRemotePairing(ctx, out, args[0], cookies)
		}

		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		log := newLogger(cfg)

		user, err := findUser(cfg, args[0])
		if err != nil {
			return err
		}

		gmCfg, err := newGMessagesConfig(user, cfg, log)
		if err != nil {
			return err
		}

		pairing := gmessages.NewPairing(gmCfg)
		defer pairing.Close()

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

// runRemotePairing drives the same pairing flow as the local branch above,
// but against a daemon's remote-pairing HTTP endpoint instead of a local
// gmessages.Pairing — see docs/MULTI-TENANCY.md, "client mode".
func runRemotePairing(ctx context.Context, out io.Writer, name string, cookies map[string]string) error {
	token, err := resolvePairToken()
	if err != nil {
		return err
	}
	client := pairclient.New(pairRemote, token, pairInsecureSkipVerify)

	startCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	start, err := client.Start(startCtx, name, cookies)
	if err != nil {
		return fmt.Errorf("starting pairing on %s: %w", pairRemote, err)
	}

	fmt.Fprintf(out, "\nOn the phone, open Google Messages and tap this emoji when it's offered:\n\n  %s\n\n", start.Emoji)
	fmt.Fprintln(out, "Waiting for confirmation...")

	result, err := client.Wait(ctx, name, start.PairingID)
	if err != nil {
		return fmt.Errorf("waiting for confirmation: %w", err)
	}

	fmt.Fprintf(out, "\nPaired with phone %s.\n", result.PhoneID)
	fmt.Fprintf(out, "The daemon verified the session and received %d conversations.\n", result.Conversations)
	return nil
}

// resolvePairToken finds the bearer token for --remote, in the order the
// command's help text documents: the flag, then a file, then the environment.
func resolvePairToken() (string, error) {
	if pairToken != "" {
		return pairToken, nil
	}
	if pairTokenFile != "" {
		data, err := os.ReadFile(pairTokenFile)
		if err != nil {
			return "", fmt.Errorf("reading --token-file: %w", err)
		}
		return strings.TrimSpace(string(data)), nil
	}
	if token := os.Getenv("MSGGW_PAIR_TOKEN"); token != "" {
		return token, nil
	}
	return "", fmt.Errorf("--remote needs a token: pass --token, --token-file, or set MSGGW_PAIR_TOKEN")
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
	if err := gmessages.ValidateCookies(cookies); err != nil {
		return nil, err
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
	pairCmd.Flags().StringVar(&pairRemote, "remote", "",
		"pair against a daemon over the network (e.g. https://msggw.example.net:8443) instead of locally — client mode, see docs/MULTI-TENANCY.md")
	pairCmd.Flags().StringVar(&pairToken, "token", "",
		"bearer token for --remote (or use --token-file / MSGGW_PAIR_TOKEN)")
	pairCmd.Flags().StringVar(&pairTokenFile, "token-file", "",
		"file containing the bearer token for --remote")
	pairCmd.Flags().BoolVar(&pairInsecureSkipVerify, "insecure-skip-verify", false,
		"skip TLS certificate verification for --remote (testing only)")
}
