package engine_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eyelock/ynf/internal/engine"
	"github.com/eyelock/ynf/internal/item"
	"github.com/eyelock/ynf/internal/store"
)

// TestSweepRemovesStepFoldersOlderThanTheLanesRetention: a step run folder older than the lane's
// retention goes on the next sweep; a newer one stays.
func TestSweepRemovesStepFoldersOlderThanTheLanesRetention(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.f.labels[1] = []string{"ynf:fmt", "pkg:internal/format"}
	if err := h.e.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	steps := filepath.Join(h.e.WorkDir, "steps", strings.NewReplacer("/", "_", "#", "_").Replace(item.IssueKey("github.com", "o/r", 1)))
	runs, err := os.ReadDir(steps)
	if err != nil || len(runs) == 0 {
		t.Fatalf("the run should have left a step folder in %s: %v", steps, err)
	}
	now := h.e.Now()
	old, fresh := filepath.Join(steps, "old"), filepath.Join(steps, "fresh")
	for dir, age := range map[string]time.Duration{old: 91 * 24 * time.Hour, fresh: 89 * 24 * time.Hour} {
		if err := os.MkdirAll(filepath.Join(dir, "run"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(dir, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.e.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a step folder past its retention should be removed: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("a step folder within its retention should stay: %v", err)
	}
}

// TestSweepRemovesSeenKeysOlderThanTheirTTL: a dedupe key older than SeenTTL is removed on the
// next sweep, a newer one stays, and a key from before keys carried a time is stamped, not removed.
func TestSweepRemovesSeenKeysOlderThanTheirTTL(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now := h.e.Now()
	put := func(key string, doc []byte) {
		t.Helper()
		if _, err := h.e.Store.Put(ctx, key, doc, ""); err != nil {
			t.Fatal(err)
		}
	}
	put("seen/github/old", engine.SeenDoc(now.Add(-engine.SeenTTL-time.Hour)))
	put("seen/github/fresh", engine.SeenDoc(now.Add(-engine.SeenTTL+time.Hour)))
	put("seen/github/legacy", []byte("{}"))
	if err := h.e.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.e.Store.Get(ctx, "seen/github/old"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a seen key past its TTL should be removed: %v", err)
	}
	for _, k := range []string{"seen/github/fresh", "seen/github/legacy"} {
		if _, _, err := h.e.Store.Get(ctx, k); err != nil {
			t.Errorf("%s should stay: %v", k, err)
		}
	}
	h.advance(engine.SeenTTL + time.Hour)
	if err := h.e.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.e.Store.Get(ctx, "seen/github/legacy"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a stamped legacy key should expire a TTL after it was stamped: %v", err)
	}
}
