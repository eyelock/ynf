// Package storetest is the conformance suite every store provider must pass (ADR-004), including
// concurrent compare-and-swap races.
package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eyelock/ynf/internal/store"
)

// Options says what a provider's backend can be held to.
type Options struct {
	// NonAtomicCAS marks a backend whose conditional writes are not atomic under concurrency, such
	// as MinIO: the race is then reported as unverifiable instead of passing by luck. Leases are
	// not safe on such a backend.
	NonAtomicCAS string
}

// Run runs the suite against stores made by open; each subtest gets a fresh store.
func Run(t *testing.T, open func(t *testing.T) store.Store) { RunWith(t, open, Options{}) }

// RunWith is Run with options.
func RunWith(t *testing.T, open func(t *testing.T) store.Store, opts Options) {
	t.Run("create only", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		v, err := s.Put(ctx, "k", []byte("a"), "")
		if err != nil || v == "" {
			t.Fatalf("create: %q %v", v, err)
		}
		if _, err := s.Put(ctx, "k", []byte("b"), ""); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("second create: want ErrConflict, got %v", err)
		}
		doc, got, err := s.Get(ctx, "k")
		if err != nil || string(doc) != "a" || got != v {
			t.Fatalf("get: %q %q %v", doc, got, err)
		}
	})

	t.Run("compare and swap", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		v1, _ := s.Put(ctx, "k", []byte("a"), "")
		v2, err := s.Put(ctx, "k", []byte("b"), v1)
		if err != nil || v2 == v1 {
			t.Fatalf("swap: %q %v", v2, err)
		}
		if _, err := s.Put(ctx, "k", []byte("c"), v1); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("stale swap: want ErrConflict, got %v", err)
		}
		if _, _, err := s.Get(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("missing: want ErrNotFound, got %v", err)
		}
	})

	t.Run("conditional delete", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		v1, _ := s.Put(ctx, "k", []byte("a"), "")
		v2, _ := s.Put(ctx, "k", []byte("b"), v1)
		if err := s.Delete(ctx, "k", v1); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("stale delete: want ErrConflict, got %v", err)
		}
		if _, _, err := s.Get(ctx, "k"); err != nil {
			t.Fatalf("a refused delete removed the document: %v", err)
		}
		if err := s.Delete(ctx, "k", v2); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if _, _, err := s.Get(ctx, "k"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("after delete: want ErrNotFound, got %v", err)
		}
		if err := s.Delete(ctx, "k", v2); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("second delete: want ErrNotFound, got %v", err)
		}
		if keys, _ := s.Keys(ctx, "k"); len(keys) != 0 {
			t.Fatalf("keys after delete: %v", keys)
		}
		if _, err := s.Put(ctx, "k", []byte("again"), ""); err != nil {
			t.Fatalf("create after delete: %v", err)
		}
	})

	t.Run("exactly one racer wins", func(t *testing.T) {
		if opts.NonAtomicCAS != "" {
			t.Skip("not verifiable on this backend: " + opts.NonAtomicCAS)
		}
		s := open(t)
		ctx := context.Background()
		v0, _ := s.Put(ctx, "k", []byte("0"), "")
		const racers = 16
		var wins atomic.Int32
		var wg sync.WaitGroup
		for i := range racers {
			wg.Go(func() {
				if _, err := s.Put(ctx, "k", fmt.Appendf(nil, "%d", i), v0); err == nil {
					wins.Add(1)
				} else if !errors.Is(err, store.ErrConflict) {
					t.Errorf("racer %d: %v", i, err)
				}
			})
		}
		wg.Wait()
		if wins.Load() != 1 {
			t.Fatalf("%d racers won, want exactly 1", wins.Load())
		}
	})

	t.Run("keys by prefix", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		for _, k := range []string{"item/b", "item/a", "alias/x"} {
			if _, err := s.Put(ctx, k, []byte("{}"), ""); err != nil {
				t.Fatal(err)
			}
		}
		keys, err := s.Keys(ctx, "item/")
		if err != nil || len(keys) != 2 || keys[0] != "item/a" || keys[1] != "item/b" {
			t.Fatalf("keys: %v %v", keys, err)
		}
	})

	t.Run("append-only log in order", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		now := time.Now()
		for _, id := range []string{"02", "01", "03"} {
			if err := s.Append(ctx, "item/a", store.LogEntry{ID: id, Time: now, Kind: "note", Body: json.RawMessage(`{}`)}); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Append(ctx, "item/a", store.LogEntry{ID: "01", Time: now, Kind: "note", Body: json.RawMessage(`{}`)}); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("duplicate entry: want ErrConflict, got %v", err)
		}
		log, err := s.Log(ctx, "item/a")
		if err != nil || len(log) != 3 || log[0].ID != "01" || log[2].ID != "03" {
			t.Fatalf("log: %v %v", log, err)
		}
	})

	t.Run("timers", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		now := time.Now()
		_ = s.Schedule(ctx, "item/late", now.Add(time.Hour))
		_ = s.Schedule(ctx, "item/b", now.Add(-time.Minute))
		_ = s.Schedule(ctx, "item/a", now.Add(-2*time.Minute))
		due, err := s.Due(ctx, now, 10)
		if err != nil || len(due) != 2 || due[0] != "item/a" || due[1] != "item/b" {
			t.Fatalf("due: %v %v", due, err)
		}
		_ = s.Schedule(ctx, "item/a", time.Time{})
		due, _ = s.Due(ctx, now, 10)
		if len(due) != 1 || due[0] != "item/b" {
			t.Fatalf("after clear: %v", due)
		}
	})
}
