// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 20:08:16
// Original filename: src/cmd/common.go

package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"msggw/internal/config"
	"msggw/internal/gmessages"
	"msggw/internal/mattermost"
	"msggw/internal/secrets"
	"msggw/internal/storage"
)

// loadConfig reads the configuration named by --config, or the default paths.
func loadConfig() (*config.Config, error) {
	return config.Load(configPath)
}

// newLogger builds the daemon's logger. Logs go to stderr so that stdout stays
// available for command output such as the sample configuration.
func newLogger(cfg *config.Config) *slog.Logger {
	return cfg.NewLogger(os.Stderr)
}

// newSessionStore resolves the Google Messages session reference.
func newSessionStore(cfg *config.Config) (*gmessages.SessionStore, error) {
	store, err := secrets.Open(cfg.GMessages.SessionRef, cfg.Vault)
	if err != nil {
		return nil, fmt.Errorf("gmessages.session_ref: %w", err)
	}
	return gmessages.NewSessionStore(store), nil
}

// newGMessagesConfig assembles what the Google Messages client needs.
func newGMessagesConfig(cfg *config.Config, log *slog.Logger) (gmessages.Config, error) {
	session, err := newSessionStore(cfg)
	if err != nil {
		return gmessages.Config{}, err
	}
	return gmessages.Config{
		Session:      session,
		Logger:       log,
		LogLevel:     cfg.SlogLevel(),
		PingInterval: cfg.PingInterval(),
		ForceRCS:     cfg.GMessages.ForceRCS,
	}, nil
}

// newMattermost builds and authenticates the Mattermost client.
func newMattermost(ctx context.Context, cfg *config.Config, log *slog.Logger) (*mattermost.Client, error) {
	token, err := secrets.OpenString(cfg.Mattermost.TokenRef, cfg.Vault)
	if err != nil {
		return nil, fmt.Errorf("mattermost.token_ref: %w", err)
	}
	url, err := cfg.MattermostURL()
	if err != nil {
		return nil, err
	}

	client, err := mattermost.New(mattermost.Config{
		URL:                url,
		Token:              token,
		InsecureSkipVerify: cfg.Mattermost.InsecureSkipVerify,
		RequestTimeout:     cfg.RequestTimeout(),
		ReconnectBackoff:   cfg.ReconnectBackoff(),
		JoinChannels:       cfg.Routing.JoinChannels,
		Vault:              cfg.Vault,
		Logger:             log,
	})
	if err != nil {
		return nil, err
	}
	if err := client.Connect(ctx); err != nil {
		return nil, err
	}
	return client, nil
}

// openStorage opens the configured storage backend: SQLite by default, or
// PostgreSQL when cfg.Backend.Driver is set to config.DatabaseDriverPostgres.
func openStorage(ctx context.Context, cfg *config.Config) (*storage.DB, error) {
	if cfg.Backend.Driver == config.DatabaseDriverPostgres {
		dsn, err := secrets.OpenString(cfg.Backend.Postgres.DSNRef, cfg.Vault)
		if err != nil {
			return nil, fmt.Errorf("backend.postgres.dsn_ref: %w", err)
		}
		return storage.OpenPostgres(ctx, dsn)
	}
	path, err := cfg.SQLitePath()
	if err != nil {
		return nil, err
	}
	return storage.Open(ctx, path)
}
