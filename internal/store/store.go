// Package store is ynf's state port (ADR-004): key/value documents with compare-and-swap, an
// append-only log per item, and timers. It is designed down to what S3 can do, so every provider
// (SQLite, S3, DynamoDB) can implement it.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	// ErrConflict means the document's version was not the one the caller read.
	ErrConflict = errors.New("store: version conflict")
	// ErrNotFound means there is no document at the key.
	ErrNotFound = errors.New("store: not found")
)

// Store is the port every provider implements.
type Store interface {
	// Get returns a document and its version.
	Get(ctx context.Context, key string) (doc []byte, version string, err error)
	// Put writes a document if its current version is ifVersion; "" means create only. It returns
	// the new version, or ErrConflict.
	Put(ctx context.Context, key string, doc []byte, ifVersion string) (string, error)
	// Delete removes a document if its current version is ifVersion. ErrConflict means it was
	// changed since; ErrNotFound means it is already gone. Unlike Put there is no unconditional
	// form, so a delete can never remove a document someone else has just rewritten.
	Delete(ctx context.Context, key string, ifVersion string) error
	// Keys lists document keys with a prefix, sorted.
	Keys(ctx context.Context, prefix string) ([]string, error)

	// Append adds an immutable entry to an item's log.
	Append(ctx context.Context, item string, e LogEntry) error
	// Log returns an item's log in order.
	Log(ctx context.Context, item string) ([]LogEntry, error)

	// Schedule sets when an item is next due; a zero time clears it.
	Schedule(ctx context.Context, item string, at time.Time) error
	// Due returns items due at or before now, earliest first.
	Due(ctx context.Context, now time.Time, limit int) ([]string, error)

	Close() error
}

// LogEntry is one immutable record in an item's log.
type LogEntry struct {
	ID   string          `json:"id"` // ULID, so entries sort by time
	Time time.Time       `json:"time"`
	Kind string          `json:"kind"` // decision, action, run, note
	Body json.RawMessage `json:"body"`
}
