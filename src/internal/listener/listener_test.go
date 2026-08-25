// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>

package listener

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// freePort asks the OS for a currently unused TCP port.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// selfSignedCert writes a throwaway self-signed certificate and key to t's
// temp dir and returns their paths.
func selfSignedCert(t *testing.T) (certPath, keyPath string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating a test key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating a test certificate: %v", err)
	}

	dir := t.TempDir()
	certPath = filepath.Join(dir, "listener.crt")
	keyPath = filepath.Join(dir, "listener.key")

	certOut, err := os.Create(certPath)
	if err != nil {
		t.Fatalf("writing the test certificate: %v", err)
	}
	defer certOut.Close()
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatalf("encoding the test certificate: %v", err)
	}

	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshalling the test key: %v", err)
	}
	keyOut, err := os.Create(keyPath)
	if err != nil {
		t.Fatalf("writing the test key: %v", err)
	}
	defer keyOut.Close()
	if err := pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}); err != nil {
		t.Fatalf("encoding the test key: %v", err)
	}

	return certPath, keyPath
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNewRejectsNoPort(t *testing.T) {
	if _, err := New(Config{}, DefaultHandler(), testLogger()); err == nil {
		t.Fatal("New with no port: want an error, got nil")
	}
}

// runAndWait starts l in the background and blocks until its address accepts
// connections, so the caller does not have to guess a startup delay.
func runAndWait(t *testing.T, l *Listener) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- l.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("tcp", l.Addr())
		if err == nil {
			conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	return func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("Run did not return after ctx was cancelled")
		}
	}
}

func TestNoCertsFallsBackToPlainHTTP(t *testing.T) {
	l, err := New(Config{Port: freePort(t)}, DefaultHandler(), testLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if l.TLS() {
		t.Fatal("TLS() = true with no cert/key configured, want false")
	}

	stop := runAndWait(t, l)
	defer stop()

	resp, err := http.Get("http://" + l.Addr() + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz over plain HTTP: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestInvalidCertFallsBackToPlainHTTP(t *testing.T) {
	l, err := New(Config{
		Port:     freePort(t),
		CertFile: "/does/not/exist.crt",
		KeyFile:  "/does/not/exist.key",
	}, DefaultHandler(), testLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if l.TLS() {
		t.Fatal("TLS() = true with an unloadable certificate, want false")
	}
}

func TestOnlyOneOfCertOrKeyFallsBackToPlainHTTP(t *testing.T) {
	certPath, _ := selfSignedCert(t)

	l, err := New(Config{Port: freePort(t), CertFile: certPath}, DefaultHandler(), testLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if l.TLS() {
		t.Fatal("TLS() = true with only cert_file set, want false")
	}
}

func TestValidCertServesTLS(t *testing.T) {
	certPath, keyPath := selfSignedCert(t)

	l, err := New(Config{
		Port:     freePort(t),
		CertFile: certPath,
		KeyFile:  keyPath,
	}, DefaultHandler(), testLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !l.TLS() {
		t.Fatal("TLS() = false with a valid certificate, want true")
	}

	stop := runAndWait(t, l)
	defer stop()

	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	resp, err := client.Get("https://" + l.Addr() + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz over TLS: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	l, err := New(Config{Port: freePort(t)}, DefaultHandler(), testLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := net.Dial("tcp", l.Addr()); err == nil {
			conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run after cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of ctx cancellation")
	}
}
