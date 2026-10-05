package engine

import (
	"context"
	"encoding/json"
	"errors"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/eyelock/ynf/internal/event"
	"github.com/eyelock/ynf/internal/facts"
	"github.com/eyelock/ynf/internal/forge"
	"github.com/eyelock/ynf/internal/item"
	"github.com/eyelock/ynf/internal/lease"
	"github.com/eyelock/ynf/internal/memory"
	"github.com/eyelock/ynf/internal/policy"
	"github.com/eyelock/ynf/internal/store"
	"github.com/eyelock/ynf/internal/telemetry"
	"github.com/eyelock/ynf/internal/tracker"
)

// ynf's telemetry (ADR-011, ynr ADR-002): a trace per step, a short span per CloudEvent received,
// and a child span per call out to another system. Nothing here reads telemetry back, and none of
// it changes what a step does.

func (e *Engine) tracer() trace.Tracer { return e.Telemetry.Tracer() }

// hostRepo is the item's code repository in host-first form: github.com/eyelock/ynh.
func hostRepo(it item.Item) string { return it.Forge + "/" + it.Repo }

// repoAttrs name a repository on a span: host-first, and as the standard vcs URL.
func repoAttrs(hostFirst string) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String(telemetry.AttrRepo, hostFirst),
		attribute.String(telemetry.AttrVcsRepositoryURLFull, "https://"+hostFirst),
	}
}

// LaneID is the lane's id in telemetry (ADR-006): the id the lane declares, else where it is
// defined, host first, then its name: github.com/acme/factory-config#lint-paydown for a lane in
// the configuration repository, however a repository overrides it, and
// github.com/eyelock/ynh#docs-refresh for one the repository defines itself.
func (e *Engine) LaneID(rp *RepoPolicy, lane policy.Lane) string {
	if lane.ID != "" {
		return lane.ID
	}
	where := rp.Repo
	if rp.Config != nil && policy.DefinesLane(rp.Config.Lanes, lane.Name) {
		where = rp.Config.Repo
	}
	host, name := e.splitRepo(where)
	return host + "/" + name + "#" + lane.Name
}

// clip bounds and scrubs a string that came from outside, such as a webhook's headers, before it
// is an attribute.
func clip(s string) string {
	s = telemetry.Scrub(s)
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// MirrorIntake records a CloudEvent ynf received as a short ynf.intake span carrying one
// ynf.intake.received event, whether it was accepted, deduplicated or rejected (ynr ADR-003): the
// one-way mirror of the control plane. itemKey is the item it is for, when it is known. The
// returned context carries the span, so the step the event starts links to it.
func (e *Engine) MirrorIntake(parent context.Context, ev event.Event, itemKey, result string) context.Context {
	ctx, span := e.tracer().Start(parent, telemetry.SpanIntake)
	attrs := []attribute.KeyValue{
		attribute.String(telemetry.AttrCloudeventsEventID, clip(ev.ID)),
		attribute.String(telemetry.AttrCloudeventsEventSource, clip(ev.Source)),
		attribute.String(telemetry.AttrCloudeventsEventType, clip(ev.Type)),
		attribute.String(telemetry.AttrCloudeventsEventSubject, clip(ev.Subject)),
		attribute.String(telemetry.AttrIntakeResult, result),
	}
	if itemKey != "" {
		span.SetAttributes(attribute.String(telemetry.AttrItemKey, itemKey))
		attrs = append(attrs, attribute.String(telemetry.AttrItemKey, itemKey))
	}
	e.Telemetry.Event(ctx, telemetry.EventIntakeReceived, attrs...)
	// The step is linked to the intake, never nested in it: the returned context is the caller's own
	// with the intake noted.
	ictx := telemetry.WithIntake(parent, span)
	telemetry.Finish(span, result)
	return ictx
}

// MirrorDelivery records a webhook delivery that never became an event for an item, because it was
// refused or recognised as one already received: a ynf.forge.changed CloudEvent with the
// delivery's id when it has one, rejected or deduplicated. subject names the repository when it is
// known.
func (e *Engine) MirrorDelivery(ctx context.Context, name, id, subject, result string) {
	if !e.Telemetry.Active() {
		return
	}
	if id == "" {
		id = e.NewID()
	}
	e.MirrorIntake(ctx, event.New(id, "github/"+name, event.ForgeChanged, subject, e.Now(), nil), "", result)
}

// noteIntake keeps the intake span in ctx on the item, for a step in another process to link to:
// a detached start is stepped later by a running serve. It is best effort, like all telemetry.
func (e *Engine) noteIntake(ctx context.Context, key string) {
	sc, ok := telemetry.Intake(ctx)
	if !ok {
		return
	}
	for range 3 {
		it, v, err := lease.Load(ctx, e.Store, key)
		if err != nil {
			return
		}
		tr := item.Trace{}
		if it.Trace != nil {
			tr = *it.Trace
		}
		tr.Intake = &item.SpanRef{TraceID: sc.TraceID().String(), SpanID: sc.SpanID().String()}
		it.Trace = &tr
		doc, err := json.Marshal(it)
		if err != nil {
			return
		}
		if _, err := e.Store.Put(ctx, key, doc, v); !errors.Is(err, store.ErrConflict) {
			return
		}
	}
}

// itemTrace is what the item records of its history in telemetry after a step: this step's span.
// An item whose step span nobody records, with telemetry off, is left as it was.
func itemTrace(prev *item.Trace, span trace.Span) *item.Trace {
	tid, sid := telemetry.IDs(span)
	if tid == "" {
		return prev
	}
	return &item.Trace{Step: &item.SpanRef{TraceID: tid, SpanID: sid}}
}

// historyLinks are the links a step adds once it holds the item: to the item's previous step, and
// to an intake recorded on the item that no step has linked to yet, such as a detached start's.
func historyLinks(it item.Item) []trace.Link {
	var links []trace.Link
	if it.Trace == nil {
		return nil
	}
	if r := it.Trace.Step; r != nil {
		if l, ok := telemetry.LinkTo(r.TraceID, r.SpanID); ok {
			links = append(links, l)
		}
	}
	if it.Trace.Intake != nil {
		if l, ok := telemetry.LinkTo(it.Trace.Intake.TraceID, it.Trace.Intake.SpanID); ok {
			links = append(links, l)
		}
	}
	return links
}

// phase starts a child span of the step's current work and makes it the step's context until end
// is called, so calls made meanwhile are its children. A step runs its phases one after another.
func (s *step) phase(name string, attrs ...attribute.KeyValue) (span trace.Span, end func(outcome string, attrs ...attribute.KeyValue)) {
	prev := s.ctx
	ctx, span := s.e.tracer().Start(prev, name, trace.WithAttributes(attrs...))
	s.ctx = ctx
	return span, func(outcome string, more ...attribute.KeyValue) {
		telemetry.Finish(span, outcome, more...)
		s.ctx = prev
	}
}

// tracedForge spans every call to the forge.
type tracedForge struct {
	forge.Forge
	t *telemetry.T
}

// tracedForgeFixes is a traced forge that also says what closed a ticket, as the one it wraps does.
type tracedForgeFixes struct {
	tracedForge
	fixes forge.Fixes
}

func (f tracedForgeFixes) FixFor(ctx context.Context, repo string, number int) (forge.Fix, error) {
	return telemetry.Call(ctx, f.t, telemetry.CallSystemForge, "fix_for", func(ctx context.Context) (forge.Fix, error) {
		return f.fixes.FixFor(ctx, repo, number)
	}, forge.ErrNoFix, forge.ErrRebased, forge.ErrNotFound)
}

func (e *Engine) traceForge(f forge.Forge) forge.Forge {
	if !e.Telemetry.Active() {
		return f
	}
	tf := tracedForge{f, e.Telemetry}
	if fx, ok := f.(forge.Fixes); ok {
		return tracedForgeFixes{tf, fx}
	}
	return tf
}

func (f tracedForge) call(ctx context.Context, op string, fn func(context.Context) error) error {
	_, err := telemetry.Call(ctx, f.t, telemetry.CallSystemForge, op, func(ctx context.Context) (struct{}, error) { return struct{}{}, fn(ctx) }, forge.ErrNotFound)
	return err
}

func (f tracedForge) Search(ctx context.Context, query string) (hits []forge.Hit, err error) {
	err = f.call(ctx, "search", func(ctx context.Context) error { hits, err = f.Forge.Search(ctx, query); return err })
	return
}

func (f tracedForge) PullRequest(ctx context.Context, repo string, number int) (pr *facts.PR, err error) {
	err = f.call(ctx, "pull_request", func(ctx context.Context) error { pr, err = f.Forge.PullRequest(ctx, repo, number); return err })
	return
}

func (f tracedForge) FindPR(ctx context.Context, repo, branch string) (n int, err error) {
	err = f.call(ctx, "find_pr", func(ctx context.Context) error { n, err = f.Forge.FindPR(ctx, repo, branch); return err })
	return
}

func (f tracedForge) OpenPR(ctx context.Context, repo string, pr forge.NewPR) (n int, err error) {
	err = f.call(ctx, "open_pr", func(ctx context.Context) error { n, err = f.Forge.OpenPR(ctx, repo, pr); return err })
	return
}

func (f tracedForge) Comment(ctx context.Context, repo string, number int, marker, body string) error {
	return f.call(ctx, "comment", func(ctx context.Context) error { return f.Forge.Comment(ctx, repo, number, marker, body) })
}

func (f tracedForge) DefaultBranch(ctx context.Context, repo string) (b string, err error) {
	err = f.call(ctx, "default_branch", func(ctx context.Context) error { b, err = f.Forge.DefaultBranch(ctx, repo); return err })
	return
}

func (f tracedForge) Head(ctx context.Context, repo, branch string) (sha string, err error) {
	err = f.call(ctx, "head", func(ctx context.Context) error { sha, err = f.Forge.Head(ctx, repo, branch); return err })
	return
}

func (f tracedForge) File(ctx context.Context, repo, ref, path string) (b []byte, err error) {
	err = f.call(ctx, "file", func(ctx context.Context) error { b, err = f.Forge.File(ctx, repo, ref, path); return err })
	return
}

// tracedTracker spans every call to a tracker.
type tracedTracker struct {
	tracker.Tracker
	t *telemetry.T
}

func (e *Engine) traceTracker(t tracker.Tracker) tracker.Tracker {
	if !e.Telemetry.Active() {
		return t
	}
	tt := tracedTracker{t, e.Telemetry}
	if c, ok := t.(Checker); ok {
		return tracedCheckingTracker{tt, c}
	}
	return tt
}

// tracedCheckingTracker is a traced tracker that can also check its server, as the one it wraps can.
type tracedCheckingTracker struct {
	tracedTracker
	c Checker
}

func (t tracedCheckingTracker) Check(ctx context.Context) error { return t.c.Check(ctx) }

func (t tracedTracker) Get(ctx context.Context, key string) (tk facts.Ticket, text tracker.Text, err error) {
	_, _ = telemetry.Call(ctx, t.t, telemetry.CallSystemTracker, "get", func(ctx context.Context) (struct{}, error) {
		tk, text, err = t.Tracker.Get(ctx, key)
		return struct{}{}, err
	}, tracker.ErrNotFound)
	return
}

func (t tracedTracker) Comment(ctx context.Context, key, marker, body string) (err error) {
	_, err = telemetry.Call(ctx, t.t, telemetry.CallSystemTracker, "comment", func(ctx context.Context) (struct{}, error) {
		return struct{}{}, t.Tracker.Comment(ctx, key, marker, body)
	})
	return
}

func (t tracedTracker) Label(ctx context.Context, key string, add, remove []string) (err error) {
	_, err = telemetry.Call(ctx, t.t, telemetry.CallSystemTracker, "label", func(ctx context.Context) (struct{}, error) {
		return struct{}{}, t.Tracker.Label(ctx, key, add, remove)
	})
	return
}

// tracedGit spans every call to git.
type tracedGit struct {
	Git
	t *telemetry.T
}

func (e *Engine) traceGit(g Git) Git {
	if !e.Telemetry.Active() || g == nil {
		return g
	}
	tg := tracedGit{g, e.Telemetry}
	if sg, ok := g.(ShadowGit); ok {
		return tracedShadowGit{tg, sg}
	}
	return tg
}

// tracedShadowGit is a traced workspace that also computes the patches shadow mode compares, as
// the one it wraps does.
type tracedShadowGit struct {
	tracedGit
	sg ShadowGit
}

func (g tracedShadowGit) FirstParent(ctx context.Context, mirror, sha string) (string, error) {
	return gitCall(g.tracedGit, ctx, "first_parent", func(ctx context.Context) (string, error) { return g.sg.FirstParent(ctx, mirror, sha) })
}

func (g tracedShadowGit) RangeDiff(ctx context.Context, mirror, from, to string) (string, error) {
	return gitCall(g.tracedGit, ctx, "range_diff", func(ctx context.Context) (string, error) { return g.sg.RangeDiff(ctx, mirror, from, to) })
}

func (g tracedShadowGit) Diff(ctx context.Context, wt, base string) (string, error) {
	return gitCall(g.tracedGit, ctx, "diff", func(ctx context.Context) (string, error) { return g.sg.Diff(ctx, wt, base) })
}

func gitCall[V any](g tracedGit, ctx context.Context, op string, fn func(context.Context) (V, error)) (V, error) {
	return telemetry.Call(ctx, g.t, telemetry.CallSystemGit, op, fn)
}

func (g tracedGit) Mirror(ctx context.Context, repo string) (string, error) {
	return gitCall(g, ctx, "mirror", func(ctx context.Context) (string, error) { return g.Git.Mirror(ctx, repo) })
}

func (g tracedGit) Checkout(ctx context.Context, mirror, ref, dir string) (string, error) {
	return gitCall(g, ctx, "checkout", func(ctx context.Context) (string, error) { return g.Git.Checkout(ctx, mirror, ref, dir) })
}

func (g tracedGit) RemoteHas(ctx context.Context, mirror, branch string) bool {
	ok, _ := gitCall(g, ctx, "remote_has", func(ctx context.Context) (bool, error) { return g.Git.RemoteHas(ctx, mirror, branch), nil })
	return ok
}

func (g tracedGit) Changed(ctx context.Context, wt string) ([]string, error) {
	return gitCall(g, ctx, "changed", func(ctx context.Context) ([]string, error) { return g.Git.Changed(ctx, wt) })
}

func (g tracedGit) Commit(ctx context.Context, wt, message string) (string, error) {
	return gitCall(g, ctx, "commit", func(ctx context.Context) (string, error) { return g.Git.Commit(ctx, wt, message) })
}

func (g tracedGit) Push(ctx context.Context, wt, repo, branch string) error {
	_, err := gitCall(g, ctx, "push", func(ctx context.Context) (struct{}, error) { return struct{}{}, g.Git.Push(ctx, wt, repo, branch) })
	return err
}

func (g tracedGit) Head(ctx context.Context, wt string) (string, error) {
	return gitCall(g, ctx, "head", func(ctx context.Context) (string, error) { return g.Git.Head(ctx, wt) })
}

func (g tracedGit) RemoteSHA(ctx context.Context, mirror, branch string) (string, error) {
	return gitCall(g, ctx, "remote_sha", func(ctx context.Context) (string, error) { return g.Git.RemoteSHA(ctx, mirror, branch) })
}

func (g tracedGit) PushFastForward(ctx context.Context, wt, repo, branch string) error {
	_, err := gitCall(g, ctx, "push_fast_forward", func(ctx context.Context) (struct{}, error) {
		return struct{}{}, g.Git.PushFastForward(ctx, wt, repo, branch)
	})
	return err
}

// tracedMemory spans every write to ynm. What is written is never an attribute.
type tracedMemory struct {
	memory.Memory
	t *telemetry.T
}

func (e *Engine) traceMemory(m memory.Memory) memory.Memory {
	if !e.Telemetry.Active() || m == nil {
		return m
	}
	return tracedMemory{m, e.Telemetry}
}

func (m tracedMemory) Remember(ctx context.Context, r memory.Record) error {
	_, err := telemetry.Call(ctx, m.t, telemetry.CallSystemYnm, "remember", func(ctx context.Context) (struct{}, error) {
		return struct{}{}, m.Memory.Remember(ctx, r)
	})
	return err
}
