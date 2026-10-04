package engine

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/eyelock/ynf/internal/decide"
	"github.com/eyelock/ynf/internal/event"
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

// remember writes what a decision learned (ADR-008): a run's outcome as an episodic memory about
// the item, and each failure signature that occurred as an episodic memory whose subject is the
// signature, so ynm's consolidation clusters recurring failures across items. Failures to write
// are logged; memory never stops a step.
func (s *step) remember(in decide.Input, d decide.Decision) {
	m := s.e.Memory
	if m == nil {
		return
	}
	it, ns := d.Item, s.e.namespace(d.Item)
	ctx := context.WithoutCancel(s.ctx)
	write := func(r memory.Record) {
		r.Type, r.Namespace, r.Source = "episodic", ns, "ynf/step/"+s.id
		r.Tags = append([]string{"ynf", "lane:" + it.Lane}, r.Tags...)
		if err := m.Remember(ctx, r); err != nil {
			s.e.log().Warn("memory remember", "item", it.Key, "err", err)
		}
	}
	if in.Event.Type == event.RunFinished && it.LastRun != nil {
		r := it.LastRun
		write(memory.Record{
			Subject: it.Key,
			Summary: fmt.Sprintf("%s run on %s ended %s", it.Lane, it.Ref(), r.Outcome),
			Content: fmt.Sprintf("Lane `%s` ran on %s. Outcome: **%s**. %s\n\nChanged %d file(s). Decision: %s.",
				it.Lane, it.Ref(), r.Outcome, r.Detail, len(r.Changed), d.Reason),
			Tags:       []string{"outcome:" + r.Outcome},
			DataSchema: "ynf.step.v1",
			Data: map[string]any{
				"item": it.Key, "lane": it.Lane, "run_id": r.ID, "outcome": r.Outcome,
				"changed": len(r.Changed), "state": string(it.State),
			},
		})
	}
	for _, name := range slices.Sorted(maps.Keys(it.Counters)) {
		if !strings.HasPrefix(name, "sig/") || it.Counters[name] <= in.Item.Counters[name] {
			continue
		}
		write(memory.Record{
			Subject:    name,
			Summary:    fmt.Sprintf("%s on %s (%s)", name, it.Ref(), it.Lane),
			Content:    fmt.Sprintf("Failure `%s` occurred on %s in lane `%s`, %d time(s) on this item. %s", name, it.Ref(), it.Lane, it.Counters[name], d.Reason),
			Tags:       []string{"failure"},
			DataSchema: "ynf.failure.v1",
			Data:       map[string]any{"signature": name, "item": it.Key, "lane": it.Lane, "count": it.Counters[name]},
		})
	}
}
