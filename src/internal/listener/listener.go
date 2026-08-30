// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.23 00:00:00
// Original filename: src/internal/listener/listener.go

// Package listener runs the daemon's HTTP(S) listener.
//
// It exists for "client mode" pairing (see docs/MULTI-TENANCY.md): a client
// running on the operator's own device, not this host, registers pairing
// material with the daemon instead of that material ever touching this
// host's own network fingerprint at Google's login page. This package only
// knows how to stand the listener up correctly — what it serves is wired in
// by the caller.
package listener

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	stdlog "log"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// shutdownGrace bounds how long a graceful shutdown waits for in-flight
// requests before Run gives up and returns.
const shutdownGrace = 5 * time.Second

// Config is what a Listener needs. It mirrors config.ListenerConfig field for
// field so that package does not have to import this one.
type Config struct {
	// Port is what the listener binds to, on all interfaces.
	Port int
	// CertFile and KeyFile are the TLS certificate and private key. Empty, or
	// unusable, and the listener falls back to plain HTTP — see New.
	CertFile string
	KeyFile  string
}

// Listener serves handler on Config.Port, in TLS when a usable certificate
// was configured.
type Listener struct {
	srv *http.Server
	log *slog.Logger
	tls bool
}

// New builds a Listener. It does not start serving; call Run for that.
//
// TLS is used when CertFile and KeyFile both load into a valid key pair.
// Otherwise — unset, unreadable, or mismatched — the listener falls back to
// plain HTTP rather than refusing to start, but says so at warn level: this
// is a client registering pairing cookies, so serving that unencrypted is
// consequential enough that it must never happen quietly.
func New(cfg Config, handler http.Handler, log *slog.Logger) (*Listener, error) {
	if cfg.Port <= 0 {
		return nil, fmt.Errorf("listener: port must be set")
	}
	if log == nil {
		log = slog.Default()
	}

	addr := fmt.Sprintf(":%d", cfg.Port)
	srv := &http.Server{
		Addr:    addr,
		Handler: handler,
		// http.Server otherwise logs things like bad TLS handshakes straight
		// to stderr through the stdlib log package, bypassing whatever the
		// daemon's own logger is configured to do (including log.format:
		// json) — the same reason libgm's own output is routed into slog
		// rather than left to print on its own.
		ErrorLog: stdlog.New(errorLogWriter{log: log}, "", 0),
	}
	l := &Listener{srv: srv, log: log}

	switch {
	case cfg.CertFile == "" && cfg.KeyFile == "":
		log.Warn("listener starting without TLS: no cert_file/key_file configured — traffic on this port is unencrypted",
			"addr", addr)
	case cfg.CertFile == "" || cfg.KeyFile == "":
		log.Warn("listener starting without TLS: cert_file and key_file must both be set — traffic on this port is unencrypted",
			"addr", addr, "cert_file", cfg.CertFile, "key_file", cfg.KeyFile)
	default:
		cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			log.Warn("listener falling back to plain HTTP: the TLS certificate could not be loaded — traffic on this port is unencrypted",
				"addr", addr, "cert_file", cfg.CertFile, "key_file", cfg.KeyFile, "error", err)
		} else {
			srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
			l.tls = true
		}
	}

	return l, nil
}

// Addr is the address Run listens on.
func (l *Listener) Addr() string { return l.srv.Addr }

// TLS reports whether Run will actually serve TLS, after New's fallback
// decision.
func (l *Listener) TLS() bool { return l.tls }

// Run serves until ctx is cancelled, then shuts down gracefully. It returns
// nil on a clean shutdown, and any error the listener itself hit otherwise.
func (l *Listener) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		var err error
		if l.tls {
			l.log.Info("listener started", "addr", l.srv.Addr, "tls", true)
			// Certificates already sit in srv.TLSConfig, so no cert/key path
			// is needed here.
			err = l.srv.ListenAndServeTLS("", "")
		} else {
			l.log.Info("listener started", "addr", l.srv.Addr, "tls", false)
			err = l.srv.ListenAndServe()
		}
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := l.srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutting down the listener: %w", err)
		}
		return <-errCh
	}
}

// errorLogWriter adapts http.Server.ErrorLog into the daemon's own logger.
type errorLogWriter struct{ log *slog.Logger }

func (w errorLogWriter) Write(p []byte) (int, error) {
	w.log.Warn("listener", "line", strings.TrimSpace(string(p)))
	return len(p), nil
}

// DefaultHandler is the listener's base handler: a health check, plus
// whatever else the caller mounts onto the returned mux (client-mode
// pairing's routes — see cmd/pairserver.go and docs/MULTI-TENANCY.md).
func DefaultHandler() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})
	return mux
}
