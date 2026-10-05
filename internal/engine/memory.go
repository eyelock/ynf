package engine

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/eyelock/ynf/internal/decide"
	"github.com/eyelock/ynf/internal/item"
	"github.com/eyelock/ynf/internal/memory"
)

// namespace is where an item's repository's memories go: {repo} is host/owner/name, so the same
// owner/name on two forges never share one (ADR-008).
func (e *Engine) namespace(it item.Item) string {
	repo := it.Forge + "/" + it.Repo
	if e.MemoryNamespace != nil {
		return e.MemoryNamespace(repo)
	}
	return "factory/" + repo
}

// memoryQueue is the queue memory writes go through: what ynm cannot take now waits in the store
// and is sent later (ADR-008). Its owner is unique to this process, so claims on queued records tell
// workers apart even when they share a configured owner.
func (e *Engine) memoryQueue() *memory.Queue {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.memq == nil {
		e.memq = &memory.Queue{Memory: e.traceMemory(e.Memory), Store: e.Store, Owner: "memq-" + e.NewID(), Now: e.Now, NewID: e.NewID, Log: e.log()}
	}
	return e.memq
}

// FlushMemory sends the memory writes that were queued while ynm was down, oldest first. It is
// cheap when none are, and it never fails: what cannot be sent stays queued.
func (e *Engine) FlushMemory(ctx context.Context) {
	if e.Memory != nil {
		e.memoryQueue().Flush(context.WithoutCancel(ctx))
	}
}

// MemoryQueued is how many memory writes are queued and when the oldest was first queued.
func (e *Engine) MemoryQueued(ctx context.Context) (int, time.Time, error) {
	return memory.Pending(ctx, e.Store)
}

// remember writes the failures a decision saw to memory (ADR-008): one episodic memory per
// occurrence of a failure signature, its subject the signature, so ynm's consolidation can find
// what keeps happening across items. Each occurrence's text is its own (item, run, step, time), and
// each is tagged occurrence, ynm's reserved tag for one event in a series, so a dream never merges
// or supersedes occurrences and the reflect pass still counts every one. Steps themselves are not written: ynf's
// own store is the run history. A write ynm cannot take is queued in ynf's store, behind any
// already queued, and sent when ynm is back; memory never stops a step.
func (s *step) remember(in decide.Input, d decide.Decision) {
	if s.e.Memory == nil {
		return
	}
	q := s.e.memoryQueue()
	it, ns := d.Item, s.e.namespace(d.Item)
	ctx := context.WithoutCancel(s.ctx)
	runID, model := "", ""
	if it.LastRun != nil {
		runID = it.LastRun.ID
	}
	if s.run != nil {
		model = s.run.Model
	}
	at := s.e.Now().UTC().Format(time.RFC3339)
	for _, name := range slices.Sorted(maps.Keys(it.Counters)) {
		if !strings.HasPrefix(name, "sig/") || it.Counters[name] <= in.Item.Counters[name] {
			continue
		}
		n := it.Counters[name]
		r := memory.Record{
			Type: "episodic", Namespace: ns, Level: s.e.MemoryLevel, Source: "ynf/step/" + s.id,
			Subject: name,
			Summary: fmt.Sprintf("%s on %s (%s), occurrence %d, run %s", name, it.Ref(), it.Lane, n, runID),
			Content: fmt.Sprintf("Failure `%s` occurred on %s in lane `%s` at %s: occurrence %d on this item, run `%s`, step `%s`, model `%s`.\n\n%s",
				name, it.Ref(), it.Lane, at, n, runID, s.id, model, d.Reason),
			Tags:       []string{"ynf", "ynf.failure.v1", "lane:" + it.Lane, "failure", "occurrence"},
			DataSchema: "ynf.failure.v1",
			Data: map[string]any{
				"signature": name, "item": it.Key, "lane": it.Lane, "count": n,
				"run_id": runID, "step": s.id, "at": at, "model": model,
			},
		}
		if err := q.Remember(ctx, r); err != nil {
			s.e.log().Warn("memory remember: could not queue", "item", it.Key, "err", err)
		}
	}
}
