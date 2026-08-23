// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 20:09:01
// Original filename: src/cmd/pair.go

package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/mdp/qrterminal/v3"
	"github.com/spf13/cobra"

	"msggw/internal/gmessages"
)

var (
	pairAttempts int
	pairPrintURL bool
)

var pairCmd = &cobra.Command{
	Use:   "pair",
	Short: "Pair the daemon with Google Messages on your phone",
	Long: `Pair the daemon with the Google Messages app on your Android phone.

A QR code is shown; on the phone, open Google Messages, then
Settings -> Device pairing -> QR code scanner, and scan it.

The QR code is only valid for 30 seconds. A new one is shown automatically
until the attempt limit is reached.

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

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		pairing := gmessages.NewPairing(gmCfg)
		defer pairing.Close()

		out := cmd.OutOrStdout()

		qr, err := pairing.Start(ctx)
		if err != nil {
			return err
		}

		for attempt := 1; ; attempt++ {
			log.Info("Google Messages pairing URL", "url", qr, "attempt", attempt, "max_attempts", pairAttempts)
			showQR(out, qr, attempt, pairAttempts)

			phoneID, err := pairing.Wait(ctx)
			if err == nil {
				fmt.Fprintf(out, "\nPaired with phone %s.\n", phoneID)
				fmt.Fprintf(out, "Session stored at %s.\n", gmCfg.Session.Describe())
				return verifyPairing(ctx, out, pairing)
			}

			if !errors.Is(err, gmessages.ErrPairingTimeout) {
				return err
			}
			if attempt >= pairAttempts {
				return fmt.Errorf("gave up after %d QR codes went unscanned", pairAttempts)
			}

			fmt.Fprintln(out, "\nThat QR code expired. Here is a new one.")
			if qr, err = pairing.Refresh(); err != nil {
				return err
			}
		}
	},
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

func showQR(out io.Writer, qr string, attempt, maxAttempts int) {
	if pairPrintURL {
		fmt.Fprintf(out, "\nPairing URL (attempt %d/%d):\n%s\n", attempt, maxAttempts, qr)
		return
	}

	fmt.Fprintf(out, "\nScan this with Google Messages (attempt %d/%d):\n\n", attempt, maxAttempts)
	// Half blocks keep the code small enough to fit an ordinary terminal, and
	// the QR standard's own quiet zone is what makes it scannable at all.
	qrterminal.GenerateWithConfig(qr, qrterminal.Config{
		Level:      qrterminal.L,
		Writer:     out,
		HalfBlocks: true,
		BlackChar:  qrterminal.BLACK_BLACK,
		WhiteChar:  qrterminal.WHITE_WHITE,
		QuietZone:  2,
	})
}

func init() {
	pairCmd.Flags().IntVarP(&pairAttempts, "attempts", "a", 6,
		"how many QR codes to show before giving up")
	pairCmd.Flags().BoolVar(&pairPrintURL, "print-url", false,
		"print the pairing URL instead of a QR code, for terminals that cannot render one")
}
