// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 19:42:07
// Original filename: src/internal/secrets/secrets.go

// Package secrets resolves the daemon's credentials — the Mattermost bot
// token, the Google Messages session, an optional webhook URL — from whichever
// backend the operator prefers, without any of them being hard-coded into the
// daemon.
//
// Every credential in the configuration file is a *reference* rather than a
// value, written as "<scheme>:<location>":
//
//	env:MM_BOT_TOKEN                      environment variable (read-only)
//	file:/etc/msggw/mm.token        plain file, must be mode 0600 or tighter
//	encoded:/etc/msggw/mm.token.enc file encoded with helperFunctions
//	encoded:/path/to/file#PASSPHRASE_VAR  ... with the passphrase in $PASSPHRASE_VAR
//	vault:secrets/msggw#bot_token   HashiCorp Vault KV, via vaultLib
//	literal:xoxb-not-a-real-token         inline in the config file (discouraged)
//
// A reference with no recognised scheme is rejected rather than guessed at, so
// that a typo cannot silently degrade into a literal secret in a world-readable
// config file.
package secrets

import (
	"encoding/base64"
	"fmt"
	"strings"
)

const (
	schemeEnv     = "env"
	schemeFile    = "file"
	schemeEncoded = "encoded"
	schemeVault   = "vault"
	schemeLiteral = "literal"
)

// Open resolves a reference to a Store. It does not read the secret: a bad
// path or an unreachable Vault surfaces on the first Load, so that a daemon
// with several credentials reports one clear error per credential.
func Open(ref string, vaultCfg VaultConfig) (Store, error) {
	scheme, location, ok := strings.Cut(ref, ":")
	if !ok {
		return nil, fmt.Errorf("secret reference %q has no scheme: expected one of env:, file:, encoded:, vault:, literal:", ref)
	}
	if location == "" {
		return nil, fmt.Errorf("secret reference %q has an empty location", ref)
	}

	switch scheme {
	case schemeEnv:
		return &envStore{name: location}, nil

	case schemeFile:
		return &fileStore{path: location}, nil

	case schemeEncoded:
		path, passphraseVar, _ := strings.Cut(location, "#")
		if path == "" {
			return nil, fmt.Errorf("secret reference %q has an empty path", ref)
		}
		return &encodedFileStore{path: path, passphraseVar: passphraseVar}, nil

	case schemeVault:
		mount, rest, ok := strings.Cut(location, "/")
		if !ok || mount == "" || rest == "" {
			return nil, fmt.Errorf("vault reference %q must be vault:<mount>/<path>#<field>", ref)
		}
		path, field, ok := strings.Cut(rest, "#")
		if !ok || path == "" || field == "" {
			return nil, fmt.Errorf("vault reference %q must be vault:<mount>/<path>#<field>", ref)
		}
		return &vaultStore{cfg: vaultCfg, mount: mount, path: path, field: field}, nil

	case schemeLiteral:
		return &literalStore{value: location}, nil

	default:
		return nil, fmt.Errorf("unknown secret scheme %q in reference %q", scheme, ref)
	}
}

// OpenString is Open plus a Load, for the common case of a credential that is
// read once at startup and never written back. Surrounding whitespace is
// trimmed, since a token in a file almost always ends with a newline.
func OpenString(ref string, vaultCfg VaultConfig) (string, error) {
	store, err := Open(ref, vaultCfg)
	if err != nil {
		return "", err
	}
	value, err := store.Load()
	if err != nil {
		return "", fmt.Errorf("reading secret from %s: %w", store.Describe(), err)
	}
	return strings.TrimSpace(string(value)), nil
}

// LooksLikeReference reports whether value's prefix, up to the first ':',
// names one of the recognised schemes. It lets a field that is usually a
// plain value — a URL, a filesystem path, a Mattermost username — also be
// sourced from Vault, a file or the environment, without requiring every
// plain value to be wrapped in "literal:".
func LooksLikeReference(value string) bool {
	scheme, _, ok := strings.Cut(value, ":")
	if !ok {
		return false
	}
	switch scheme {
	case schemeEnv, schemeFile, schemeEncoded, schemeVault, schemeLiteral:
		return true
	default:
		return false
	}
}

// MaybeResolve resolves value through OpenString when it looks like a
// reference, and returns it unchanged otherwise.
func MaybeResolve(value string, vaultCfg VaultConfig) (string, error) {
	if !LooksLikeReference(value) {
		return value, nil
	}
	return OpenString(value, vaultCfg)
}

// isBase64 reports whether b is valid standard base64. helperFunctions'
// DecodeString panics on malformed input instead of returning an error, so
// encodedFileStore checks the encoding itself before handing anything over.
func isBase64(b []byte) bool {
	_, err := base64.StdEncoding.DecodeString(string(b))
	return err == nil
}
