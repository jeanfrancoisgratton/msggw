// rcs_gateway
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 19:42:41
// Original filename: src/internal/secrets/stores.go

package secrets

import (
	"crypto/aes"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	hf "github.com/jeanfrancoisgratton/helperFunctions/v5"
	"github.com/jeanfrancoisgratton/vaultlib/v2/kv"
)

// ---------------------------------------------------------------------------
// env:
// ---------------------------------------------------------------------------

type envStore struct {
	name string
}

func (s *envStore) Load() ([]byte, error) {
	value, ok := os.LookupEnv(s.name)
	if !ok {
		return nil, ErrNotFound
	}
	return []byte(value), nil
}

func (s *envStore) Save([]byte) error {
	// Writing to our own environment would not survive the process, and the
	// Google Messages session has to. Refusing loudly beats a session that
	// silently has to be re-paired on every restart.
	return fmt.Errorf("%w: env:%s", ErrReadOnly, s.name)
}

func (s *envStore) Describe() string { return "env:" + s.name }

// ---------------------------------------------------------------------------
// literal:
// ---------------------------------------------------------------------------

type literalStore struct {
	value string
}

func (s *literalStore) Load() ([]byte, error) { return []byte(s.value), nil }

func (s *literalStore) Save([]byte) error {
	return fmt.Errorf("%w: a literal: secret is part of the config file", ErrReadOnly)
}

func (s *literalStore) Describe() string { return "literal:(inline)" }

// ---------------------------------------------------------------------------
// file:
// ---------------------------------------------------------------------------

type fileStore struct {
	path string
}

func (s *fileStore) Load() ([]byte, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := checkPermissions(s.path); err != nil {
		return nil, err
	}
	return data, nil
}

func (s *fileStore) Save(value []byte) error { return writeFile(s.path, value) }

func (s *fileStore) Describe() string { return "file:" + s.path }

// ---------------------------------------------------------------------------
// encoded:
// ---------------------------------------------------------------------------

// encodedFileStore holds a secret encoded with helperFunctions' EncodeString.
// Unlike the file-to-file EncodeFile/DecodeFile pair, the string variants work
// in memory, so the plaintext never touches the disk.
//
// The encoding is AES-256-CFB, which is unauthenticated: a wrong passphrase
// yields plausible-looking garbage rather than an error. Callers therefore
// have to validate what comes back — the session store parses it as JSON,
// which catches the case in practice.
type encodedFileStore struct {
	path string
	// passphraseVar is the environment variable holding the passphrase. When
	// empty the passphrase is the empty string, matching how the other tools
	// in this family encode their key material.
	passphraseVar string
}

func (s *encodedFileStore) passphrase() string {
	if s.passphraseVar == "" {
		return ""
	}
	return os.Getenv(s.passphraseVar)
}

func (s *encodedFileStore) Load() ([]byte, error) {
	encoded, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := checkPermissions(s.path); err != nil {
		return nil, err
	}

	trimmed := strings.TrimSpace(string(encoded))
	if !isBase64([]byte(trimmed)) {
		return nil, fmt.Errorf("%s is not a helperFunctions-encoded file: contents are not base64", s.path)
	}
	// DecodeString panics on a ciphertext shorter than one AES block, so that
	// case is rejected here rather than being allowed to reach it.
	raw, _ := base64.StdEncoding.DecodeString(trimmed)
	if len(raw) < aes.BlockSize {
		return nil, fmt.Errorf("%s is truncated: %d bytes of ciphertext, minimum is %d",
			s.path, len(raw), aes.BlockSize)
	}

	return []byte(hf.DecodeString(trimmed, s.passphrase())), nil
}

func (s *encodedFileStore) Save(value []byte) error {
	return writeFile(s.path, []byte(hf.EncodeString(string(value), s.passphrase())))
}

func (s *encodedFileStore) Describe() string { return "encoded:" + s.path }

// ---------------------------------------------------------------------------
// vault:
// ---------------------------------------------------------------------------

type vaultStore struct {
	cfg   VaultConfig
	mount string
	path  string
	field string
}

// resolve builds the vaultLib config, reading the Vault token through the same
// reference machinery so that it too can live outside the config file. A
// vault: token reference would be circular and is rejected.
func (s *vaultStore) resolve() (kv.Config, error) {
	var token string
	if s.cfg.TokenRef != "" {
		if strings.HasPrefix(s.cfg.TokenRef, schemeVault+":") {
			return kv.Config{}, errors.New("vault.token_ref may not itself be a vault: reference")
		}
		var err error
		token, err = OpenString(s.cfg.TokenRef, VaultConfig{})
		if err != nil {
			return kv.Config{}, fmt.Errorf("reading Vault token: %w", err)
		}
	}
	// An empty token is left to vaultLib, which falls back to VAULT_TOKEN and
	// then to ~/.vault-token.
	return s.cfg.toShared(s.mount, token), nil
}

func (s *vaultStore) Load() ([]byte, error) {
	cfg, err := s.resolve()
	if err != nil {
		return nil, err
	}
	value, err := kv.ReadSecretField(cfg, s.path, s.field, 0)
	if err != nil {
		// vaultLib reports a missing secret and a missing field as errors, and
		// does not distinguish them from transport failures. Treating "not
		// found" as ErrNotFound here would risk re-pairing on a Vault outage,
		// so the error is passed through as-is.
		return nil, err
	}
	str, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("vault field %s/%s#%s is %T, expected a string",
			s.mount, s.path, s.field, value)
	}
	return []byte(str), nil
}

func (s *vaultStore) Save(value []byte) error {
	cfg, err := s.resolve()
	if err != nil {
		return err
	}
	// UpdateSecretField patches the one field, leaving anything else stored
	// under the same path untouched.
	_, err = kv.UpdateSecretField(cfg, s.path, s.field, string(value))
	return err
}

func (s *vaultStore) Describe() string {
	return fmt.Sprintf("vault:%s/%s#%s", s.mount, s.path, s.field)
}

// ---------------------------------------------------------------------------
// shared helpers
// ---------------------------------------------------------------------------

// checkPermissions refuses to read a secret out of a file that anyone other
// than its owner can read.
func checkPermissions(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if mode := fi.Mode().Perm(); mode&0o077 != 0 {
		return fmt.Errorf("%s is mode %#o: a secret file must not be readable by group or other (chmod 0600 %s)",
			path, mode, path)
	}
	return nil
}

// writeFile writes a secret atomically and with restrictive permissions. The
// rename means a crash mid-write leaves the previous session intact instead of
// a truncated one, which would cost a re-pairing.
func writeFile(path string, value []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("creating temporary file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("setting mode on %s: %w", tmpName, err)
	}
	if _, err := tmp.Write(value); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("syncing %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("renaming %s to %s: %w", tmpName, path, err)
	}
	return nil
}
