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
