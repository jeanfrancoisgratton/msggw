// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.17 00:00:00
// Original filename: src/internal/storage/postgres.go

// PostgreSQL backend, selected by setting config.Config.DatabaseDriver to
// config.DatabaseDriverPostgres; see openStorage in src/cmd/common.go, which
// picks between this and Open in sqlite.go.
//
// The query code in conversations.go and messages.go works against either
// backend unmodified: every query is written with "?" placeholders and goes
// through DB.execContext/queryContext/queryRowContext (or the Tx
// equivalents), which rebind "?" to PostgreSQL's "$1, $2, ..." style when
// db.backend is BackendPostgres. See DB.rebind in db.go.
package storage

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// OpenPostgres opens a PostgreSQL database at dsn and brings its schema up to
// date. It mirrors Open in sqlite.go; the two differ only where the backends
// themselves differ (connection pooling, migration bookkeeping).
func OpenPostgres(ctx context.Context, dsn string) (*DB, error) {
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening postgres database: %w", err)
	}

	if err := sqlDB.PingContext(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("opening postgres database: %w", err)
	}

	db := &DB{sql: sqlDB, backend: BackendPostgres}
	if err := db.migratePostgres(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("migrating postgres database: %w", err)
	}
	return db, nil
}

// postgresMigrations mirrors sqliteMigrations table for table. Column types
// are kept identical to the SQLite schema (TEXT / INTEGER) rather than
// switched to BOOLEAN or TIMESTAMPTZ, so that the Scan targets in
// conversations.go and messages.go (bool, int32, int64 unix seconds) need no
// backend-specific cases. PostgreSQL has no equivalent of SQLite's implicit
// per-file PRAGMA user_version, so progress is tracked in a schema_migrations
// table instead (see migratePostgres) rather than by mirroring that pragma.
var postgresMigrations = []string{
	`
CREATE TABLE conversations (
    gmessages_conversation_id TEXT PRIMARY KEY,
    mattermost_channel_id     TEXT NOT NULL,
    mattermost_root_post_id   TEXT NOT NULL DEFAULT '',
    display_name              TEXT NOT NULL DEFAULT '',
    is_group                  INTEGER NOT NULL DEFAULT 0,
    outgoing_participant_id   TEXT NOT NULL DEFAULT '',
    conversation_type         INTEGER NOT NULL DEFAULT 0,
    send_mode                 INTEGER NOT NULL DEFAULT 0,
    last_seen                 INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX conversations_by_root_post ON conversations(mattermost_root_post_id);
CREATE INDEX conversations_by_channel ON conversations(mattermost_channel_id);

CREATE TABLE conversation_participants (
    gmessages_conversation_id TEXT NOT NULL
        REFERENCES conversations(gmessages_conversation_id) ON DELETE CASCADE,
    participant_id            TEXT NOT NULL,
    phone                     TEXT NOT NULL DEFAULT '',
    normalized_phone          TEXT NOT NULL DEFAULT '',
    display_name              TEXT NOT NULL DEFAULT '',
    is_me                     INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (gmessages_conversation_id, participant_id)
);

CREATE INDEX participants_by_phone ON conversation_participants(normalized_phone);

CREATE TABLE messages (
    gmessages_message_id TEXT PRIMARY KEY,
    mattermost_post_id   TEXT NOT NULL,
    conversation_id      TEXT NOT NULL,
    direction            TEXT NOT NULL,
    status               INTEGER NOT NULL DEFAULT 0,
    created_at           INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX messages_by_post ON messages(mattermost_post_id);
CREATE INDEX messages_by_conversation ON messages(conversation_id);

CREATE TABLE outbound_pending (
    tmp_id             TEXT PRIMARY KEY,
    mattermost_post_id TEXT NOT NULL,
    conversation_id    TEXT NOT NULL,
    created_at         INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
`,
}

// migratePostgres applies postgresMigrations, tracking progress in a
// schema_migrations table (PostgreSQL has nothing like SQLite's PRAGMA
// user_version) rather than mirroring sqlite.go's migrateSQLite.
func (db *DB) migratePostgres(ctx context.Context) error {
	if _, err := db.sql.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("creating schema_migrations: %w", err)
	}

	var current int
	if err := db.sql.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("reading schema version: %w", err)
	}
	if current > len(postgresMigrations) {
		return fmt.Errorf("database is at schema version %d, this build only knows up to %d: it was written by a newer msg-gw",
			current, len(postgresMigrations))
	}

	for version := current; version < len(postgresMigrations); version++ {
		tx, err := db.sql.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, postgresMigrations[version]); err != nil {
			tx.Rollback()
			return fmt.Errorf("applying migration %d: %w", version+1, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version) VALUES ($1)`, version+1); err != nil {
			tx.Rollback()
			return fmt.Errorf("recording schema version %d: %w", version+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("committing migration %d: %w", version+1, err)
		}
	}
	return nil
}
