package memory

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/eyelock/ynf/internal/store"
)

// QueuePrefix is where queued writes live in ynf's store: one document per record, keyed by a ULID
// so the keys sort oldest first.
const QueuePrefix = "memory/queue/"

// QueueCap bounds the queue: past it the oldest records are dropped, so a ynm that never comes back
// cannot fill the store.
const QueueCap = 1000

// ClaimTTL is how long a claim on a queued record holds. A worker that died mid-send leaves its
// claim behind; after this another may retake the record.
const ClaimTTL = 5 * time.Minute

// Queued is the document for one record waiting to be sent.
type Queued struct {
	Record    Record    `json:"record"`
	Queued    time.Time `json:"queued"`   // when it was first queued
	Attempts  int       `json:"attempts"` // sends tried, including one in flight
	ClaimedBy string    `json:"claimedBy,omitempty"`
	ClaimedAt time.Time `json:"claimedAt,omitempty"`
}

// Queue sends records to Memory and keeps the ones it cannot in the store until it can (ADR-008):
// a ynm outage loses nothing and never stops a step. Records are sent oldest first, and a record
// is claimed with a conditional write before it is sent, so two workers sharing a store never send
// the same one.
type Queue struct {
	Memory Memory
	Store  store.Store
	Owner  string // this process, unique among the workers sharing the store
	Now    func() time.Time
	NewID  func() string // ULIDs
	Log    *slog.Logger
	// Cap and ClaimTTL default to QueueCap and ClaimTTL.
	Cap      int
	ClaimTTL time.Duration

	mu sync.Mutex // one flush at a time in this process
}

func (q *Queue) log() *slog.Logger {
	if q.Log != nil {
		return q.Log
	}
	return slog.Default()
}

func (q *Queue) cap() int {
	if q.Cap > 0 {
		return q.Cap
	}
	return QueueCap
}

func (q *Queue) claimTTL() time.Duration {
	if q.ClaimTTL > 0 {
		return q.ClaimTTL
	}
	return ClaimTTL
}

// Remember sends the record, behind anything already queued: it first flushes, and only when
// nothing is left does it send directly, so records do not jump the queue. A record that cannot be
// sent is queued. It never fails because ynm does; an error means it could not even be queued.
func (q *Queue) Remember(ctx context.Context, r Record) error {
	if left := q.Flush(ctx); left == 0 {
		err := q.Memory.Remember(ctx, r)
		if err == nil {
			return nil
		}
		q.log().Warn("memory remember, queued", "subject", r.Subject, "err", err)
	}
	return q.enqueue(ctx, r)
}

func (q *Queue) enqueue(ctx context.Context, r Record) error {
	doc, err := json.Marshal(Queued{Record: r, Queued: q.Now().UTC()})
	if err != nil {
		return err
	}
	if _, err := q.Store.Put(ctx, QueuePrefix+q.NewID(), doc, ""); err != nil {
		return err
	}
	q.trim(ctx)
	return nil
}

// trim drops the oldest records past the cap. It is best effort: a record that cannot be dropped
// now is dropped by a later trim.
func (q *Queue) trim(ctx context.Context) {
	keys, err := q.Store.Keys(ctx, QueuePrefix)
	if err != nil || len(keys) <= q.cap() {
		return
	}
	dropped := 0
	for _, k := range keys[:len(keys)-q.cap()] {
		if _, v, err := q.Store.Get(ctx, k); err == nil && q.Store.Delete(ctx, k, v) == nil {
			dropped++
		}
	}
	if dropped > 0 {
		q.log().Warn("memory queue is full, dropped the oldest writes", "dropped", dropped, "cap", q.cap())
	}
}

// Flush sends queued records oldest first, deleting each once sent, and stops at the first that
// fails: ynm is still down, so the rest stay queued. It returns how many are left, counting those
// another worker has claimed. With nothing queued it costs one Keys call.
func (q *Queue) Flush(ctx context.Context) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	keys, err := q.Store.Keys(ctx, QueuePrefix)
	if err != nil {
		q.log().Warn("memory queue", "err", err)
		return -1
	}
	left := len(keys)
	for _, k := range keys {
		sent, stop := q.send(ctx, k)
		if sent {
			left--
		}
		if stop {
			break
		}
	}
	return left
}

// send claims and sends one queued record. sent means it is gone from the queue; stop means ynm
// refused it, so the caller should not try the rest.
func (q *Queue) send(ctx context.Context, key string) (sent, stop bool) {
	doc, v, err := q.Store.Get(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		return true, false // another worker sent it
	}
	if err != nil {
		q.log().Warn("memory queue", "key", key, "err", err)
		return false, true
	}
	var d Queued
	if err := json.Unmarshal(doc, &d); err != nil {
		// Nothing can ever send this; keeping it would only block the count.
		q.log().Warn("memory queue: dropping an unreadable record", "key", key, "err", err)
		return q.Store.Delete(ctx, key, v) == nil, false
	}
	now := q.Now().UTC()
	if d.ClaimedBy != "" && d.ClaimedBy != q.Owner && now.Sub(d.ClaimedAt) < q.claimTTL() {
		return false, false // another worker is sending it
	}
	d.ClaimedBy, d.ClaimedAt = q.Owner, now
	d.Attempts++
	claimed, _ := json.Marshal(d)
	cv, err := q.Store.Put(ctx, key, claimed, v)
	if errors.Is(err, store.ErrConflict) {
		return false, false // lost the race for it
	}
	if err != nil {
		q.log().Warn("memory queue", "key", key, "err", err)
		return false, true
	}
	if err := q.Memory.Remember(ctx, d.Record); err != nil {
		q.log().Warn("memory queue: still cannot send", "key", key, "attempts", d.Attempts, "err", err)
		// Give the claim back so the next flush, here or elsewhere, need not wait out the TTL.
		d.ClaimedBy, d.ClaimedAt = "", time.Time{}
		released, _ := json.Marshal(d)
		_, _ = q.Store.Put(ctx, key, released, cv)
		return false, true
	}
	if err := q.Store.Delete(ctx, key, cv); err != nil && !errors.Is(err, store.ErrNotFound) {
		// Sent but not removed: it may be sent twice, which beats losing it.
		q.log().Warn("memory queue: sent but not removed", "key", key, "err", err)
		return false, true
	}
	return true, false
}

// Pending reports how many writes are queued and when the oldest was first queued; zero when none.
func (q *Queue) Pending(ctx context.Context) (n int, since time.Time, err error) {
	return Pending(ctx, q.Store)
}

// Pending is Queue.Pending for a store alone, which is all `ynf doctor` has.
func Pending(ctx context.Context, s store.Store) (n int, since time.Time, err error) {
	keys, err := s.Keys(ctx, QueuePrefix)
	if err != nil || len(keys) == 0 {
		return 0, time.Time{}, err
	}
	doc, _, err := s.Get(ctx, keys[0])
	if err != nil {
		return len(keys), time.Time{}, nil // sent just now; the count is still right
	}
	var d Queued
	_ = json.Unmarshal(doc, &d)
	return len(keys), d.Queued, nil
}
