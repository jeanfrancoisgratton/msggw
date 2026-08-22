// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 20:09:41
// Original filename: src/cmd/status.go

package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"msggw/internal/config"
	"msggw/internal/gmessages"
)

var statusOffline bool

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Report on the pairing, the Mattermost account and the stored mappings",
	Long: `Report the daemon's state: whether a Google Messages session is stored, which
phone it is paired with, whether the Mattermost token works, and which
conversations are currently bridged.

By default the Mattermost account is checked over the network. Use --offline to
report only on what is on disk.`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		// Status output is for a person reading it; the daemon's own logging
		// would only get in the way.
		log := slog.New(slog.NewTextHandler(io.Discard, nil))

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "configuration : %s\n\n", cfg.Path())

		reportSession(ctx, out, cfg, log)
		fmt.Fprintln(out)
		reportMattermost(ctx, out, cfg, log)
		fmt.Fprintln(out)
		return reportMappings(ctx, out, cfg)
	},
}

func reportSession(ctx context.Context, out io.Writer, cfg *config.Config, log *slog.Logger) {
	gmCfg, err := newGMessagesConfig(cfg, log)
	if err != nil {
		fmt.Fprintf(out, "google messages: MISCONFIGURED — %v\n", err)
		return
	}
	fmt.Fprintf(out, "google messages: session at %s\n", gmCfg.Session.Describe())

	client, err := gmessages.New(gmCfg)
	if errors.Is(err, gmessages.ErrNotPaired) {
		fmt.Fprintf(out, "                 NOT PAIRED — run \"msg-gw pair\"\n")
		return
	}
	if err != nil {
		fmt.Fprintf(out, "                 UNUSABLE — %v\n", err)
		return
	}

	if phoneID := client.PhoneID(); phoneID != "" {
		fmt.Fprintf(out, "                 paired with phone %s\n", phoneID)
	} else {
		fmt.Fprintf(out, "                 paired (the phone did not report an ID)\n")
	}

	if statusOffline {
		fmt.Fprintf(out, "                 not contacted (--offline)\n")
		return
	}

	// Connecting is the only way to know the session is still honoured: Google
	// can revoke a paired device at any time, and nothing on disk changes when
	// it does.
	if err := client.Connect(ctx); err != nil {
		fmt.Fprintf(out, "                 SESSION REJECTED — %v\n", err)
		return
	}
	defer client.Disconnect()

	conversations, err := client.ListConversations(ctx)
	if err != nil {
		fmt.Fprintf(out, "                 connected, but listing conversations failed: %v\n", err)
		return
	}
	fmt.Fprintf(out, "                 connected, %d conversations on the phone\n", len(conversations))
}

func reportMattermost(ctx context.Context, out io.Writer, cfg *config.Config, log *slog.Logger) {
	url, err := cfg.MattermostURL()
	if err != nil {
		fmt.Fprintf(out, "mattermost     : MISCONFIGURED — %v\n", err)
		return
	}
	fmt.Fprintf(out, "mattermost     : %s\n", url)
	fmt.Fprintf(out, "                 default route %s\n", cfg.Routing.Default.String())

	if statusOffline {
		fmt.Fprintf(out, "                 not contacted (--offline)\n")
		return
	}

	client, err := newMattermost(ctx, cfg, log)
	if err != nil {
		fmt.Fprintf(out, "                 UNREACHABLE — %v\n", err)
		return
	}
	fmt.Fprintf(out, "                 authenticated as @%s\n", client.BotUsername())
}

func reportMappings(ctx context.Context, out io.Writer, cfg *config.Config) error {
	db, err := openStorage(ctx, cfg)
	if err != nil {
		fmt.Fprintf(out, "database       : UNUSABLE — %v\n", err)
		return nil
	}
	defer db.Close()

	conversations, err := db.ListConversations(ctx)
	if err != nil {
		return err
	}
	messages, err := db.CountMessages(ctx)
	if err != nil {
		return err
	}

	if cfg.Backend.Driver == config.DatabaseDriverPostgres {
		fmt.Fprintf(out, "database       : postgres (%s)\n", cfg.Backend.Postgres.DSNRef)
	} else if path, err := cfg.SQLitePath(); err != nil {
		fmt.Fprintf(out, "database       : MISCONFIGURED — %v\n", err)
	} else {
		fmt.Fprintf(out, "database       : %s\n", path)
	}
	fmt.Fprintf(out, "                 %d conversations bridged, %d messages mapped\n",
		len(conversations), messages)

	if len(conversations) == 0 {
		return nil
	}

	fmt.Fprintln(out)
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CONVERSATION\tCHANNEL\tTHREAD\tLAST SEEN")
	for _, conv := range conversations {
		name := conv.DisplayName
		if name == "" {
			name = conv.ID
		}
		thread := conv.RootPostID
		if thread == "" {
			thread = "-"
		}
		lastSeen := "-"
		if !conv.LastSeen.IsZero() {
			lastSeen = conv.LastSeen.Local().Format(time.RFC3339)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", name, conv.ChannelID, thread, lastSeen)
	}
	return tw.Flush()
}

func init() {
	statusCmd.Flags().BoolVar(&statusOffline, "offline", false,
		"report only on local state, without contacting Google or Mattermost")
}
