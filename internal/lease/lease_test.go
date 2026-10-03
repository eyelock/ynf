package lease_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eyelock/ynf/internal/item"
	"github.com/eyelock/ynf/internal/lease"
	"github.com/eyelock/ynf/internal/store/sqlite"
)

func setup(t *testing.T) (*sqlite.Store, string) {
	t.Helper()
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	key := item.IssueKey("o/r", 1)
	if err := lease.Create(context.Background(), s, item.Item{Key: key, State: item.Ready}); err != nil {
		t.Fatal(err)
	}
	return s, key
}

type clock struct{ t atomic.Int64 }

func (c *clock) now() time.Time      { return time.UnixMilli(c.t.Load()) }
func (c *clock) add(d time.Duration) { c.t.Add(d.Milliseconds()) }
func newClock() *clock               { c := &clock{}; c.t.Store(time.Now().UnixMilli()); return c }

func TestExactlyOneClaimerWins(t *testing.T) {
	s, key := setup(t)
	c := newClock()
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Go(func() {
			_, err := lease.Claim(context.Background(), s, key, "owner-"+string(rune('a'+i)), "step", time.Minute, c.now)
			switch {
			case err == nil:
				wins.Add(1)
			case !errors.Is(err, lease.ErrHeld):
				t.Errorf("claim: %v", err)
			}
		})
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d claimers won, want 1", wins.Load())
	}
}

func TestExpiredLeaseIsReclaimedAndOldHolderIsFenced(t *testing.T) {
	s, key := setup(t)
	ctx := context.Background()
	c := newClock()

	a, err := lease.Claim(ctx, s, key, "a", "step-a", 90*time.Second, c.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lease.Claim(ctx, s, key, "b", "step-b", 90*time.Second, c.now); !errors.Is(err, lease.ErrHeld) {
		t.Fatalf("live lease: want ErrHeld, got %v", err)
	}

	c.add(2 * time.Minute) // a stalls past its TTL
	b, err := lease.Claim(ctx, s, key, "b", "step-b", 90*time.Second, c.now)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if b.Epoch() != a.Epoch()+1 {
		t.Fatalf("epoch %d, want %d", b.Epoch(), a.Epoch()+1)
	}

	// a wakes up and tries to write: fenced.
	it := a.Item()
	it.State = item.Done
	if err := a.Save(ctx, it); !errors.Is(err, lease.ErrLost) {
		t.Fatalf("stale save: want ErrLost, got %v", err)
	}
	if err := a.Renew(ctx); !errors.Is(err, lease.ErrLost) {
		t.Fatalf("stale renew: want ErrLost, got %v", err)
	}

	got, _, _ := lease.Load(ctx, s, key)
	if got.State != item.Ready || got.Lease.Owner != "b" {
		t.Fatalf("stored %s owned by %s; the stale write leaked", got.State, got.Lease.Owner)
	}
}

func TestRenewKeepsSavesWorking(t *testing.T) {
	s, key := setup(t)
	ctx := context.Background()
	c := newClock()
	h, err := lease.Claim(ctx, s, key, "a", "step", 90*time.Second, c.now)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		c.add(30 * time.Second)
		if err := h.Renew(ctx); err != nil {
			t.Fatal(err)
		}
	}
	it := h.Item()
	it.State = item.Running
	if err := h.Save(ctx, it); err != nil {
		t.Fatal(err)
	}
	if err := h.Release(ctx); err != nil {
		t.Fatal(err)
	}
	got, _, _ := lease.Load(ctx, s, key)
	if got.State != item.Running || got.Lease != nil {
		t.Fatalf("after release: %s lease=%v", got.State, got.Lease)
	}
}
