// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.17 00:00:00
// Original filename: src/internal/storage/db.go

// Package storage keeps the bridge's mappings: which Mattermost thread stands
// for which Google Messages conversation, and which post stands for which
// message.
//
// Two backends are available: SQLite (sqlite.go), the default and the one
// exercised against real message volume, and PostgreSQL (postgres.go), for an
// operator who wants the state store on a separate server. The query code in
// this package (conversations.go, messages.go) is written against
// database/sql and "?" placeholders only, so both backends share it unchanged;
// see DB.rebind below for how "?" becomes each backend's native style.
package storage

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
)

// ErrNotFound is returned by the lookup helpers when nothing is mapped yet.
// It is an expected outcome — the first message of a conversation always hits
// it — rather than a failure.
var ErrNotFound = errors.New("not found")

// Backend identifies which SQL engine a DB is talking to. Every backend is
// driven through the same query text; only placeholder rewriting
// (see DB.rebind) differs.
type Backend int

const (
	// BackendSQLite is the default backend; see Open in sqlite.go.
	BackendSQLite Backend = iota
	// BackendPostgres is the alternative backend; see OpenPostgres in
	// postgres.go.
	BackendPostgres
)

// DB is the bridge's persistent state.
type DB struct {
	sql     *sql.DB
	backend Backend
}

// Close releases the database.
func (db *DB) Close() error { return db.sql.Close() }

// rebind rewrites a query written with "?" placeholders (SQLite's style) into
// the style the backend actually expects. Every query in this package is
// written with "?" so that it reads the same regardless of backend; rebind is
// where that fiction is made true. SQLite accepts "?" unchanged, so this is a
// no-op there. PostgreSQL requires numbered placeholders ("$1", "$2", ...).
func (db *DB) rebind(query string) string {
	if db.backend != BackendPostgres {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 8)
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (db *DB) execContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return db.sql.ExecContext(ctx, db.rebind(query), args...)
}

func (db *DB) queryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return db.sql.QueryContext(ctx, db.rebind(query), args...)
}

func (db *DB) queryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return db.sql.QueryRowContext(ctx, db.rebind(query), args...)
}

// Tx wraps a database transaction with the same placeholder rewriting as DB,
// so callers can keep writing "?" inside a transaction too.
type Tx struct {
	sql *sql.Tx
	db  *DB
}

func (db *DB) beginTx(ctx context.Context) (*Tx, error) {
	sqlTx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &Tx{sql: sqlTx, db: db}, nil
}

func (tx *Tx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return tx.sql.ExecContext(ctx, tx.db.rebind(query), args...)
}

func (tx *Tx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return tx.sql.QueryRowContext(ctx, tx.db.rebind(query), args...)
}

func (tx *Tx) Commit() error { return tx.sql.Commit() }

func (tx *Tx) Rollback() error { return tx.sql.Rollback() }

// GetMeta reads a value from the key/value side table, returning "" when the
// key has never been set.
func (db *DB) GetMeta(ctx context.Context, key string) (string, error) {
	var value string
	err := db.queryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

// SetMeta writes a value into the key/value side table.
func (db *DB) SetMeta(ctx context.Context, key, value string) error {
	_, err := db.execContext(ctx,
		`INSERT INTO meta (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}
