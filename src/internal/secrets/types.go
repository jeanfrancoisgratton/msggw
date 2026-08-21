// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 19:41:45
// Original filename: src/internal/secrets/types.go

package secrets

import (
	"errors"

	vaultshared "github.com/jeanfrancoisgratton/vaultlib/v2/shared"
)

// ErrReadOnly is returned by Save on a store that cannot be written back to,
// such as one backed by an environment variable.
var ErrReadOnly = errors.New("secret store is read-only")

// ErrNotFound is returned by Load when the reference resolves correctly but
// the secret itself does not exist yet. Callers that create secrets (the
// pairing flow writing its first session) treat this as "nothing stored yet"
// rather than as a failure.
var ErrNotFound = errors.New("secret not found")

// Store is a place a secret is read from, and — for every backend except the
// environment — written back to.
//
// The daemon needs write access because the Google Messages session is not a
// static credential: libgm refreshes its Tachyon auth token roughly hourly and
// the refreshed AuthData has to be persisted, or a restart re-pairs from
// scratch.
type Store interface {
	// Load returns the secret's current value, or ErrNotFound.
	Load() ([]byte, error)
	// Save replaces the secret's value, or returns ErrReadOnly.
	Save(value []byte) error
	// Describe returns a loggable description that never contains the secret.
	Describe() string
}

// VaultConfig is the Vault connection used by vault: references. Every field
// is optional: vaultLib falls back to the usual VAULT_* environment variables
// for anything left empty.
type VaultConfig struct {
	Address string `json:"address,omitempty"`
	// TokenRef is itself a secret reference, so that the Vault token can come
	// from a file or the environment rather than sitting in the config file.
	// It may not itself be a vault: reference.
	TokenRef  string `json:"token_ref,omitempty"`
	Namespace string `json:"namespace,omitempty"`

	CACertPath     string `json:"ca_cert_path,omitempty"`
	ClientCertPath string `json:"client_cert_path,omitempty"`
	ClientKeyPath  string `json:"client_key_path,omitempty"`
	TLSSkipVerify  bool   `json:"tls_skip_verify,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

// toShared converts to vaultLib's config, with the mount and token filled in
// by the caller.
func (v VaultConfig) toShared(mount, token string) vaultshared.Config {
	return vaultshared.Config{
		Address:        v.Address,
		Token:          token,
		MountPath:      mount,
		Namespace:      v.Namespace,
		CACertPath:     v.CACertPath,
		ClientCertPath: v.ClientCertPath,
		ClientKeyPath:  v.ClientKeyPath,
		TLSSkipVerify:  v.TLSSkipVerify,
		TimeoutSeconds: v.TimeoutSeconds,
	}
}
