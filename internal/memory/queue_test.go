package memory_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/eyelock/ynf/internal/memory"
	"github.com/eyelock/ynf/internal/store"
	"github.com/eyelock/ynf/internal/store/sqlite"
)

// sink is a Memory that can be taken down, and remembers what it was sent.
type sink struct {
	mu   sync.Mutex
	down bool
	got  []string // subjects, in the order sent
	hook func()   // runs inside Remember, before it answers
}

func (s *sink) Remember(_ context.Context, r memory.Record) error {
	if s.hook != nil {
		s.hook()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.down {
		return errors.New("ynm is down")
	}
	s.got = append(s.got, r.Subject)
	return nil
}

func (s *sink) setDown(d bool) { s.mu.Lock(); s.down = d; s.mu.Unlock() }

func (s *sink) sent() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.got...)
}

type env struct {
	st  store.Store
	m   *sink
	now time.Time
	n   int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := sqlite.Open(t.TempDir() + "/s.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &env{st: st, m: &sink{}, now: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)}
}

// queue is a worker's view of the shared store; ids sort by creation.
func (e *env) queue(owner string) *memory.Queue {
	return &memory.Queue{Memory: e.m, Store: e.st, Owner: owner, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:   func() time.Time { return e.now },
		NewID: func() string { e.n++; return fmt.Sprintf("%026d", e.n) }}
}

func rec(subject string) memory.Record {
	return memory.Record{Type: "episodic", Namespace: "factory/o/r", Subject: subject, Content: "c", Tags: []string{"a"}, Data: map[string]any{"count": 1}}
}

func TestQueueSendsDirectlyWhenYnmIsUp(t *testing.T) {
	e := newEnv(t)
	q := e.queue("w1")
	if err := q.Remember(context.Background(), rec("a")); err != nil {
		t.Fatal(err)
	}
	if got := e.m.sent(); len(got) != 1 || got[0] != "a" {
		t.Fatalf("sent %v", got)
	}
	if n, _, _ := q.Pending(context.Background()); n != 0 {
		t.Fatalf("%d queued", n)
	}
}

func TestQueueHoldsWritesWhileDownAndSendsOldestFirst(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	q := e.queue("w1")
	e.m.setDown(true)
	for _, s := range []string{"a", "b", "c"} {
		e.now = e.now.Add(time.Minute)
		if err := q.Remember(ctx, rec(s)); err != nil {
			t.Fatalf("a down ynm must not fail the write: %v", err)
		}
	}
	n, since, err := q.Pending(ctx)
	if err != nil || n != 3 || !since.Equal(time.Date(2026, 10, 5, 12, 1, 0, 0, time.UTC)) {
		t.Fatalf("pending: %d %v %v", n, since, err)
	}
	// Still down: a flush sends nothing and leaves all three, and the oldest has been tried at each flush (twice by the writes behind it, once now).
	if left := q.Flush(ctx); left != 3 {
		t.Fatalf("left %d", left)
	}
	keys, _ := e.st.Keys(ctx, memory.QueuePrefix)
	doc, _, _ := e.st.Get(ctx, keys[0])
	var d memory.Queued
	_ = json.Unmarshal(doc, &d)
	if d.Attempts != 3 || d.ClaimedBy != "" || d.Record.Subject != "a" || d.Record.Data["count"] != float64(1) {
		t.Fatalf("queued doc %+v", d)
	}

	// Back up: a new write goes behind the queue, so the order is queued first.
	e.m.setDown(false)
	if err := q.Remember(ctx, rec("d")); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(e.m.sent()); got != "[a b c d]" {
		t.Fatalf("sent %s", got)
	}
	if n, _, _ := q.Pending(ctx); n != 0 {
		t.Fatalf("%d left queued", n)
	}
}

func TestQueueStopsAtTheFirstFailure(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	q := e.queue("w1")
	e.m.setDown(true)
	for _, s := range []string{"a", "b", "c"} {
		_ = q.Remember(ctx, rec(s))
	}
	e.m.setDown(false)
	calls := 0
	e.m.hook = func() {
		if calls++; calls == 2 { // ynm drops again on the second record
			e.m.setDown(true)
		}
	}
	// the hook flips down before the second send is judged
	if left := q.Flush(ctx); left != 2 {
		t.Fatalf("left %d", left)
	}
	if calls != 2 {
		t.Fatalf("%d sends: the flush should stop at the first failure", calls)
	}
	if got := fmt.Sprint(e.m.sent()); got != "[a]" {
		t.Fatalf("sent %s", got)
	}
}

func TestQueueEmptyCostsOneKeysCall(t *testing.T) {
	e := newEnv(t)
	c := &counting{Store: e.st}
	q := &memory.Queue{Memory: e.m, Store: c, Owner: "w", Now: func() time.Time { return e.now }, NewID: func() string { return "x" }}
	if left := q.Flush(context.Background()); left != 0 {
		t.Fatal(left)
	}
	if c.calls != 1 || c.keys != 1 {
		t.Fatalf("%d store calls, %d Keys", c.calls, c.keys)
	}
}

type counting struct {
	store.Store
	calls, keys int
}

func (c *counting) Keys(ctx context.Context, p string) ([]string, error) {
	c.calls++
	c.keys++
	return c.Store.Keys(ctx, p)
}

// Two workers on one store never both send a record: the one that claims it sends it, the other
// skips; and a claim left behind by a dead worker is retaken after the TTL, not before.
func TestQueueClaimsBeforeSending(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	w1, w2 := e.queue("w1"), e.queue("w2")
	e.m.setDown(true)
	_ = w1.Remember(ctx, rec("a"))
	e.m.setDown(false)

	// w2 flushes from inside w1's send: w1 holds the claim, so w2 must not send.
	var w2left int
	e.m.hook = func() {
		e.m.hook = nil
		w2left = w2.Flush(ctx)
	}
	if left := w1.Flush(ctx); left != 0 {
		t.Fatalf("w1 left %d", left)
	}
	if w2left != 1 {
		t.Fatalf("w2 saw %d left: it should see w1's claim and skip", w2left)
	}
	if got := fmt.Sprint(e.m.sent()); got != "[a]" {
		t.Fatalf("sent %s: exactly once", got)
	}
}

func TestQueueRetakesAStaleClaim(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.m.setDown(true)
	_ = e.queue("dead").Remember(ctx, rec("a"))
	e.m.setDown(false)
	keys, _ := e.st.Keys(ctx, memory.QueuePrefix)
	doc, v, _ := e.st.Get(ctx, keys[0])
	var d memory.Queued
	_ = json.Unmarshal(doc, &d)
	d.ClaimedBy, d.ClaimedAt = "dead", e.now
	claimed, _ := json.Marshal(d)
	if _, err := e.st.Put(ctx, keys[0], claimed, v); err != nil {
		t.Fatal(err)
	}

	w := e.queue("w")
	e.now = e.now.Add(memory.ClaimTTL - time.Second)
	if left := w.Flush(ctx); left != 1 || len(e.m.sent()) != 0 {
		t.Fatalf("a live claim was taken: left %d sent %v", left, e.m.sent())
	}
	e.now = e.now.Add(2 * time.Second)
	if left := w.Flush(ctx); left != 0 || len(e.m.sent()) != 1 {
		t.Fatalf("a stale claim was not retaken: left %d sent %v", left, e.m.sent())
	}
}

func TestQueueIsBoundedAndDropsTheOldest(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	q := e.queue("w1")
	q.Cap = 3
	e.m.setDown(true)
	for _, s := range []string{"a", "b", "c", "d", "e"} {
		if err := q.Remember(ctx, rec(s)); err != nil {
			t.Fatal(err)
		}
	}
	if n, _, _ := q.Pending(ctx); n != 3 {
		t.Fatalf("%d queued, cap 3", n)
	}
	e.m.setDown(false)
	q.Flush(ctx)
	if got := fmt.Sprint(e.m.sent()); got != "[c d e]" {
		t.Fatalf("sent %s: the oldest two should have been dropped", got)
	}
}

func TestQueueDropsWhatCanNeverBeSent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, _ = e.st.Put(ctx, memory.QueuePrefix+"bad", []byte("{not json"), "")
	q := e.queue("w1")
	e.m.setDown(true)
	_ = q.Remember(ctx, rec("a")) // queued behind the unreadable one
	e.m.setDown(false)
	if left := q.Flush(ctx); left != 0 {
		t.Fatalf("left %d", left)
	}
	if got := fmt.Sprint(e.m.sent()); got != "[a]" {
		t.Fatalf("sent %s", got)
	}
}

// brokenStore fails reads or writes, as a store outage would.
type brokenStore struct {
	store.Store
	keys, put bool
}

func (b brokenStore) Keys(ctx context.Context, p string) ([]string, error) {
	if b.keys {
		return nil, errors.New("store down")
	}
	return b.Store.Keys(ctx, p)
}

func (b brokenStore) Put(ctx context.Context, k string, d []byte, v string) (string, error) {
	if b.put {
		return "", errors.New("store down")
	}
	return b.Store.Put(ctx, k, d, v)
}

func TestQueueWhenTheStoreIsDown(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	q := e.queue("w1")
	q.Store = brokenStore{Store: e.st, keys: true}
	// It cannot see the queue, so it must not send past it: the write is queued.
	if err := q.Remember(ctx, rec("a")); err != nil {
		t.Fatal(err)
	}
	if got := e.m.sent(); len(got) != 0 {
		t.Fatalf("sent past an unreadable queue: %v", got)
	}
	q.Store = brokenStore{Store: e.st, put: true}
	e.m.setDown(true)
	if err := q.Remember(ctx, rec("b")); err == nil {
		t.Fatal("a write that could not be queued must say so")
	}
	if _, _, err := memory.Pending(ctx, brokenStore{Store: e.st, keys: true}); err == nil {
		t.Fatal("pending should report a store error")
	}
}
