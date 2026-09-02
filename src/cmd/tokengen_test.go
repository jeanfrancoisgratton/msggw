// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/cmd/tokengen_test.go

package cmd

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateToken(t *testing.T) {
	a, err := generateToken()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := hex.DecodeString(a)
	if err != nil {
		t.Fatalf("token %q is not hex: %v", a, err)
	}
	if len(raw) != 32 {
		t.Fatalf("got a %d-byte token, want 32 (like openssl rand -hex 32)", len(raw))
	}

	b, err := generateToken()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two calls produced the same token")
	}
}

func TestRunTokengenStdout(t *testing.T) {
	var out bytes.Buffer
	if err := runTokengen(&out, ""); err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSpace(out.String())
	if _, err := hex.DecodeString(token); err != nil {
		t.Fatalf("stdout output %q is not a bare hex token: %v", out.String(), err)
	}
}

func TestRunTokengenFileDestination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "pairing.token")

	var out bytes.Buffer
	if err := runTokengen(&out, "file:"+path); err != nil {
		t.Fatal(err)
	}

	if strings.Contains(out.String(), "\n") == false || strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("expected exactly one confirmation line, got %q", out.String())
	}
	if strings.Contains(out.String(), path) == false {
		t.Fatalf("confirmation %q does not mention the destination", out.String())
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("token file was not written: %v", err)
	}
	if _, err := hex.DecodeString(strings.TrimSpace(string(written))); err != nil {
		t.Fatalf("file contents %q are not a bare hex token: %v", written, err)
	}
	if strings.Contains(out.String(), string(written)) {
		t.Fatal("the token value leaked into the confirmation message")
	}
}

func TestRunTokengenBadDestination(t *testing.T) {
	if err := runTokengen(&bytes.Buffer{}, "nonsense-with-no-scheme"); err == nil {
		t.Fatal("expected an error for a reference with no scheme")
	}
}
