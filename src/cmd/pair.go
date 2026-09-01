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
	"golang.org/x/term"

	"msggw/internal/browserauth"
	"msggw/internal/config"
	"msggw/internal/gmessages"
	"msggw/internal/pairclient"
)

var (
	pairCookiesFile        string
	pairNoBrowser          bool
	pairRemote             string
	pairToken              string
	pairTokenFile          string
	pairInsecureSkipVerify bool
	pairEmail              string
	pairMattermostServer   string
	pairMattermostUser     string
)

var pairCmd = &cobra.Command{
	Use:   "pair NAME",
	Short: "Pair the daemon with Google Messages on your phone",
	Long: `Pair the daemon with the Google Messages app on your Android phone.

NAME is which tenant the pairing is stored against — a "name" entry under
"users" in the configuration. If NAME does not exist yet, --mattermost-user
provisions it: pair creates that users[] entry (routed to a direct message
with --mattermost-user by default) before pairing, using the same
config-mutation and validation "msg-gw rules" uses, so a bad provisioning
request is rejected before it ever touches config.json. --email records which
Google account NAME is expected to pair with (shown by "msg-gw status") —
it is documentation only, never verified against the account you actually
sign into. --mattermost-server, if given, is checked against the configured
mattermost.url as a sanity check, useful when several deployments' configs
might otherwise be confused for one another. None of --email,
--mattermost-server or --mattermost-user do anything once NAME already
exists — provisioning only happens once, the first time NAME is paired.
Provisioning is local-only; it has no effect with --remote (see below), where
the daemon's own configuration must already have NAME.

Google killed QR-code device pairing, so this authenticates as your Google
account instead. By default, pair opens a browser window for you to sign into
that account — nothing to copy, nothing to configure. Once you're signed in,
the window closes on its own and pairing continues. The daemon then shows an
emoji; tap the matching one on Google Messages on the phone to confirm.

Once paired, the session is stored under root_dir (see
docs/CONFIGURATION.md#gmessages) and the daemon can reconnect without pairing
again.

For environments where a browser can't be opened (a headless server, an
SSH-only box, a scripted pairing pipeline), a manual fallback still exists:
sign into https://messages.google.com/web yourself, open devtools, and copy
the SID, HSID, SSID, OSID, APISID and SAPISID cookies (and __Secure-1PSIDTS if
present) into a JSON file:

  {"SID": "...", "HSID": "...", "SSID": "...", "OSID": "...",
   "APISID": "...", "SAPISID": "...", "__Secure-1PSIDTS": "..."}

Reach it with --cookies-file, by piping that JSON to stdin, or with
--no-browser (which reads stdin without needing to also make stdin
non-interactive). See docs/RUNNING.md, "Fallback: manual cookies", for the
full walkthrough.

With --remote, this runs in "client mode" (see docs/SOLUTION.md): the
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
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		out := cmd.OutOrStdout()

		cookies, err := resolveCookies(ctx, cmd, out)
		if err != nil {
			return err
		}

		if pairRemote != "" {
			if pairEmail != "" || pairMattermostServer != "" || pairMattermostUser != "" {
				return fmt.Errorf("--email, --mattermost-server and --mattermost-user provision a user locally; " +
					"they have no effect with --remote, where the daemon's own configuration must already have NAME")
			}
			return runRemotePairing(ctx, out, args[0], cookies)
		}

		cfg, err := loadConfig()
		if err != nil {
			return err
		}

		cfg, user, err := provisionUser(out, cfg, args[0])
		if err != nil {
			return err
		}
		log := newLogger(cfg)

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

// provisionUser finds name among cfg's users, or — if --mattermost-user was
// given — creates it via config.Mutate, so "pair" can be the first command
// run against a brand-new tenant instead of requiring config.json to be
// hand-edited first. An existing user is returned unchanged; --email,
// --mattermost-server and --mattermost-user are only consulted the one time
// a user does not exist yet.
func provisionUser(out io.Writer, cfg *config.Config, name string) (*config.Config, config.UserConfig, error) {
	if user, err := findUser(cfg, name); err == nil {
		if pairMattermostServer != "" {
			if err := checkMattermostServer(cfg, pairMattermostServer); err != nil {
				return nil, config.UserConfig{}, err
			}
		}
		if pairEmail != "" || pairMattermostUser != "" {
			fmt.Fprintf(out, "%q already exists; ignoring --email/--mattermost-user (only used to provision a new user).\n", name)
		}
		return cfg, user, nil
	}

	if pairMattermostUser == "" {
		return nil, config.UserConfig{}, fmt.Errorf(
			"no user named %q in %s; pass --mattermost-user to create one (see \"msg-gw pair --help\")", name, cfg.Path())
	}
	if pairMattermostServer != "" {
		if err := checkMattermostServer(cfg, pairMattermostServer); err != nil {
			return nil, config.UserConfig{}, err
		}
	}

	newUser := config.UserConfig{
		Name: name,
		GMessages: config.GMessagesConfig{
			GoogleAccount: pairEmail,
		},
		Routing: config.RoutingConfig{
			DefaultDirect: config.Destination{Type: config.DestDirect, User: pairMattermostUser},
		},
	}

	newCfg, err := config.Mutate(cfg.Path(), func(c *config.Config) error {
		for _, u := range c.Users {
			if u.Name == name {
				return fmt.Errorf("%q already exists in %s", name, cfg.Path())
			}
		}
		c.Users = append(c.Users, newUser)
		return nil
	})
	if err != nil {
		return nil, config.UserConfig{}, err
	}

	fmt.Fprintf(out, "Created user %q, routed to a direct message with @%s.\n", name, pairMattermostUser)

	user, err := findUser(newCfg, name)
	if err != nil {
		return nil, config.UserConfig{}, err
	}
	return newCfg, user, nil
}

// checkMattermostServer compares --mattermost-server against the daemon's
// actual configured Mattermost URL — a sanity check against pairing (or
// provisioning) against the wrong deployment's configuration.
func checkMattermostServer(cfg *config.Config, given string) error {
	actual, err := cfg.MattermostURL()
	if err != nil {
		return fmt.Errorf("--mattermost-server: could not resolve the configured mattermost.url to check against: %w", err)
	}
	if strings.TrimRight(given, "/") != strings.TrimRight(actual, "/") {
		return fmt.Errorf("--mattermost-server %q does not match the configured mattermost.url %q", given, actual)
	}
	return nil
}

// runRemotePairing drives the same pairing flow as the local branch above,
// but against a daemon's remote-pairing HTTP endpoint instead of a local
// gmessages.Pairing — see docs/SOLUTION.md, "client mode".
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

// resolveCookies picks where the Google account cookies come from: an
// explicit --cookies-file always wins; --no-browser or a piped (non-tty)
// stdin falls back to reading JSON from stdin; otherwise — the default —
// a browser opens and drives the sign-in itself, so pairing never asks a
// non-technical user to touch devtools or hand-copy a cookies.json.
func resolveCookies(ctx context.Context, cmd *cobra.Command, out io.Writer) (map[string]string, error) {
	switch {
	case pairCookiesFile != "":
		return readCookiesFile(pairCookiesFile)
	case pairNoBrowser || !isInteractive(cmd):
		return readCookiesStdin(cmd)
	default:
		cookies, err := browserauth.CaptureCookies(ctx, out)
		if err != nil {
			return nil, err
		}
		if err := gmessages.ValidateCookies(cookies); err != nil {
			return nil, err
		}
		return cookies, nil
	}
}

// isInteractive reports whether cmd's stdin is an actual terminal, rather
// than a pipe or a file — the signal to launch a browser instead of trying
// to read cookies JSON off it.
func isInteractive(cmd *cobra.Command) bool {
	f, ok := cmd.InOrStdin().(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// readCookiesFile loads the Google account cookies from a --cookies-file.
func readCookiesFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening cookies file: %w", err)
	}
	defer f.Close()
	return decodeCookies(f)
}

// readCookiesStdin loads the Google account cookies as JSON from stdin —
// the fallback for --no-browser and scripted/piped invocations.
func readCookiesStdin(cmd *cobra.Command) (map[string]string, error) {
	return decodeCookies(cmd.InOrStdin())
}

func decodeCookies(r io.Reader) (map[string]string, error) {
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
		"fallback: JSON file with Google account cookies, instead of the automated browser sign-in")
	pairCmd.Flags().BoolVar(&pairNoBrowser, "no-browser", false,
		"fallback: skip the automated browser sign-in and read cookies JSON from stdin instead")
	pairCmd.Flags().StringVar(&pairRemote, "remote", "",
		"pair against a daemon over the network (e.g. https://msggw.example.net:8443) instead of locally — client mode, see docs/SOLUTION.md")
	pairCmd.Flags().StringVar(&pairToken, "token", "",
		"bearer token for --remote (or use --token-file / MSGGW_PAIR_TOKEN)")
	pairCmd.Flags().StringVar(&pairTokenFile, "token-file", "",
		"file containing the bearer token for --remote")
	pairCmd.Flags().BoolVar(&pairInsecureSkipVerify, "insecure-skip-verify", false,
		"skip TLS certificate verification for --remote (testing only)")
	pairCmd.Flags().StringVar(&pairEmail, "email", "",
		"the Google account NAME is expected to pair with; recorded for \"msg-gw status\", never verified (only used when provisioning a new NAME)")
	pairCmd.Flags().StringVar(&pairMattermostServer, "mattermost-server", "",
		"checked against the configured mattermost.url as a sanity check")
	pairCmd.Flags().StringVar(&pairMattermostUser, "mattermost-user", "",
		"the Mattermost username NAME's messages default to (required to provision a new NAME)")
}
