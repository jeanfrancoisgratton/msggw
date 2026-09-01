// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/cmd/rules.go

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"msggw/internal/config"
	"msggw/internal/rulesclient"
	"msggw/internal/rulesproto"
)

var rulesCmd = &cobra.Command{
	Use:   "rules",
	Short: "Manage a user's routing rules",
	Long: `Add, remove or list one user's routing rules without hand-editing config.json.

A change made here is validated exactly as the daemon validates config.json
at startup — see "msg-gw config check" — and is only written to disk once the
result loads cleanly. The running daemon does not pick up the change on its
own; run "msg-gw reload" (or send it SIGHUP, or restart it) afterwards.`,
}

var rulesListCmd = &cobra.Command{
	Use:   "list NAME",
	Short: "List a user's routing rules",
	Long: `List the routing rules for the user named NAME, in the order they are
evaluated (first match wins), followed by the two defaults that apply when
nothing matches.`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		user, err := findUser(cfg, args[0])
		if err != nil {
			return err
		}

		out := cmd.OutOrStdout()
		if len(user.Routing.Rules) == 0 {
			fmt.Fprintf(out, "%s has no routing rules.\n\n", args[0])
		} else {
			fmt.Fprintf(out, "%s's routing rules, in evaluation order:\n\n", args[0])
			for i, rule := range user.Routing.Rules {
				fmt.Fprintf(out, "%2d. %-24s  %-40s -> %s\n",
					i+1, ruleLabel(rule), ruleCriteria(rule), rule.Destination.String())
			}
			fmt.Fprintln(out)
		}
		fmt.Fprintf(out, "default (direct): %s\n", user.Routing.DefaultDirect.String())
		fmt.Fprintf(out, "default (group) : %s\n", defaultGroupLog(user.Routing))
		return nil
	},
}

var (
	ruleAddName        string
	ruleAddConvIDs     []string
	ruleAddPhones      []string
	ruleAddNamePattern string
	ruleAddGroupsOnly  bool
	ruleAddDirectsOnly bool
	ruleAddToChannel   string
	ruleAddToChannelID string
	ruleAddToUser      string
	ruleAddToUsers     []string
)

var rulesAddCmd = &cobra.Command{
	Use:   "add NAME",
	Short: "Add a routing rule for a user",
	Long: `Add a routing rule for the user named NAME. It is appended to the end of
NAME's rule list — rules are evaluated in "msg-gw rules list NAME"'s order,
first match wins, so a rule meant to take precedence over an existing one has
to be added before it (remove and re-add, for now — there is no "insert at
position" yet).

A rule needs at least one matching criterion:

  --conversation-id ID    matches a Google Messages conversation ID exactly (repeatable)
  --phone NUMBER          matches any participant's phone number (repeatable)
  --name-pattern REGEXP   matches the conversation's display name

and/or a shape filter:

  --groups-only           only group conversations
  --directs-only          only one-to-one conversations

and exactly one destination:

  --to-channel TEAM/CHANNEL   a named channel
  --to-channel-id ID          a channel by its raw ID
  --to-user USERNAME          a direct message with USERNAME
  --to-users USER1,USER2,...  a group message (2-7 members)

The change is validated the same way "msg-gw config check" validates
config.json, and only written if the result still loads cleanly — but the
running daemon does not pick it up until it is reloaded (see "msg-gw
reload").

Example:

  msg-gw rules add jfgratton --name family \
    --phone "+1 514 555-1212" --phone "+1 514 555-1213" \
    --to-user jfgratton`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		if _, err := findUser(cfg, args[0]); err != nil {
			return err
		}

		dest, err := destinationFromFlags(ruleAddToChannel, ruleAddToChannelID, ruleAddToUser, ruleAddToUsers)
		if err != nil {
			return err
		}

		rule := config.Rule{
			Name:            ruleAddName,
			ConversationIDs: ruleAddConvIDs,
			Phones:          ruleAddPhones,
			NamePattern:     ruleAddNamePattern,
			GroupsOnly:      ruleAddGroupsOnly,
			DirectsOnly:     ruleAddDirectsOnly,
			Destination:     dest,
		}

		var position int
		if _, err := config.Mutate(cfg.Path(), func(c *config.Config) error {
			for i := range c.Users {
				if c.Users[i].Name != args[0] {
					continue
				}
				c.Users[i].Routing.Rules = append(c.Users[i].Routing.Rules, rule)
				position = len(c.Users[i].Routing.Rules)
				return nil
			}
			return fmt.Errorf("no user named %q in %s", args[0], cfg.Path())
		}); err != nil {
			return err
		}

		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Added rule %d for %s: %s -> %s\n", position, args[0], ruleCriteria(rule), dest.String())
		fmt.Fprintln(out, `Run "msg-gw reload" to pick up the change.`)
		return nil
	},
}

var rulesRemoveCmd = &cobra.Command{
	Use:   "remove NAME INDEX",
	Short: "Remove one of a user's routing rules",
	Long: `Remove routing rule number INDEX (1-based, as shown by "msg-gw rules list
NAME") for the user named NAME.

The change is validated the same way "msg-gw config check" validates
config.json — removing a rule cannot itself make the configuration invalid,
but the running daemon still needs to be reloaded (see "msg-gw reload") to
pick up the change.`,
	Args:         cobra.ExactArgs(2),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		if _, err := findUser(cfg, args[0]); err != nil {
			return err
		}

		index, err := strconv.Atoi(args[1])
		if err != nil || index < 1 {
			return fmt.Errorf("INDEX must be a positive integer (see %q), got %q",
				"msg-gw rules list "+args[0], args[1])
		}

		var removed config.Rule
		if _, err := config.Mutate(cfg.Path(), func(c *config.Config) error {
			for i := range c.Users {
				if c.Users[i].Name != args[0] {
					continue
				}
				rules := c.Users[i].Routing.Rules
				if index > len(rules) {
					return fmt.Errorf("user %q has %d routing rule(s); no rule %d", args[0], len(rules), index)
				}
				removed = rules[index-1]
				c.Users[i].Routing.Rules = append(rules[:index-1:index-1], rules[index:]...)
				return nil
			}
			return fmt.Errorf("no user named %q in %s", args[0], cfg.Path())
		}); err != nil {
			return err
		}

		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Removed rule %d for %s: %s -> %s\n",
			index, args[0], ruleCriteria(removed), removed.Destination.String())
		fmt.Fprintln(out, `Run "msg-gw reload" to pick up the change.`)
		return nil
	},
}

// ruleLabel renders a rule's optional Name for display, since it has no
// effect on matching and is commonly left unset.
func ruleLabel(r config.Rule) string {
	if r.Name == "" {
		return "(unnamed)"
	}
	return r.Name
}

// ruleCriteria summarises what a rule matches on, for "rules list" and the
// confirmation printed by "rules add"/"rules remove".
func ruleCriteria(r config.Rule) string {
	var parts []string
	if len(r.ConversationIDs) > 0 {
		parts = append(parts, "conversation_ids="+strings.Join(r.ConversationIDs, ","))
	}
	if len(r.Phones) > 0 {
		parts = append(parts, "phones="+strings.Join(r.Phones, ","))
	}
	if r.NamePattern != "" {
		parts = append(parts, fmt.Sprintf("name_pattern=%q", r.NamePattern))
	}
	if r.GroupsOnly {
		parts = append(parts, "groups_only")
	}
	if r.DirectsOnly {
		parts = append(parts, "directs_only")
	}
	if len(parts) == 0 {
		return "(no criteria)"
	}
	return strings.Join(parts, " ")
}

// destinationFromFlags builds a config.Destination from exactly one of
// "rules add"'s mutually exclusive --to-* flags. Beyond that exclusivity
// check, it deliberately does not duplicate config.Destination.Validate's
// rules (a channel destination needing both team and channel, a group
// needing 2-7 members, and so on) — config.Mutate runs the result back
// through Load, which reports those the same way "config check" would.
func destinationFromFlags(channel, channelID, user string, users []string) (config.Destination, error) {
	set := 0
	for _, given := range []bool{channel != "", channelID != "", user != "", len(users) > 0} {
		if given {
			set++
		}
	}
	switch {
	case set == 0:
		return config.Destination{}, errors.New(
			"a destination is required: one of --to-channel, --to-channel-id, --to-user, --to-users")
	case set > 1:
		return config.Destination{}, errors.New(
			"only one of --to-channel, --to-channel-id, --to-user, --to-users may be given")
	}

	switch {
	case channel != "":
		team, ch, ok := strings.Cut(channel, "/")
		if !ok || team == "" || ch == "" {
			return config.Destination{}, fmt.Errorf("--to-channel must be TEAM/CHANNEL, got %q", channel)
		}
		return config.Destination{Type: config.DestChannel, Team: team, Channel: ch}, nil
	case channelID != "":
		return config.Destination{Type: config.DestChannelID, ChannelID: channelID}, nil
	case user != "":
		return config.Destination{Type: config.DestDirect, User: user}, nil
	default:
		return config.Destination{Type: config.DestGroup, Users: users}, nil
	}
}

var (
	rulesRemote             string
	rulesToken              string
	rulesTokenFile          string
	rulesInsecureSkipVerify bool
	rulesFile               string
)

var rulesPullCmd = &cobra.Command{
	Use:   "pull NAME",
	Short: "Fetch a user's routing rules from a remote daemon",
	Long: `Fetch the routing rules for the user named NAME from a daemon over the
network — see docs/RUNNING.md, "Remote rules management" — and print them as
JSON, so they can be edited and pushed back with "msg-gw rules push".

This is "client mode": nothing here reads or writes local config.json.
--remote needs a bearer token for that user, set on the daemon side under
that user's remote_rules.token_ref; pass it here with --token, --token-file,
or the MSGGW_RULES_TOKEN environment variable.`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		token, err := resolveRulesToken()
		if err != nil {
			return err
		}
		client := rulesclient.New(rulesRemote, token, rulesInsecureSkipVerify)

		getCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		doc, err := client.Get(getCtx, args[0])
		if err != nil {
			return fmt.Errorf("fetching rules for %s: %w", args[0], err)
		}

		data, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return fmt.Errorf("encoding rules: %w", err)
		}
		data = append(data, '\n')

		if rulesFile == "" {
			_, err = cmd.OutOrStdout().Write(data)
			return err
		}
		if err := os.WriteFile(rulesFile, data, 0o600); err != nil {
			return fmt.Errorf("writing --file: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s's current routing rules to %s.\n", args[0], rulesFile)
		return nil
	},
}

var rulesPushCmd = &cobra.Command{
	Use:   "push NAME",
	Short: "Replace a user's routing rules on a remote daemon",
	Long: `Replace the routing rules for the user named NAME on a daemon over the
network — see docs/RUNNING.md, "Remote rules management" — with the rules
read from --file (as produced by "msg-gw rules pull"). This is a full
replacement of routing.rules, and only routing.rules: whatever rules the
daemon currently has for NAME are discarded in favor of --file's, which is
why "pull" before "push" matters — recreate rules from the daemon's own
output, not from memory.

default_direct and default_group are not affected by this command at all,
even though --file (as "pull" writes it) includes them for context: they
are operator-set, so the fallback destination that guarantees message
delivery when nothing in "rules" matches can never be changed by a push.

The rules are validated locally first, with the same checks the daemon
itself applies, so an obvious mistake is caught before the round trip. The
daemon then validates them again, saves them, and reloads before
responding, so this command's result is an honest pass/fail: config.json
(on the daemon's host) was written to. When the daemon has to revert to its
previous configuration, on reload failure, the rules are still saved — they
will take effect on the next successful reload — but this is reported as an
error regardless, since they are not live yet.

--remote needs a bearer token for that user, set on the daemon side under
that user's remote_rules.token_ref; pass it here with --token, --token-file,
or the MSGGW_RULES_TOKEN environment variable.`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		if rulesFile == "" {
			return errors.New("--file is required: the rules to push (see \"msg-gw rules pull\")")
		}
		data, err := os.ReadFile(rulesFile)
		if err != nil {
			return fmt.Errorf("reading --file: %w", err)
		}
		var doc rulesproto.RulesDocument
		if err := json.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("parsing --file as JSON: %w", err)
		}
		if err := config.ValidateRuleList(doc.Rules); err != nil {
			return fmt.Errorf("%s is not valid: %w", rulesFile, err)
		}

		token, err := resolveRulesToken()
		if err != nil {
			return err
		}
		client := rulesclient.New(rulesRemote, token, rulesInsecureSkipVerify)

		result, err := client.Put(ctx, args[0], doc.Rules)
		if err != nil {
			return fmt.Errorf("pushing rules for %s: %w", args[0], err)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "Pushed and applied %d rule(s) for %s.\n", len(result.Rules), args[0])
		return nil
	},
}

// resolveRulesToken finds the bearer token for --remote, in the order the
// command's help text documents: the flag, then a file, then the
// environment — the same precedence resolvePairToken uses.
func resolveRulesToken() (string, error) {
	if rulesToken != "" {
		return rulesToken, nil
	}
	if rulesTokenFile != "" {
		data, err := os.ReadFile(rulesTokenFile)
		if err != nil {
			return "", fmt.Errorf("reading --token-file: %w", err)
		}
		return strings.TrimSpace(string(data)), nil
	}
	if token := os.Getenv("MSGGW_RULES_TOKEN"); token != "" {
		return token, nil
	}
	return "", fmt.Errorf("--remote needs a token: pass --token, --token-file, or set MSGGW_RULES_TOKEN")
}

func addRulesRemoteFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&rulesRemote, "remote", "", "the daemon to talk to over the network (e.g. https://msggw.example.net:8443) — required")
	cmd.Flags().StringVar(&rulesToken, "token", "", "bearer token for --remote (or use --token-file / MSGGW_RULES_TOKEN)")
	cmd.Flags().StringVar(&rulesTokenFile, "token-file", "", "file containing the bearer token for --remote")
	cmd.Flags().BoolVar(&rulesInsecureSkipVerify, "insecure-skip-verify", false, "skip TLS certificate verification for --remote (testing only)")
	_ = cmd.MarkFlagRequired("remote")
}

func init() {
	addRulesRemoteFlags(rulesPullCmd)
	rulesPullCmd.Flags().StringVar(&rulesFile, "file", "", "write the fetched document here instead of stdout")

	addRulesRemoteFlags(rulesPushCmd)
	rulesPushCmd.Flags().StringVar(&rulesFile, "file", "", "the document to push, as produced by \"rules pull\" (required)")

	rulesCmd.AddCommand(rulesPullCmd, rulesPushCmd)
}

func init() {
	rulesAddCmd.Flags().StringVar(&ruleAddName, "name", "", "a label for this rule, shown in logs and \"rules list\" (has no effect on matching)")
	rulesAddCmd.Flags().StringSliceVar(&ruleAddConvIDs, "conversation-id", nil, "match a Google Messages conversation ID exactly (repeatable)")
	rulesAddCmd.Flags().StringSliceVar(&ruleAddPhones, "phone", nil, "match any participant's phone number (repeatable)")
	rulesAddCmd.Flags().StringVar(&ruleAddNamePattern, "name-pattern", "", "match the conversation's display name against this regular expression")
	rulesAddCmd.Flags().BoolVar(&ruleAddGroupsOnly, "groups-only", false, "only match group conversations")
	rulesAddCmd.Flags().BoolVar(&ruleAddDirectsOnly, "directs-only", false, "only match one-to-one conversations")
	rulesAddCmd.Flags().StringVar(&ruleAddToChannel, "to-channel", "", "destination: a named channel, as TEAM/CHANNEL")
	rulesAddCmd.Flags().StringVar(&ruleAddToChannelID, "to-channel-id", "", "destination: a channel by its raw ID")
	rulesAddCmd.Flags().StringVar(&ruleAddToUser, "to-user", "", "destination: a direct message with this Mattermost username")
	rulesAddCmd.Flags().StringSliceVar(&ruleAddToUsers, "to-users", nil, "destination: a group message with these Mattermost usernames (2-7, repeatable or comma-separated)")

	rulesCmd.AddCommand(rulesListCmd, rulesAddCmd, rulesRemoveCmd)
}
