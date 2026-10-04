package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/eyelock/ynf/internal/facts"
	"github.com/eyelock/ynf/internal/item"
	"github.com/eyelock/ynf/internal/policy"
	"github.com/eyelock/ynf/internal/store"
)

// LaneState is a lane's own record: paused by a human or a stop condition (ADR-010).
type LaneState struct {
	Repo   string    `json:"repo"`
	Lane   string    `json:"lane"`
	Paused bool      `json:"paused"`
	Reason string    `json:"reason,omitempty"`
	By     string    `json:"by,omitempty"`
	At     time.Time `json:"at"`
}

func laneKey(repo, lane string) string { return "lane/" + repo + "/" + lane }

// LaneState reads a lane's state; a lane never paused has the zero state.
func (e *Engine) LaneState(ctx context.Context, repo, lane string) (LaneState, string, error) {
	doc, v, err := e.Store.Get(ctx, laneKey(repo, lane))
	if errors.Is(err, store.ErrNotFound) {
		return LaneState{Repo: repo, Lane: lane}, "", nil
	}
	if err != nil {
		return LaneState{}, "", err
	}
	var s LaneState
	return s, v, json.Unmarshal(doc, &s)
}

// SetPaused pauses or resumes a lane, recording who and why. Unpausing is deliberate: it takes a
// reason too, because stop conditions are renegotiated under pressure (ADR-010).
func (e *Engine) SetPaused(ctx context.Context, repo, lane string, paused bool, reason, by string) error {
	if strings.TrimSpace(reason) == "" {
		return errors.New("a reason is required")
	}
	for range 5 {
		s, v, err := e.LaneState(ctx, repo, lane)
		if err != nil {
			return err
		}
		s.Paused, s.Reason, s.By, s.At = paused, reason, by, e.Now()
		doc, _ := json.Marshal(s)
		_, err = e.Store.Put(ctx, laneKey(repo, lane), doc, v)
		if errors.Is(err, store.ErrConflict) {
			continue
		}
		if err != nil {
			return err
		}
		note, _ := json.Marshal(map[string]any{"paused": paused, "reason": reason, "by": by})
		return e.Store.Append(ctx, laneKey(repo, lane), store.LogEntry{ID: e.NewID(), Time: e.Now(), Kind: "note", Body: note})
	}
	return fmt.Errorf("lane %s/%s: too many concurrent updates", repo, lane)
}

// laneFacts is the lane's state as a decision sees it.
func (e *Engine) laneFacts(ctx context.Context, repo, lane string) (*facts.Lane, error) {
	s, _, err := e.LaneState(ctx, repo, lane)
	if err != nil {
		return nil, err
	}
	items, err := e.allItems(ctx)
	if err != nil {
		return nil, err
	}
	open := 0
	for _, it := range items {
		if e.repoOf(it) == repo && it.Lane == lane && (it.State == item.Proposed || it.State == item.InReview) {
			open++
		}
	}
	reason := s.Reason
	if s.By != "" {
		reason = "by " + s.By + ": " + reason
	}
	l := &facts.Lane{Paused: s.Paused, OpenProposals: open}
	if s.Paused {
		l.PausedReason = reason
	}
	return l, nil
}

func (e *Engine) allItems(ctx context.Context) ([]item.Item, error) {
	keys, err := e.Store.Keys(ctx, "item/")
	if err != nil {
		return nil, err
	}
	out := make([]item.Item, 0, len(keys))
	for _, k := range keys {
		doc, _, err := e.Store.Get(ctx, k)
		if err != nil {
			return nil, err
		}
		var it item.Item
		if err := json.Unmarshal(doc, &it); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, nil
}

// Stats are a lane's factory numbers (ADR-010, ADR-011).
type Stats struct {
	Repo       string         `json:"repo"`
	Lane       string         `json:"lane"`
	States     map[string]int `json:"states"`
	Proposed   int            `json:"proposed"` // items that ever had a pull request
	Merged     int            `json:"merged"`
	Rejected   int            `json:"rejected"` // pull requests closed without merging
	Yield      float64        `json:"yield"`    // merged ÷ (merged + rejected); -1 with no decided proposals
	Signatures map[string]int `json:"signatures"`
	Paused     bool           `json:"paused"`
	Reason     string         `json:"reason,omitempty"`
	// Models breaks the lane's runs down by model and effort (ADR-011): which can do the work,
	// at what cost. Read from ynf's own run records.
	Models []ModelStats `json:"models,omitempty"`
}

// ModelStats is one model and effort's share of a lane's runs. Proposals, merges and rejections
// are attributed to the model of the run whose change was proposed.
type ModelStats struct {
	Model     string  `json:"model"`
	Effort    string  `json:"effort,omitempty"`
	Runs      int     `json:"runs"`
	Converged int     `json:"converged"`
	Turns     int     `json:"turns"`  // total, over runs that reported them
	Tokens    int     `json:"tokens"` // total, input and output
	CacheRead int     `json:"cache_read_tokens,omitempty"`
	CostUSD   float64 `json:"cost_usd,omitempty"`
	Proposed  int     `json:"proposed"`
	Merged    int     `json:"merged"`
	Rejected  int     `json:"rejected"`
}

// Stats computes every lane's numbers from its items. Every lane of every enrolled repository is
// listed, with or without items, so a paused lane is always visible.
func (e *Engine) Stats(ctx context.Context) ([]Stats, error) {
	items, err := e.allItems(ctx)
	if err != nil {
		return nil, err
	}
	by := map[string]*Stats{}
	enrolled, err := e.Enrolled(ctx)
	if err != nil {
		return nil, err
	}
	for _, repo := range enrolled {
		rp, err := e.Policy(ctx, repo)
		if err != nil {
			continue // a repository whose lanes cannot be read is reported by sweep and doctor
		}
		for _, name := range rp.File.Names() {
			if e.wantLane(name) {
				by[repo+"\x00"+name] = &Stats{Repo: repo, Lane: name, States: map[string]int{}, Signatures: map[string]int{}}
			}
		}
	}
	for _, it := range items {
		if !e.wantLane(it.Lane) {
			continue
		}
		k := e.repoOf(it) + "\x00" + it.Lane
		s, ok := by[k]
		if !ok {
			s = &Stats{Repo: e.repoOf(it), Lane: it.Lane, States: map[string]int{}, Signatures: map[string]int{}}
			by[k] = s
		}
		s.States[string(it.State)]++
		if it.PR > 0 || it.Kind == "adopt" {
			s.Proposed++
			switch it.State {
			case item.Done:
				s.Merged++
			case item.Closed:
				s.Rejected++
			}
		}
		for name, n := range it.Counters {
			if strings.HasPrefix(name, "sig/") {
				s.Signatures[name] += n
			}
		}
		if err := e.addModelStats(ctx, s, it); err != nil {
			return nil, err
		}
	}
	out := make([]Stats, 0, len(by))
	for _, k := range slices.Sorted(maps.Keys(by)) {
		s := by[k]
		s.Yield = -1
		if d := s.Merged + s.Rejected; d > 0 {
			s.Yield = float64(s.Merged) / float64(d)
		}
		ls, _, err := e.LaneState(ctx, s.Repo, s.Lane)
		if err != nil {
			return nil, err
		}
		s.Paused, s.Reason = ls.Paused, ls.Reason
		out = append(out, *s)
	}
	return out, nil
}

// checkStops pauses a lane whose yield has fallen below its floor over enough decided proposals
// (ADR-010). Below the minimum sample there is not enough data to say either way.
func (e *Engine) checkStops(ctx context.Context, repo string, lane policy.Lane, stats []Stats) error {
	floor := lane.Stop.YieldFloor
	if floor <= 0 {
		return nil
	}
	min := lane.Stop.MinSample
	if min == 0 {
		min = 20
	}
	for _, s := range stats {
		if s.Repo != repo || s.Lane != lane.Name || s.Paused {
			continue
		}
		if decided := s.Merged + s.Rejected; decided >= min && s.Yield < floor {
			reason := fmt.Sprintf("yield %.2f is below the floor %.2f over %d decided proposals", s.Yield, floor, decided)
			e.log().Warn("pausing lane", "repo", repo, "lane", lane.Name, "reason", reason)
			return e.SetPaused(ctx, repo, lane.Name, true, reason, "ynf")
		}
	}
	return nil
}

// addModelStats adds an item's runs to its lane's breakdown by model and effort.
func (e *Engine) addModelStats(ctx context.Context, s *Stats, it item.Item) error {
	entries, err := e.Store.Log(ctx, it.Key)
	if err != nil {
		return err
	}
	find := func(model, effort string) *ModelStats {
		for i := range s.Models {
			if s.Models[i].Model == model && s.Models[i].Effort == effort {
				return &s.Models[i]
			}
		}
		s.Models = append(s.Models, ModelStats{Model: model, Effort: effort})
		return &s.Models[len(s.Models)-1]
	}
	var last *ModelStats // the model of the latest converged run: the one whose change was proposed
	for _, en := range entries {
		if en.Kind != "run" {
			continue
		}
		var r RunRecord
		if json.Unmarshal(en.Body, &r) != nil {
			continue
		}
		model := r.Model
		switch {
		case model != "":
		case r.Backend != "":
			model = r.Backend + " (model not reported)" // the vendor's default, which it did not name
		case r.Runner == "command":
			model = "none (command)"
		case r.Runner == "":
			model = "unknown (model not reported)" // a record from before runs named their runner
		default:
			model = r.Runner + " (model not reported)"
		}
		// The effort the backend reported, else the one asked for: a backend that reports none
		// still ran at the lane's request, and grouping it under "-" would hide the comparison.
		effort := r.Effort
		if effort == "" {
			effort = r.EffortRequested
		}
		m := find(model, effort)
		m.Runs++
		m.Turns += r.Turns
		m.Tokens += r.Tokens
		m.CacheRead += r.CacheReadTokens
		m.CostUSD += r.CostUSD
		if r.Outcome == "converged" {
			m.Converged++
			last = m
		}
	}
	if last != nil && (it.PR > 0 || it.Kind == "adopt") {
		last.Proposed++
		switch it.State {
		case item.Done:
			last.Merged++
		case item.Closed:
			last.Rejected++
		}
	}
	slices.SortFunc(s.Models, func(a, b ModelStats) int { return strings.Compare(a.Model+"\x00"+a.Effort, b.Model+"\x00"+b.Effort) })
	return nil
}
