package engine

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/eyelock/ynf/internal/decide"
	"github.com/eyelock/ynf/internal/policy"
	"github.com/eyelock/ynf/internal/store"
)

// Replayed is one recorded decision recomputed.
type Replayed struct {
	EntryID  string          `json:"entry_id"`
	Event    string          `json:"event"`
	Recorded decide.Decision `json:"recorded"`
	Replayed decide.Decision `json:"replayed"`
	Same     bool            `json:"same"`
	Policy   string          `json:"policy"` // recorded, or the lane hash it was replayed under
}

// Replay recomputes every decision in an item's log from its recorded inputs (ADR-011). With
// override, each decision is recomputed under that file's lane of the same name instead: a policy
// change can be tested against what actually happened before it ships.
func Replay(entries []store.LogEntry, override *policy.File) ([]Replayed, error) {
	var out []Replayed
	for _, e := range entries {
		if e.Kind != "decision" {
			continue
		}
		var rec DecisionRecord
		if err := json.Unmarshal(e.Body, &rec); err != nil {
			return nil, fmt.Errorf("entry %s: %w", e.ID, err)
		}
		in := rec.Input
		used := "recorded"
		if override != nil {
			if l, ok := override.Lanes[in.Lane.Name]; ok {
				in.Lane = l
				used = l.Hash()
			}
		}
		got := decide.Decide(in)
		a, err := json.Marshal(rec.Decision)
		if err != nil {
			return nil, err
		}
		b, err := json.Marshal(got)
		if err != nil {
			return nil, err
		}
		out = append(out, Replayed{EntryID: e.ID, Event: in.Event.Type, Recorded: rec.Decision, Replayed: got, Same: bytes.Equal(a, b), Policy: used})
	}
	return out, nil
}
