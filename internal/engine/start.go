package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/eyelock/ynf/internal/event"
	"github.com/eyelock/ynf/internal/facts"
	"github.com/eyelock/ynf/internal/forge"
	"github.com/eyelock/ynf/internal/item"
	"github.com/eyelock/ynf/internal/lease"
	"github.com/eyelock/ynf/internal/policy"
	"github.com/eyelock/ynf/internal/store"
	"github.com/eyelock/ynf/internal/tracker"
)

// AdhocHost is the tracker host of work started from a prompt rather than a ticket (ADR-002).
const AdhocHost = "adhoc"

// StartRequest is an instruction (ADR-003): take on this one item now.
type StartRequest struct {
	Ref    tracker.Ref // the ticket; empty when Prompt is set
	Prompt string      // ad hoc work, with no ticket
	Repo   string      // owner/name on the forge; a GitHub issue's own repository when empty
	Lane   string      // empty: the repository's only originate lane
	Detach bool        // only record it, for a running ynf serve to step
}

// RefusedError is an instruction ynf refused before creating anything: the reference, the
// tracker, the repository or the lane did not check out.
type RefusedError struct{ Reason string }

func (r *RefusedError) Error() string { return r.Reason }

func refuse(format string, args ...any) error { return &RefusedError{fmt.Sprintf(format, args...)} }

// Start carries out an instruction. It resolves the ticket, checks where its code goes and that a
// lane takes it, and fails before creating anything if any check does. Then it creates the item,
// or picks up the existing one, and steps it until it waits on something outside ynf; with
// Detach it only schedules it.
func (e *Engine) Start(ctx context.Context, req StartRequest) (item.Item, error) {
	if (req.Prompt == "") == (req.Ref == tracker.Ref{}) {
		return item.Item{}, refuse("start needs a ticket reference or a prompt, not both")
	}
	ref := req.Ref
	if req.Prompt != "" {
		ref = tracker.Ref{Host: AdhocHost, Key: strings.ToLower(e.NewID())}
	}

	repo := req.Repo
	if r, _, err := forge.ParseIssueKey(ref.Key); err == nil && ref.Host == e.forgeHost() {
		if repo != "" && repo != r {
			return item.Item{}, refuse("%s is an issue in %s, not %s", ref, r, repo)
		}
		repo = r
	}
	if repo == "" {
		return item.Item{}, refuse("%s: say which repository its code goes to, with --repo", ref)
	}
	enrolled, err := e.Enrolled(ctx)
	if err != nil {
		return item.Item{}, err
	}
	if !slices.Contains(enrolled, repo) {
		return item.Item{}, refuse("%s/%s is not an enrolled repository", e.forgeHost(), repo)
	}
	if _, err := e.Forge.DefaultBranch(ctx, repo); err != nil {
		return item.Item{}, refuse("%s/%s cannot be reached: %v", e.forgeHost(), repo, err)
	}

	lane, err := e.startLane(ctx, repo, req.Lane)
	if err != nil {
		return item.Item{}, err
	}

	if req.Prompt != "" {
		doc, _ := json.Marshal(adhocDoc{Prompt: req.Prompt})
		if _, err := e.Store.Put(ctx, adhocKey(ref.Key), doc, ""); err != nil {
			return item.Item{}, err
		}
	}
	tr, err := e.tracker(ref)
	if err != nil {
		return item.Item{}, refuse("%v", err)
	}
	if _, _, err := tr.Get(ctx, ref.Key); err != nil {
		return item.Item{}, refuse("%s cannot be read: %v", ref, err)
	}

	now := e.Now()
	it := item.Item{
		Key: item.Key(ref), Kind: "originate", Lane: lane.Name, Ticket: ref,
		Forge: e.forgeHost(), Repo: repo, State: item.Intake, Created: now, Updated: now,
	}
	if _, n, err := forge.ParseIssueKey(ref.Key); err == nil {
		it.Number = n
	}
	typ := event.TicketMatched
	switch err := lease.Create(ctx, e.Store, it); {
	case errors.Is(err, store.ErrConflict):
		existing, _, err := lease.Load(ctx, e.Store, it.Key)
		if err != nil {
			return item.Item{}, err
		}
		if existing.Lane != lane.Name {
			return item.Item{}, refuse("%s is already in lane %s", ref, existing.Lane)
		}
		it, typ = existing, event.TimerDue // a nudge: step what is there
	case err != nil:
		return item.Item{}, err
	default:
		e.log().Info("tracking", "item", it.Key, "lane", lane.Name, "by", "start")
	}
	if req.Detach {
		return it, e.Store.Schedule(ctx, it.Key, now)
	}
	ev := event.New(e.NewID(), "ynf/start", typ, it.Subject(), now, map[string]any{"lane": lane.Name})
	if err := e.Handle(ctx, it.Key, ev); err != nil {
		return it, err
	}
	it, _, err = lease.Load(ctx, e.Store, it.Key)
	return it, err
}

// startLane is the lane an instruction names, or the repository's only originate lane.
func (e *Engine) startLane(ctx context.Context, repo, name string) (policy.Lane, error) {
	rp, err := e.Policy(ctx, repo)
	if err != nil {
		return policy.Lane{}, refuse("%s's lanes cannot be read: %v", repo, err)
	}
	var originate []string
	for _, n := range rp.File.Names() {
		if l := rp.File.Lanes[n]; l.Kind != "adopt" && l.On() && e.wantLane(n) {
			originate = append(originate, n)
		}
	}
	switch {
	case name != "":
		l, ok := rp.File.Lanes[name]
		if !ok || !e.wantLane(name) {
			return policy.Lane{}, refuse("%s has no lane %s; its lanes: %s", repo, name, strings.Join(rp.File.Names(), ", "))
		}
		if l.Kind == "adopt" {
			return policy.Lane{}, refuse("lane %s adopts pull requests; start takes on tickets", name)
		}
		if !l.On() {
			return policy.Lane{}, refuse("lane %s is switched off", name)
		}
		return l, nil
	case len(originate) == 1:
		return rp.File.Lanes[originate[0]], nil
	default:
		return policy.Lane{}, refuse("%s has %d lanes that take tickets; name one with --lane: %s", repo, len(originate), strings.Join(originate, ", "))
	}
}

type adhocDoc struct {
	Prompt string `json:"prompt"`
}

func adhocKey(id string) string { return "adhoc/" + id }

// AdhocTracker is the tracker for work started from a prompt: the prompt, kept in ynf's store, is
// the ticket. There is nothing to comment on or label; the draft pull request carries the work.
func AdhocTracker(s store.Store) tracker.Tracker { return adhoc{s} }

type adhoc struct{ s store.Store }

func (a adhoc) Get(ctx context.Context, key string) (facts.Ticket, tracker.Text, error) {
	doc, _, err := a.s.Get(ctx, adhocKey(key))
	if errors.Is(err, store.ErrNotFound) {
		return facts.Ticket{}, tracker.Text{}, tracker.ErrNotFound
	}
	if err != nil {
		return facts.Ticket{}, tracker.Text{}, err
	}
	var d adhocDoc
	if err := json.Unmarshal(doc, &d); err != nil {
		return facts.Ticket{}, tracker.Text{}, err
	}
	title, _, _ := strings.Cut(strings.TrimSpace(d.Prompt), "\n")
	return facts.Ticket{Key: key, State: "open"}, tracker.Text{Title: title, Body: d.Prompt}, nil
}

func (adhoc) Comment(context.Context, string, string, string) error { return nil }

func (adhoc) Label(context.Context, string, []string, []string) error { return nil }
