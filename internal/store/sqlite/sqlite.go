// Package sqlite is the SQLite store provider (ADR-004), for the daemon host. It is pure Go
// (modernc.org/sqlite), so ynf stays a static binary.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/eyelock/ynf/internal/store"
	_ "modernc.org/sqlite" // the driver
)

const schema = `
CREATE TABLE IF NOT EXISTS docs (key TEXT PRIMARY KEY, version INTEGER NOT NULL, body BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS log (item TEXT NOT NULL, id TEXT NOT NULL, time TEXT NOT NULL, kind TEXT NOT NULL, body BLOB NOT NULL, PRIMARY KEY (item, id));
CREATE TABLE IF NOT EXISTS timers (item TEXT PRIMARY KEY, due_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS timers_due ON timers (due_at);
`

// Store is a SQLite-backed store.Store.
type Store struct{ db *sql.DB }

// Open opens (creating if needed) the database at path.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Get implements store.Store.
func (s *Store) Get(ctx context.Context, key string) ([]byte, string, error) {
	var body []byte
	var version int64
	err := s.db.QueryRowContext(ctx, `SELECT body, version FROM docs WHERE key = ?`, key).Scan(&body, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", store.ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	return body, strconv.FormatInt(version, 10), nil
}

// Put implements store.Store.
func (s *Store) Put(ctx context.Context, key string, doc []byte, ifVersion string) (string, error) {
	if ifVersion == "" {
		_, err := s.db.ExecContext(ctx, `INSERT INTO docs (key, version, body) VALUES (?, 1, ?)`, key, doc)
		if err != nil {
			if isConstraint(err) {
				return "", store.ErrConflict
			}
			return "", err
		}
		return "1", nil
	}
	v, err := strconv.ParseInt(ifVersion, 10, 64)
	if err != nil {
		return "", fmt.Errorf("sqlite: bad version %q", ifVersion)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE docs SET body = ?, version = version + 1 WHERE key = ? AND version = ?`, doc, key, v)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", store.ErrConflict
	}
	return strconv.FormatInt(v+1, 10), nil
}

// Delete implements store.Store.
func (s *Store) Delete(ctx context.Context, key, ifVersion string) error {
	v, err := strconv.ParseInt(ifVersion, 10, 64)
	if err != nil {
		return fmt.Errorf("sqlite: bad version %q", ifVersion)
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM docs WHERE key = ? AND version = ?`, key, v)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	var cur int64
	if err := s.db.QueryRowContext(ctx, `SELECT version FROM docs WHERE key = ?`, key).Scan(&cur); errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	} else if err != nil {
		return err
	}
	return store.ErrConflict
}

// Keys implements store.Store.
func (s *Store) Keys(ctx context.Context, prefix string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key FROM docs WHERE substr(key, 1, ?) = ? ORDER BY key`, len(prefix), prefix)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// Append implements store.Store.
func (s *Store) Append(ctx context.Context, item string, e store.LogEntry) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO log (item, id, time, kind, body) VALUES (?, ?, ?, ?, ?)`,
		item, e.ID, e.Time.UTC().Format(time.RFC3339Nano), e.Kind, []byte(e.Body))
	if isConstraint(err) {
		return store.ErrConflict
	}
	return err
}

// Log implements store.Store.
func (s *Store) Log(ctx context.Context, item string) ([]store.LogEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, time, kind, body FROM log WHERE item = ? ORDER BY id`, item)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []store.LogEntry
	for rows.Next() {
		var e store.LogEntry
		var t string
		var body []byte
		if err := rows.Scan(&e.ID, &t, &e.Kind, &body); err != nil {
			return nil, err
		}
		e.Time, _ = time.Parse(time.RFC3339Nano, t)
		e.Body = body
		out = append(out, e)
	}
	return out, rows.Err()
}

// Schedule implements store.Store.
func (s *Store) Schedule(ctx context.Context, item string, at time.Time) error {
	if at.IsZero() {
		_, err := s.db.ExecContext(ctx, `DELETE FROM timers WHERE item = ?`, item)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO timers (item, due_at) VALUES (?, ?) ON CONFLICT(item) DO UPDATE SET due_at = excluded.due_at`,
		item, at.UnixMilli())
	return err
}

// Due implements store.Store.
func (s *Store) Due(ctx context.Context, now time.Time, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT item FROM timers WHERE due_at <= ? ORDER BY due_at, item LIMIT ?`, now.UnixMilli(), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// Close implements store.Store.
func (s *Store) Close() error { return s.db.Close() }

func isConstraint(err error) bool {
	return err != nil && strings.Contains(err.Error(), "constraint failed")
}
