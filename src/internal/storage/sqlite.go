// rcs_gateway
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 19:45:06
// Original filename: src/internal/storage/sqlite.go

// SQLite is the wired-up backend: the expected volume is one person's text
// messages, and the driver is modernc.org/sqlite rather than mattn/go-sqlite3
// so that the daemon still builds with CGO_ENABLED=0, as src/build.sh does.
package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// schemaVersion is bumped whenever migrations below are added to.
const schemaVersion = 1

// Open opens (creating it if needed) the SQLite database at path and brings
// its schema up to date.
func Open(ctx context.Context, path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("creating %s: %w", dir, err)
		}
	}

	// WAL keeps the writer from blocking readers, and a busy timeout means a
	// concurrent "status" invocation waits rather than failing outright.
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}

	// SQLite takes one writer at a time; more connections buy nothing and only
	// turn contention into SQLITE_BUSY.
	sqlDB.SetMaxOpenConns(1)

	if err := sqlDB.PingContext(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}

	db := &DB{sql: sqlDB, backend: BackendSQLite}
	if err := db.migrateSQLite(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("migrating %s: %w", path, err)
	}
	return db, nil
}

// sqliteMigrations are applied in order; each one moves the schema from
// version i to version i+1. They are never edited once released, only
// appended to.
var sqliteMigrations = []string{
	`
CREATE TABLE conversations (
    gmessages_conversation_id TEXT PRIMARY KEY,
    mattermost_channel_id     TEXT NOT NULL,
    -- Empty when routing.thread_per_conversation is off: there is then no
    -- single root post standing for the conversation.
    mattermost_root_post_id   TEXT NOT NULL DEFAULT '',
    display_name              TEXT NOT NULL DEFAULT '',
    is_group                  INTEGER NOT NULL DEFAULT 0,
    -- The participant ID the phone expects as the sender of outgoing messages
    -- in this conversation. It is per-conversation because a dual-SIM phone
    -- has a different one per SIM.
    outgoing_participant_id   TEXT NOT NULL DEFAULT '',
    conversation_type         INTEGER NOT NULL DEFAULT 0,
    send_mode                 INTEGER NOT NULL DEFAULT 0,
    last_seen                 INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX conversations_by_root_post ON conversations(mattermost_root_post_id);
CREATE INDEX conversations_by_channel ON conversations(mattermost_channel_id);

-- A conversation is not one phone number: a group RCS chat has several, and a
-- contact can move between numbers. Keeping them in their own table avoids the
-- one-number assumption docs/SOLUTION.md warns about.
CREATE TABLE conversation_participants (
    gmessages_conversation_id TEXT NOT NULL
        REFERENCES conversations(gmessages_conversation_id) ON DELETE CASCADE,
    participant_id            TEXT NOT NULL,
    phone                     TEXT NOT NULL DEFAULT '',
    -- phone with spaces, dashes, dots and parentheses removed, for matching.
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
    -- 'in' for phone to Mattermost, 'out' for Mattermost to phone.
    direction            TEXT NOT NULL,
    -- Last gmproto.MessageStatusType seen, so that a repeated status update
    -- does not re-decorate the post.
    status               INTEGER NOT NULL DEFAULT 0,
    created_at           INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX messages_by_post ON messages(mattermost_post_id);
CREATE INDEX messages_by_conversation ON messages(conversation_id);

-- A message sent to the phone is acknowledged with a status, not with its
-- final ID: that arrives later as an ordinary message event carrying the tmpID
-- we chose. Until then the mapping lives here, which is also what stops the
-- bridge from posting our own message back into Mattermost as if it were new.
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

// migrateSQLite applies sqliteMigrations, tracking progress in SQLite's own
// PRAGMA user_version rather than a table, so that migration 0 has somewhere
// to record itself before any table exists.
func (db *DB) migrateSQLite(ctx context.Context) error {
	var current int
	if err := db.sql.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&current); err != nil {
		return fmt.Errorf("reading schema version: %w", err)
	}
	if current > len(sqliteMigrations) {
		return fmt.Errorf("database is at schema version %d, this build only knows up to %d: it was written by a newer rcs-mm_gw",
			current, schemaVersion)
	}

	for version := current; version < len(sqliteMigrations); version++ {
		tx, err := db.sql.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, sqliteMigrations[version]); err != nil {
			tx.Rollback()
			return fmt.Errorf("applying migration %d: %w", version+1, err)
		}
		// PRAGMA user_version does not accept a bound parameter.
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version+1)); err != nil {
			tx.Rollback()
			return fmt.Errorf("recording schema version %d: %w", version+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("committing migration %d: %w", version+1, err)
		}
	}
	return nil
}
