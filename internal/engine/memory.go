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

// remember writes the failures a decision saw to memory (ADR-008): one episodic memory per
// occurrence of a failure signature, its subject the signature, so ynm's consolidation can find
// what keeps happening across items. Each occurrence's text is its own (item, run, step, time), and
// each is tagged occurrence, ynm's reserved tag for one event in a series, so a dream never merges
// or supersedes occurrences and the reflect pass still counts every one. Steps themselves are not written: ynf's
// own store is the run history. Failures to write are logged; memory never stops a step.
func (s *step) remember(in decide.Input, d decide.Decision) {
	m := s.e.Memory
	if m == nil {
		return
	}
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
		if err := m.Remember(ctx, r); err != nil {
			s.e.log().Warn("memory remember", "item", it.Key, "err", err)
		}
	}
}
