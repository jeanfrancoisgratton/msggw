// rcs_gateway
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 20:13:19
// Original filename: src/internal/secrets/secrets_test.go

package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenRejectsBadReferences(t *testing.T) {
	for _, ref := range []string{
		"",                      // nothing at all
		"MM_TOKEN",              // no scheme: could be mistaken for a literal
		"env:",                  // no location
		"carrier-pigeon:/tmp/x", // unknown scheme
		"vault:onlymount",       // no path or field
		"vault:mount/path",      // no field
		"vault:mount/path#",     // empty field
	} {
		if _, err := Open(ref, VaultConfig{}); err == nil {
			t.Errorf("Open(%q) succeeded, want an error", ref)
		}
	}
}

func TestEnvStore(t *testing.T) {
	t.Setenv("RCS_TEST_TOKEN", "s3cret")

	store, err := Open("env:RCS_TEST_TOKEN", VaultConfig{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	value, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(value) != "s3cret" {
		t.Errorf("Load = %q, want %q", value, "s3cret")
	}

	// An environment variable cannot outlive the process, so a session must
	// never be pointed at one.
	if err := store.Save([]byte("new")); !errors.Is(err, ErrReadOnly) {
		t.Errorf("Save = %v, want ErrReadOnly", err)
	}

	// Describe must never leak the value itself.
	if strings.Contains(store.Describe(), "s3cret") {
		t.Errorf("Describe() leaks the secret: %q", store.Describe())
	}
}

func TestEnvStoreMissingIsNotFound(t *testing.T) {
	store, err := Open("env:RCS_TEST_DEFINITELY_UNSET", VaultConfig{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := store.Load(); !errors.Is(err, ErrNotFound) {
		t.Errorf("Load = %v, want ErrNotFound", err)
	}
}

func TestFileStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "token")

	store, err := Open("file:"+path, VaultConfig{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Nothing there yet: that is the state pairing starts from.
	if _, err := store.Load(); !errors.Is(err, ErrNotFound) {
		t.Errorf("Load on a missing file = %v, want ErrNotFound", err)
	}

	// Save has to create the directory too, since the state directory may not
	// exist on a first run.
	if err := store.Save([]byte("hello")); err != nil {
		t.Fatalf("Save: %v", err)
	}

	value, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(value) != "hello" {
		t.Errorf("Load = %q, want %q", value, "hello")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("a saved secret is mode %#o, want 0600", mode)
	}
}

// TestFileStoreRefusesLooseModes is the check that stops a secret from being
// read out of a world-readable file.
func TestFileStoreRefusesLooseModes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("writing the test file: %v", err)
	}

	store, err := Open("file:"+path, VaultConfig{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("a 0644 secret file was accepted")
	} else if !strings.Contains(err.Error(), "chmod") {
		t.Errorf("the error does not say how to fix it: %v", err)
	}
}

func TestEncodedFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.enc")

	store, err := Open("encoded:"+path, VaultConfig{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	payload := `{"version":1,"auth_data":{}}`
	if err := store.Save([]byte(payload)); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// The file on disk must not contain the plaintext.
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(onDisk), "auth_data") {
		t.Error("the encoded file contains plaintext")
	}

	value, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(value) != payload {
		t.Errorf("Load = %q, want %q", value, payload)
	}
}

func TestEncodedFileWithPassphrase(t *testing.T) {
	t.Setenv("RCS_TEST_PASSPHRASE", "correct horse")
	path := filepath.Join(t.TempDir(), "session.enc")

	store, err := Open("encoded:"+path+"#RCS_TEST_PASSPHRASE", VaultConfig{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.Save([]byte("payload")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	value, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(value) != "payload" {
		t.Errorf("Load = %q, want %q", value, "payload")
	}

	// The encoding is unauthenticated, so a wrong passphrase yields garbage
	// rather than an error. That is exactly why the session store parses what
	// it gets back as JSON: this test records the behaviour so the assumption
	// is not lost.
	wrong, err := Open("encoded:"+path, VaultConfig{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	garbage, err := wrong.Load()
	if err != nil {
		t.Fatalf("Load with a wrong passphrase returned an error, which the callers do not expect: %v", err)
	}
	if string(garbage) == "payload" {
		t.Error("a wrong passphrase decoded the secret correctly")
	}
}

func TestEncodedFileRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.enc")
	if err := os.WriteFile(path, []byte("this is not base64!!!"), 0o600); err != nil {
		t.Fatalf("writing the test file: %v", err)
	}

	store, err := Open("encoded:"+path, VaultConfig{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Without this check helperFunctions' DecodeString would panic instead.
	if _, err := store.Load(); err == nil {
		t.Fatal("a file that is not base64 was accepted")
	}
}

func TestEncodedFileRejectsTruncatedCiphertext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.enc")
	// Valid base64, but shorter than one AES block.
	if err := os.WriteFile(path, []byte("YWJj"), 0o600); err != nil {
		t.Fatalf("writing the test file: %v", err)
	}

	store, err := Open("encoded:"+path, VaultConfig{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("a truncated ciphertext was accepted; DecodeString would have panicked on it")
	}
}

func TestOpenString(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	// A token in a file almost always ends with a newline.
	if err := os.WriteFile(path, []byte("  xoxb-token\n"), 0o600); err != nil {
		t.Fatalf("writing the test file: %v", err)
	}

	value, err := OpenString("file:"+path, VaultConfig{})
	if err != nil {
		t.Fatalf("OpenString: %v", err)
	}
	if value != "xoxb-token" {
		t.Errorf("OpenString = %q, want the trimmed token", value)
	}
}

func TestLiteralStore(t *testing.T) {
	store, err := Open("literal:inline-token", VaultConfig{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	value, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(value) != "inline-token" {
		t.Errorf("Load = %q, want %q", value, "inline-token")
	}
	if strings.Contains(store.Describe(), "inline-token") {
		t.Errorf("Describe() leaks the secret: %q", store.Describe())
	}
}

// TestVaultTokenRefMayNotBeCircular guards the one reference that cannot be a
// Vault reference: the token used to reach Vault.
func TestVaultTokenRefMayNotBeCircular(t *testing.T) {
	store, err := Open("vault:mount/path#field", VaultConfig{
		Address:  "https://vault.invalid",
		TokenRef: "vault:mount/other#token",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("a circular Vault token reference was accepted")
	} else if !strings.Contains(err.Error(), "token_ref") {
		t.Errorf("the error does not name the offending setting: %v", err)
	}
}
