// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.09.01 00:00:00
// Original filename: src/cmd/tokengen.go

package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"msggw/internal/secrets"
)

var tokengenCmd = &cobra.Command{
	Use:   "tokengen [destination]",
	Short: "Generate a random bearer token",
	Long: `Generate a random bearer token, the same way "openssl rand -hex 32" would,
for use as a user's remote_pairing.token_ref or remote_rules.token_ref.

With no argument, the token is printed to standard output and nothing else is
written. With a destination, the token is instead saved there and only a
confirmation is printed — never the token itself. The destination is a secret
reference in the same "<scheme>:<location>" form used everywhere else in the
configuration (see "msg-gw config sample"):

  msg-gw tokengen                                       # print to stdout
  msg-gw tokengen file:/etc/msggw/secrets/pairing.token
  msg-gw tokengen vault:secrets/msggw#pairing_token

Whichever form you use, the same string belongs in that user's
remote_pairing.token_ref or remote_rules.token_ref in config.json.`,
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		var dest string
		if len(args) > 0 {
			dest = args[0]
		}
		return runTokengen(cmd.OutOrStdout(), dest)
	},
}

// runTokengen generates a token and either prints it to out (dest empty) or
// saves it to dest and prints a confirmation. It is split out from RunE so it
// can be tested without going through cobra.
func runTokengen(out io.Writer, dest string) error {
	token, err := generateToken()
	if err != nil {
		return err
	}

	if dest == "" {
		_, err := fmt.Fprintln(out, token)
		return err
	}

	store, err := secrets.Open(dest, secrets.VaultConfig{})
	if err != nil {
		return err
	}
	if err := store.Save([]byte(token)); err != nil {
		return fmt.Errorf("writing token to %s: %w", store.Describe(), err)
	}
	_, err = fmt.Fprintf(out, "Wrote a new token to %s.\n", store.Describe())
	return err
}

// generateToken returns a random 32-byte token, hex-encoded — the same size
// "openssl rand -hex 32" produces, since that is what the docs already tell
// operators to run by hand.
func generateToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating random token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}
