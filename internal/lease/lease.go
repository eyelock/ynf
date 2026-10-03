// Package lease gives one ynf instance an exclusive, fault-tolerant claim on a work item
// (ADR-005): a compare-and-swap on the item document that bumps an epoch, a heartbeat that
// renews it, and fencing so a holder that lost its lease without knowing cannot write.
package lease

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/eyelock/ynf/internal/item"
	"github.com/eyelock/ynf/internal/store"
)

// ErrHeld means another owner holds a live lease on the item.
var ErrHeld = errors.New("lease: held by another owner")

// ErrLost means this holder no longer owns the item; it must stop and write nothing more.
var ErrLost = errors.New("lease: lost")

// Load reads an item document.
func Load(ctx context.Context, s store.Store, key string) (item.Item, string, error) {
	doc, v, err := s.Get(ctx, key)
	if err != nil {
		return item.Item{}, "", err
	}
	var it item.Item
	if err := json.Unmarshal(doc, &it); err != nil {
		return item.Item{}, "", fmt.Errorf("item %s: %w", key, err)
	}
	return it, v, nil
}

// Create writes a new item document; ErrConflict if one exists.
func Create(ctx context.Context, s store.Store, it item.Item) error {
	doc, err := json.Marshal(it)
	if err != nil {
		return err
	}
	_, err = s.Put(ctx, it.Key, doc, "")
	return err
}

// Holder is a live claim on one item.
type Holder struct {
	s     store.Store
	key   string
	owner string
	ttl   time.Duration
	now   func() time.Time

	mu      sync.Mutex
	version string
	item    item.Item
	lost    bool
}

// Claim takes the item if no live lease is held on it. stepID identifies this step's side
// effects (ADR-005: idempotency per step).
func Claim(ctx context.Context, s store.Store, key, owner, stepID string, ttl time.Duration, now func() time.Time) (*Holder, error) {
	it, v, err := Load(ctx, s, key)
	if err != nil {
		return nil, err
	}
	t := now()
	if it.Lease.Held(t) && it.Lease.Owner != owner {
		return nil, ErrHeld
	}
	var epoch int64 = 1
	if it.Lease != nil {
		epoch = it.Lease.Epoch + 1
	}
	it.Lease = &item.Lease{Owner: owner, Epoch: epoch, StepID: stepID, Acquired: t, ExpiresAt: t.Add(ttl)}
	doc, err := json.Marshal(it)
	if err != nil {
		return nil, err
	}
	nv, err := s.Put(ctx, key, doc, v)
	if errors.Is(err, store.ErrConflict) {
		return nil, ErrHeld
	}
	if err != nil {
		return nil, err
	}
	return &Holder{s: s, key: key, owner: owner, ttl: ttl, now: now, version: nv, item: it}, nil
}

// Item returns the item as this holder last wrote it.
func (h *Holder) Item() item.Item {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.item
}

// Epoch is this claim's epoch.
func (h *Holder) Epoch() int64 { return h.Item().Lease.Epoch }

// StepID is this claim's step id.
func (h *Holder) StepID() string { return h.Item().Lease.StepID }

// Save writes the item, fenced: it fails with ErrLost unless this holder's version is still
// current, which is only true while no one else has claimed the item.
func (h *Holder) Save(ctx context.Context, it item.Item) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.lost {
		return ErrLost
	}
	it.Lease = h.item.Lease
	it.Updated = h.now()
	return h.put(ctx, it)
}

// Renew extends the lease. A holder whose renewal fails must stop.
func (h *Holder) Renew(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.lost {
		return ErrLost
	}
	it := h.item
	l := *it.Lease
	l.ExpiresAt = h.now().Add(h.ttl)
	it.Lease = &l
	return h.put(ctx, it)
}

// Release clears the lease, keeping the item as last saved.
func (h *Holder) Release(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.lost {
		return ErrLost
	}
	it := h.item
	it.Lease = nil
	return h.put(ctx, it)
}

// Heartbeat renews every interval until ctx ends. If a renewal fails, it calls onLost once and
// returns: the holder must stop the work it is doing.
func (h *Holder) Heartbeat(ctx context.Context, interval time.Duration, onLost func(error)) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := h.Renew(ctx); err != nil {
				if ctx.Err() == nil {
					onLost(err)
				}
				return
			}
		}
	}
}

// put writes with the held version; caller holds mu.
func (h *Holder) put(ctx context.Context, it item.Item) error {
	doc, err := json.Marshal(it)
	if err != nil {
		return err
	}
	nv, err := h.s.Put(ctx, h.key, doc, h.version)
	if errors.Is(err, store.ErrConflict) {
		h.lost = true
		return ErrLost
	}
	if err != nil {
		return err
	}
	h.version, h.item = nv, it
	return nil
}
