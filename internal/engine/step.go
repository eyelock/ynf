package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/eyelock/ynf/internal/decide"
	"github.com/eyelock/ynf/internal/event"
	"github.com/eyelock/ynf/internal/executor"
	"github.com/eyelock/ynf/internal/facts"
	"github.com/eyelock/ynf/internal/forge"
	"github.com/eyelock/ynf/internal/gate"
	"github.com/eyelock/ynf/internal/item"
	"github.com/eyelock/ynf/internal/lease"
	"github.com/eyelock/ynf/internal/policy"
	"github.com/eyelock/ynf/internal/runner"
	"github.com/eyelock/ynf/internal/spool"
	"github.com/eyelock/ynf/internal/store"
	"github.com/eyelock/ynf/internal/telemetry"
	"github.com/eyelock/ynf/internal/tracker"
)

// maxDecisions bounds how many decisions one step makes before it lets the timer take over.
const maxDecisions = 8

// DecisionRecord is the log entry every decision writes: everything Decide read, and what it
// returned, so `ynf replay` can recompute it (ADR-006, ADR-011).
type DecisionRecord struct {
	Input    decide.Input    `json:"input"`
	Decision decide.Decision `json:"decision"`
	Policy   PolicyRef       `json:"policy"`
}

// PolicyRef says which policy a decision ran under.
type PolicyRef struct {
	Repo      string `json:"repo"`
	Dir       string `json:"dir"`
	Ref       string `json:"ref"`
	SHA       string `json:"sha,omitempty"` // the repository's commit its lanes were read at
	Config    string `json:"config,omitempty"`
	ConfigSHA string `json:"config_sha,omitempty"`
	Hash      string `json:"hash"`
}

func configRepo(rp *RepoPolicy) string {
	if rp.Config == nil {
		return ""
	}
	return rp.Config.Repo
}

func configSHA(rp *RepoPolicy) string {
	if rp.Config == nil {
		return ""
	}
	return rp.Config.SHA
}

// RunRecord is the log entry for a run.
type RunRecord struct {
	RunID  string `json:"run_id"`
	Runner string `json:"runner"`
	// RunnerDetected is set when the lane named no runner and detection chose this one, so the
	// log and stats can tell a named runner from a detected one (ADR-012).
	RunnerDetected bool     `json:"runner_detected,omitempty"`
	Executor       string   `json:"executor"`
	Argv           []string `json:"argv"`
	Base           string   `json:"base"`
	Exit           int      `json:"exit"`
	Outcome        string   `json:"outcome"`
	Detail         string   `json:"detail,omitempty"`
	Changed        []string `json:"changed,omitempty"`
	Denied         []string `json:"denied,omitempty"` // hosts the egress proxy refused
	StepDir        string   `json:"step_dir"`
	Duration       string   `json:"duration"`
	// HarnessPin is the repository and ref a pinned harness was installed from, as the lane wrote
	// it; Harness and HarnessSHA say which harness and commit that gave.
	HarnessPin string `json:"harness_pin,omitempty"`
	// Model and Usage are what the runner reports, for comparing outcomes and cost by model and
	// effort (ADR-011). ynf's own store is the run history; memory holds only failure patterns.
	Model string `json:"model,omitempty"`
	runner.Usage
}

// ActionRecord is the log entry for a forge action.
type ActionRecord struct {
	Action string `json:"action"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
	PR     int    `json:"pr,omitempty"`
	Commit string `json:"commit,omitempty"`
}

// Handle runs one step for the item at key, starting from ev. A held lease means another instance
// is working on it, which is not an error.
//
// The step is one trace (ADR-011): its span has the claim, and for each decision a probe, a decide
// and an act, with the run and every call out to another system beneath them. It links to the
// item's previous step and to the intake of the event that started it, and leaves its own span on
// the item for the next step to link to.
func (e *Engine) Handle(ctx context.Context, key string, ev event.Event) (err error) {
	stepID := e.NewID()
	itemKey := attribute.String(telemetry.AttrItemKey, key)
	var links []trace.Link
	if sc, ok := telemetry.Intake(ctx); ok {
		links = append(links, trace.Link{SpanContext: sc})
	}
	ctx, span := e.tracer().Start(ctx, telemetry.SpanStep, trace.WithLinks(links...), trace.WithAttributes(
		itemKey, attribute.String(telemetry.AttrStepID, stepID), attribute.String(telemetry.AttrCloudeventsEventType, ev.Type)))
	var lost atomic.Bool
	outcome := telemetry.OutcomeCompleted
	defer func() {
		switch {
		case lost.Load():
			outcome = telemetry.OutcomeLost
		case err != nil && outcome == telemetry.OutcomeCompleted:
			outcome = telemetry.OutcomeFailed
		}
		telemetry.Finish(span, outcome)
	}()
	e.Telemetry.Event(ctx, telemetry.EventStepStarted, itemKey,
		attribute.String(telemetry.AttrStepID, stepID), attribute.String(telemetry.AttrCloudeventsEventType, ev.Type))

	claimCtx, claim := e.tracer().Start(ctx, telemetry.SpanClaim, trace.WithAttributes(itemKey))
	h, err := lease.Claim(claimCtx, e.Store, key, e.Owner, stepID, e.LeaseTTL, e.Now)
	if errors.Is(err, lease.ErrHeld) {
		telemetry.Finish(claim, telemetry.OutcomeHeld)
		outcome = telemetry.OutcomeHeld
		e.log().Debug("held elsewhere", "item", key)
		return nil
	}
	if err != nil {
		telemetry.Finish(claim, telemetry.OutcomeFailed)
		return err
	}
	epoch := attribute.Int64(telemetry.AttrLeaseEpoch, h.Epoch())
	telemetry.Finish(claim, telemetry.OutcomeClaimed, epoch)
	span.SetAttributes(epoch)
	for _, l := range historyLinks(h.Item()) {
		span.AddLink(l)
	}
	runCtx, cancel := context.WithCancel(ctx)
	go h.Heartbeat(runCtx, e.Heartbeat, func(it item.Item) {
		if err := e.schedule(runCtx, it); err != nil && runCtx.Err() == nil {
			e.log().Warn("reschedule on heartbeat", "item", key, "err", err)
		}
	}, func(err error) {
		lost.Store(true)
		e.log().Error("lease lost; stopping", "item", key, "err", err)
		cancel()
	})
	s := &step{e: e, h: h, id: stepID, ctx: runCtx, g: e.gitFor(e.repoOf(h.Item())), span: span, expired: h.Expired()}
	defer func() {
		s.cleanup()
		cancel()
		if !lost.Load() {
			bg := context.WithoutCancel(ctx)
			if err := h.Release(bg); err != nil && !errors.Is(err, lease.ErrLost) {
				e.log().Error("release", "item", key, "err", err)
			} else if err == nil {
				// The lease is gone, so the item's timer goes back to what its decision asked for.
				if err := e.schedule(bg, h.Item()); err != nil {
					e.log().Error("schedule", "item", key, "err", err)
				}
			}
		}
	}()

	next := &ev
	for i := 0; next != nil && i < maxDecisions; i++ {
		if next, err = s.decideAndAct(*next); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	return nil
}

type step struct {
	e   *Engine
	g   Git // the git workspace for the item's forge
	h   *lease.Holder
	id  string
	ctx context.Context
	n   int // runs in this step

	span    trace.Span // the step's own span; nil in shadow mode, which has no step
	laneID  string     // the lane's id in telemetry, once the policy is read
	expired bool       // the claim took over an expired lease, counted once the lane is known

	mirror string
	wt     string
	base   string // the commit the run started from
	run    *RunRecord
	result runner.Result
	text   forge.Text

	// Shadow mode runs a lane as the factory would, minus everything outward (FR-26): quiet keeps
	// the run out of the item's log, and the rest stand in for what a real step reads from the
	// ticket and the lane.
	quiet bool
	// labels, when labelsSet, are the ticket's labels the run reads, instead of the tracker's.
	labels    []string
	labelsSet bool
	// imageBuilt means the lane's run.image is one ynf built for this run's pin, not one the
	// lane names, so the harness in it is whatever the folder carried.
	imageBuilt bool
}

// schedule sets the item's timer: what its decision asked for, or, while a lease is held, no later
// than just after the lease expires. A live holder's heartbeat keeps pushing that forward, so it
// never fires; a dead holder's item wakes about one lease TTL after the death, and is restarted
// (ADR-005) instead of waiting out the decision's much longer timer.
func (e *Engine) schedule(ctx context.Context, it item.Item) error {
	var due time.Time
	if it.NextDue != nil {
		due = *it.NextDue
	}
	if it.Lease != nil {
		if reclaim := it.Lease.ExpiresAt.Add(reclaimGrace); due.IsZero() || reclaim.Before(due) {
			due = reclaim
		}
	}
	return e.Store.Schedule(ctx, it.Key, due)
}

// reclaimGrace keeps a reclaim timer just past the lease's expiry, so it never finds the lease
// still held.
const reclaimGrace = time.Second

func (s *step) cleanup() {
	if s.wt != "" {
		_ = s.g.RemoveCheckout(s.wt)
	}
}

func (s *step) decideAndAct(ev event.Event) (*event.Event, error) {
	e, ctx := s.e, s.ctx
	it := s.h.Item()
	rp, lane, err := e.laneFor(ctx, it)
	if err != nil {
		return nil, err
	}
	s.describe(it, rp, lane)
	itemKey := attribute.String(telemetry.AttrItemKey, it.Key)
	_, endProbe := s.phase(telemetry.SpanProbe, itemKey)
	f, err := s.probe(it)
	endProbe(telemetry.Result(err))
	if err != nil {
		return nil, err
	}
	it.NoCI = noCI(it, f, e.Now())
	in := decide.Input{Lane: lane, Item: it, Facts: f, Event: ev, Poll: e.Poll}
	in.Item.Lease = nil // not an input to the decision; keeps replay exact
	in.Item.Trace = nil // nor is where the item's history is in telemetry
	_, endDecide := s.phase(telemetry.SpanDecide, itemKey)
	d := decide.Decide(in)
	e.Telemetry.Event(s.ctx, telemetry.EventDecisionMade, itemKey, attribute.String(telemetry.AttrStepID, s.id),
		attribute.String(telemetry.AttrItemState, string(d.Item.State)), attribute.String(telemetry.AttrCloudeventsEventType, ev.Type))

	rec := DecisionRecord{Input: in, Decision: d, Policy: PolicyRef{Repo: rp.Repo, Dir: rp.Dir, Ref: rp.Base, SHA: rp.SHA, Config: configRepo(rp), ConfigSHA: configSHA(rp), Hash: lane.Hash()}}
	if err := s.record(it.Key, "decision", rec); err != nil {
		endDecide(telemetry.OutcomeFailed)
		return nil, err
	}
	d.Item.Trace = itemTrace(it.Trace, s.span) // after the record, which replay compares
	d.Item.Reason = latestReason(it, d)        // likewise: the record is the decider's own output
	if err := s.h.Save(ctx, d.Item); err != nil {
		endDecide(telemetry.OutcomeFailed)
		return nil, err
	}
	due := time.Time{}
	if d.Item.NextDue != nil {
		due = *d.Item.NextDue
	}
	if err := e.schedule(ctx, s.h.Item()); err != nil {
		endDecide(telemetry.OutcomeFailed)
		return nil, err
	}
	s.remember(in, d)
	endDecide(telemetry.OutcomeOk, attribute.String(telemetry.AttrItemState, string(d.Item.State)))
	e.log().Info("decided", "item", it.Key, "event", ev.Type, "state", d.Item.State, "reason", d.Reason)
	if d.Item.State != it.State {
		s.label(d.Item, lane)
	}

	for _, a := range d.Actions {
		_, endAct := s.phase(telemetry.SpanAct, itemKey, attribute.String(telemetry.AttrAction, a.Kind))
		next, err := s.act(a, d.Item, rp, lane)
		endAct(actOutcome(next, err))
		if err != nil {
			return nil, err
		}
		if next != nil {
			return next, nil
		}
	}
	if !due.IsZero() && !due.After(e.Now()) {
		ev := s.event(event.TimerDue, d.Item, nil)
		return &ev, nil
	}
	return nil, nil
}

// noCI keeps when a proposed head commit was first seen with no check or status of any kind, so a
// decision can tell a commit nothing has reported on for a long time from one whose CI has not
// started yet. It is a fact about the pull request, noted by the engine and recorded with the
// decision; the decider only reads it. It is dropped as soon as anything reports, or the head moves.
func noCI(it item.Item, f facts.Facts, now time.Time) *item.NoCI {
	pr := f.PR
	if pr == nil || it.State != item.Proposed || len(pr.Checks) > 0 || pr.State != "open" {
		return nil
	}
	if it.NoCI != nil && it.NoCI.SHA == pr.HeadSHA {
		return it.NoCI
	}
	return &item.NoCI{SHA: pr.HeadSHA, Since: now}
}

// latestReason is the reason the item shows after a decision. A decision that moves the item sets
// its reason itself; one that keeps it waiting (CI pending, a paused lane) only reports one, so
// the item takes it. This rides the save every decision already makes, so it costs no write. A
// settled item that is only being told something keeps the reason it was settled for.
func latestReason(it item.Item, d decide.Decision) string {
	if d.Reason == "" || d.Item.State == it.State && it.State.Settled() && it.State != item.InReview {
		return d.Item.Reason
	}
	return d.Reason
}

// actOutcome is how an action ended: failed when it errored, or when the action it asked of the
// forge was refused; ok otherwise. A run's own outcome is on its run span.
func actOutcome(next *event.Event, err error) string {
	if err != nil || next != nil && next.Type == event.ActionDone && !next.Bool("ok") {
		return telemetry.OutcomeFailed
	}
	return telemetry.OutcomeOk
}

// describe puts on the step's span what is known once its lane is: the lane's id, the policy hash
// and the repository. It also counts an expired lease the claim took over, once.
func (s *step) describe(it item.Item, rp *RepoPolicy, lane policy.Lane) {
	s.laneID = s.e.LaneID(rp, lane)
	if s.span != nil {
		s.span.SetAttributes(append(repoAttrs(hostRepo(it)),
			attribute.String(telemetry.AttrLane, s.laneID), attribute.String(telemetry.AttrPolicyHash, lane.Hash()))...)
	}
	if s.expired {
		s.expired = false
		s.e.Telemetry.LeaseExpired(s.ctx, s.laneID)
	}
}

func (s *step) event(typ string, it item.Item, data map[string]any) event.Event {
	return event.New(s.e.NewID(), "ynf/step/"+s.id, typ, it.Subject(), s.e.Now(), data)
}

func (s *step) probe(it item.Item) (facts.Facts, error) {
	var f facts.Facts
	tr, err := s.e.tracker(it.Ticket)
	if err != nil {
		return f, err
	}
	t, text, err := tr.Get(s.ctx, it.Ticket.Key)
	switch {
	case errors.Is(err, tracker.ErrNotFound):
		t = facts.Ticket{Key: it.Ticket.Key, Number: it.Number, State: "closed"}
	case err != nil:
		return f, err
	}
	f.Ticket, s.text = &t, text
	if f.Lane, err = s.e.laneFacts(s.ctx, s.e.repoOf(it), it.Lane); err != nil {
		return f, err
	}
	if it.PR > 0 {
		fg, name, err := s.e.forgeFor(s.e.repoOf(it))
		if err != nil {
			return f, err
		}
		p, err := fg.PullRequest(s.ctx, name, it.PR)
		if err != nil && !errors.Is(err, forge.ErrNotFound) {
			return f, err
		}
		f.PR = p
	}
	return f, nil
}

func (s *step) act(a decide.Action, it item.Item, rp *RepoPolicy, lane policy.Lane) (*event.Event, error) {
	switch a.Kind {
	case decide.Run:
		ev := s.runLane(it, rp, lane, a.Feedback)
		return &ev, nil
	case decide.OpenPR:
		ev := s.openPR(it, rp, lane)
		return &ev, nil
	case decide.PushCommit:
		ev := s.pushCommit(it, lane)
		return &ev, nil
	case decide.Escalate, decide.Quarantine, decide.Comment, decide.Close:
		verb := map[string]string{decide.Escalate: "escalated this", decide.Quarantine: "quarantined this", decide.Comment: "notes", decide.Close: "closed this"}[a.Kind]
		body := fmt.Sprintf("**ynf** %s: %s\n\nLane `%s`, step `%s`.", verb, a.Reason, lane.Name, s.id)
		err := s.comment(it, marker(s.id, a.Kind), body)
		s.e.log().Info("action", "item", it.Key, "action", a.Kind, "ok", err == nil, "reason", oneLine(a.Reason, 200))
		s.recordAction(it.Key, ActionRecord{Action: a.Kind, OK: err == nil, Detail: errString(err)})
		if err != nil {
			s.e.log().Error("comment", "item", it.Key, "err", err)
		}
	}
	return nil, nil
}

// runLane runs the lane's runner on a fresh worktree and reports how it ended.
func (s *step) runLane(it item.Item, rp *RepoPolicy, lane policy.Lane, feedback string) event.Event {
	e := s.e
	s.n++
	runID := fmt.Sprintf("%s-%d", s.id, s.n)
	laneID := e.LaneID(rp, lane)
	runAttrs := []attribute.KeyValue{
		attribute.String(telemetry.AttrItemKey, it.Key), attribute.String(telemetry.AttrStepID, s.id),
		attribute.String(telemetry.AttrRunID, runID), attribute.String(telemetry.AttrLane, laneID),
	}
	_, endRun := s.phase(telemetry.SpanRun, runAttrs...)
	e.Telemetry.Event(s.ctx, telemetry.EventRunStarted, runAttrs...)
	// usedHarness and usedFocus are what the run actually used, known once the harness is read.
	var usedHarness, usedFocus string
	var installed *runner.Installed // the harness installed into this run's own ynh home, if one was
	finished := func(rec RunRecord) event.Event {
		rec.RunID = runID
		if installed != nil {
			// What ynh installed, for a run that did not get as far as reporting its own.
			if rec.Harness == "" {
				rec.Harness = installed.Label()
			}
			if rec.HarnessSHA == "" {
				rec.HarnessSHA = installed.Commit
			}
			rec.HarnessPin = installed.Pin
		}
		s.run = &rec
		s.recordRun(it.Key, rec)
		done := []attribute.KeyValue{attribute.String(telemetry.AttrGenAiResponseModel, telemetry.Scrub(rec.Model))}
		if usedHarness != "" {
			done = append(done, attribute.String(telemetry.AttrLaneHarness, usedHarness))
		}
		if usedFocus != "" {
			done = append(done, attribute.String(telemetry.AttrLaneFocus, usedFocus))
		}
		e.Telemetry.RunFinished(s.ctx, rec.Outcome, laneID, rec.Model, telemetry.Usage{
			InputTokens: rec.InputTokens, OutputTokens: rec.OutputTokens, CacheReadTokens: rec.CacheReadTokens, CostUSD: rec.CostUSD})
		endRun(rec.Outcome, done...)
		return s.event(event.RunFinished, it, map[string]any{
			"run_id": runID, "outcome": rec.Outcome, "detail": rec.Detail, "changed": anyList(rec.Changed), "denied": anyList(rec.Denied),
			// What the runner reported, for the decider's failure signatures. Empty means the
			// runner reported nothing, which the decider reads as not known.
			"bound_by": rec.BoundBy, "harness": rec.Harness, "failed_sensors": anyList(rec.FailedSensors),
		})
	}
	// A run refused or broken before it starts still says what it would have used: the lane's
	// runner and executor, until the executor is built and names itself.
	runnerName, executorName, detected := lane.Run.Runner, lane.Run.Executor, false
	fail := func(outcome string, err error) event.Event {
		return finished(RunRecord{Runner: runnerName, RunnerDetected: detected, Executor: executorName, Outcome: outcome, Detail: err.Error()})
	}

	ynhHost := e.HostYnh(s.ctx)
	res, err := runner.Resolve(lane, ynhHost)
	if err != nil {
		return fail(runner.OperatorError, err)
	}
	r, detected := res.Runner, res.Detected
	runnerName = r.Name()
	ex, err := e.Executor(lane.Run.Executor)
	if err != nil {
		return fail(runner.OperatorError, err)
	}
	executorName = ex.Name()
	if !ex.Contained() && !e.Interactive {
		return fail(runner.OperatorError, fmt.Errorf("lane %s uses the %s executor, which is not contained; unattended lanes need a contained one (ADR-007)", lane.Name, ex.Name()))
	}

	mirror, err := s.g.Mirror(s.ctx, e.repoOf(it))
	if err != nil {
		return fail(runner.Error, err)
	}
	base := rp.Base
	switch {
	case it.Kind == "adopt":
		base = it.Branch // the author's branch, as it is now
		if !s.g.RemoteHas(s.ctx, mirror, base) {
			return fail(runner.Error, fmt.Errorf("the pull request's branch %s is gone", base))
		}
	case feedback != "" && it.Branch != "" && s.g.RemoteHas(s.ctx, mirror, it.Branch):
		base = it.Branch // resume from what was proposed, with the feedback
	}
	if s.wt != "" {
		_ = s.g.RemoveCheckout(s.wt)
		s.wt = ""
	}
	stepDir := filepath.Join(e.WorkDir, "steps", strings.NewReplacer("/", "_", "#", "_").Replace(it.Key), runID)
	runDir := filepath.Join(stepDir, "run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return fail(runner.Error, err)
	}
	if runDir, err = filepath.EvalSymlinks(runDir); err != nil {
		return fail(runner.Error, err)
	}
	wt, err := s.g.Checkout(s.ctx, mirror, base, filepath.Join(stepDir, "wt"))
	if err != nil {
		return fail(runner.Error, err)
	}
	s.mirror, s.wt = mirror, wt
	if s.base, err = s.g.Head(s.ctx, wt); err != nil {
		return fail(runner.Error, err)
	}

	job, inImage, err := s.job(lane, r, ex, ynhHost, wt, runDir)
	if err != nil {
		return fail(runner.OperatorError, err)
	}
	// The run joins this step's trace: its process gets the run span's context (ynr ADR-006,
	// rule 4). It has no spool folder of its own yet.
	telemetry.Inject(s.ctx, job.Env)
	inline := ex.Name() == "inline"
	if inline {
		job.Image = "" // the run is in this image, whatever the lane names for a container
		// The job runner's network policy enforces egress here, not ynf (ADR-007): say what the
		// lane expects, so a mismatch is visible.
		e.log().Info("egress is the job runner's", "item", it.Key, "lane", lane.Name, "expects", strings.Join(job.Egress, ","))
	}
	// The harness the lane is held to is the one that will run: inside the image, or in the
	// folder ynh runs on the host (ADR-012).
	var focus *runner.Focus
	var harness runner.Harness
	harnessKnown := false
	if y, ok := r.(runner.YnhRunner); ok {
		var h runner.Harness
		var known bool
		pin, pinned, _ := policy.ParsePin(y.Cfg.Harness) // the lane was validated when it was loaded
		if pinned {
			// A harness pinned from a repository: installed into a ynh home of this run's own
			// before the run starts, then read from what ynh installed, so the lane is held to
			// exactly the harness that runs, and nothing is copied into the checkout.
			inst, err := s.installFor(runner.HarnessSource{Pin: &pin}, y.Cfg.Harness, runDir)
			if err != nil {
				return fail(runner.OperatorError, err)
			}
			installed = &inst
			if h, err = runner.ReadHarness(inst.Path); err != nil {
				return fail(runner.OperatorError, fmt.Errorf("read the harness %s installed for this run: %w", pin, err))
			}
			h.ID, known = inst.ID, true
			usedHarness = inst.ID
			y.Cfg.Harness = inst.ID
			job.Env["YNH_HOME"] = filepath.Join(runDir, "ynh")
			r = y
		} else {
			var err error
			if h, known, err = s.harness(y, job.Image, inImage, lane.Run.Image == "" || s.imageBuilt, inline, wt); err != nil {
				return fail(runner.OperatorError, err)
			}
			usedHarness = y.Cfg.Harness
			switch {
			case inline && h.ID != "":
				y.Cfg.Harness = h.ID // run the harness installed here, by its id
				usedHarness = h.ID
				r = y
			case known && h.ID == "" && !inImage && e.InstallHarness != nil:
				// A harness folder on the host: ynh agent run takes only an id, so install the
				// folder into a ynh home of this run's own, and run with that home. The operator's
				// is never touched, and the run record and telemetry keep the folder the lane names.
				inst, err := s.installFor(runner.HarnessSource{Dir: harnessDir(wt, y.Cfg.Harness)}, y.Cfg.Harness, runDir)
				if err != nil {
					return fail(runner.OperatorError, err)
				}
				installed = &inst
				y.Cfg.Harness = inst.ID
				job.Env["YNH_HOME"] = filepath.Join(runDir, "ynh")
				r = y
			}
		}
		harness, harnessKnown = h, known
		usedFocus = y.Cfg.Focus
		if usedHarness == "" {
			usedHarness = h.ID
		}
		if known {
			if err := s.checkPassthrough(lane, y, h, ex.Name() == "docker" && len(job.Egress) > 0); err != nil {
				return fail(runner.OperatorError, err)
			}
			if err := h.CheckLane(y.Cfg); err != nil {
				return fail(runner.OperatorError, err)
			}
		}
		if y.Cfg.Focus != "" {
			if !known {
				return fail(runner.OperatorError, fmt.Errorf("focus %q: the harness %s cannot be read, so its focus cannot be resolved", y.Cfg.Focus, y.Cfg.Harness))
			}
			f, err := h.Focus(y.Cfg.Focus)
			if err != nil {
				return fail(runner.OperatorError, fmt.Errorf("focus %q: %w", y.Cfg.Focus, err))
			}
			focus = &f
		}
	}
	body := task(it, s.text, feedback)
	if focus != nil {
		body = focus.Prompt + "\n\n" + body
	}
	if err := os.WriteFile(filepath.Join(runDir, "task.md"), []byte(body), 0o644); err != nil {
		return fail(runner.Error, err)
	}
	_, cr := ex.Paths(job)
	labels := s.labels
	if tr, err := e.tracker(it.Ticket); err == nil && !s.labelsSet {
		if t, _, err := tr.Get(s.ctx, it.Ticket.Key); err == nil {
			labels = t.Labels
		}
	}
	// The scopes were checked above without labels; now with this item's, so what runs is what was
	// narrowed.
	if y, ok := r.(runner.YnhRunner); ok && harnessKnown {
		if err := harness.CheckScopes(y.Cfg, labels); err != nil {
			return fail(runner.OperatorError, err)
		}
	}
	argv, err := r.Command(runner.Spec{Lane: lane, Labels: labels, TaskFile: cr + "/task.md", RunDir: cr, Feedback: feedback, InImage: inImage, Contained: ex.Contained(), Focus: focus, HostAutoApprove: e.HostAutoApprove})
	if err != nil {
		return fail(runner.OperatorError, err)
	}
	job.Argv = argv
	if y, ok := r.(runner.YnhRunner); ok && y.Cfg.TelemetryRelay {
		// ynh agent run starts ynr relay for the vendor CLI, which writes into the run's folder.
		job.Env["YNH_TELEMETRY_RELAY"] = "1"
		if e.Spool == nil {
			e.log().Warn("telemetry_relay is on, and there is no spool root to relay into: set telemetry.spool", "item", it.Key, "lane", lane.Name)
		}
	}
	spoolRun := s.beginSpool(&job, ex, spool.Manifest{Run: runID, Lane: laneID, Harness: usedHarness, Focus: usedFocus, Item: it.Key, Step: s.id})

	log := e.log().With("item", it.Key, "run", runID)
	log.Info("run started", "lane", lane.Name, "runner", r.Name(), "executor", ex.Name(), "image", job.Image, "base", base, "attempt", it.Attempts)
	start := e.Now()
	stop := s.progress(log, filepath.Join(runDir, "trajectory.jsonl"))
	out, err := telemetry.Call(s.ctx, e.Telemetry, telemetry.CallSystemExecutor, ex.Name(), func(ctx context.Context) (executor.Output, error) {
		return ex.Run(ctx, job)
	})
	stop()
	s.endSpool(spoolRun, stepDir)
	rec := RunRecord{Runner: r.Name(), RunnerDetected: detected, Executor: ex.Name(), Argv: argv, Base: base, StepDir: stepDir, Denied: out.Denied}
	_ = os.WriteFile(filepath.Join(runDir, "stdout"), out.Stdout, 0o644)
	_ = os.WriteFile(filepath.Join(runDir, "stderr"), out.Stderr, 0o644)
	switch {
	case errors.Is(err, executor.ErrEgress):
		rec.Outcome, rec.Detail = runner.OperatorError, err.Error()
	case err != nil:
		rec.Outcome, rec.Detail = runner.Error, err.Error()
	default:
		s.result = r.Interpret(out.Exit, out.Stdout, runDir)
		rec.Exit, rec.Outcome, rec.Detail = out.Exit, s.result.Outcome, s.result.Detail
		rec.Model, rec.Usage = s.result.Model, s.result.Usage
		if rec.Outcome == runner.Error && len(out.Stderr) > 0 && !strings.Contains(rec.Detail, ":") {
			rec.Detail += ": " + tail(string(out.Stderr))
		}
	}
	if detected && rec.RunnerVersion == "" && r.Name() == "ynh" {
		rec.RunnerVersion = ynhHost.Version // the detected version, when the run did not report its own
	}
	rec.Duration = e.Now().Sub(start).Round(time.Millisecond).String()
	if rec.Changed, err = s.g.Changed(s.ctx, wt); err != nil {
		rec.Outcome, rec.Detail = runner.Error, err.Error()
	}
	log.Info("run finished", "outcome", rec.Outcome, "exit", rec.Exit, "duration", rec.Duration,
		"changed", len(rec.Changed), "denied", strings.Join(rec.Denied, ","), "model", rec.Model, "turns", rec.Turns, "tokens", rec.Tokens,
		"detail", oneLine(rec.Detail, 200))
	return finished(rec)
}

// job describes the run for the executor. A ynh lane on a contained executor runs in an agent
// image ynf builds from the harness (or the lane's run.image), as the image's own user, with the
// vendor's API host allowed through the egress proxy (ADR-007, ADR-012).
func (s *step) job(lane policy.Lane, r runner.Runner, ex executor.Executor, ynhHost runner.Detection, wt, runDir string) (executor.Job, bool, error) {
	e := s.e
	job := executor.Job{Worktree: wt, RunDir: runDir, Image: lane.Run.ImageFor(r.Name()), Timeout: e.RunTimeout, Env: map[string]string{}, Secrets: map[string]string{}}
	if lane.Run.Egress != nil {
		job.Egress = append([]string(nil), lane.Run.Egress.Allow...)
	}
	getenv := e.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	for _, name := range lane.Run.Env {
		if v := getenv(name); v != "" {
			job.Secrets[name] = v
		}
	}
	y, isYnh := r.(runner.YnhRunner)
	if !isYnh {
		job.ImageUser = r.Name() == "command" && lane.Run.Command != nil && lane.Run.Command.ImageUser
		return job, false, nil
	}
	// A lane that names ynh never falls back, and never runs without it: where ynh runs on this
	// host (inline, the process executor, or an image built here), a missing or unsupported ynh
	// refuses the run before anything starts. A published image carries its own ynh.
	if e.DetectYnh != nil && lane.Run.Runner == "ynh" && !ynhHost.Found && (ex.Name() == "inline" || !ex.Contained() || job.Image == "") {
		return job, false, fmt.Errorf("lane %s names runner ynh, and %s", lane.Name, ynhUnusable(ynhHost))
	}
	if ex.Name() == "inline" {
		// The factory image: ynh is installed here and the job runner contains it (ADR-007), so a
		// lane's auto_approve applies, once this ynh is shown to support it.
		if y.Cfg.AutoApprove != "" && e.HostCapabilities != nil {
			caps, err := e.HostCapabilities(s.ctx)
			if err != nil {
				return job, false, fmt.Errorf("read this image's ynh capabilities: %w", err)
			}
			if !atLeast(caps, autoApproveCapabilities) {
				return job, false, fmt.Errorf("lane %s sets auto_approve, which needs ynh capabilities %s; this image's ynh has %s", lane.Name, autoApproveCapabilities, caps)
			}
		}
		// ynf does not enforce egress here, but the agent still needs its model's API, so the
		// hosts the log says the job runner must allow include it.
		job.Egress = withModelHosts(job.Egress, y)
		return job, false, nil
	}
	if !ex.Contained() {
		if y.Cfg.AutoApprove != "" && e.HostAutoApprove == "" {
			e.log().Warn("auto_approve applies only inside containment; this run keeps its approval prompts", "lane", lane.Name, "executor", ex.Name())
		}
		if e.HostAutoApprove != "" && e.HostCapabilities != nil {
			caps, err := e.HostCapabilities(s.ctx)
			if err != nil {
				return job, false, fmt.Errorf("read this machine's ynh capabilities: %w", err)
			}
			if !atLeast(caps, autoApproveCapabilities) {
				return job, false, fmt.Errorf("--auto-approve needs ynh capabilities %s; this machine's ynh has %s", autoApproveCapabilities, caps)
			}
		}
		return job, false, nil
	}
	if pin, ok, _ := policy.ParsePin(y.Cfg.Harness); ok {
		// ynf builds an image from a folder, not from a repository, and a published image carries
		// its own harness: neither is a place to install a pin for one run.
		return job, false, fmt.Errorf("lane %s pins harness %s from a repository, which only the host executors (process, inline) run: on %s, use a harness folder in the repository or run.image", lane.Name, pin, ex.Name())
	}
	if job.Image == "" {
		if e.BuildImage == nil {
			return job, false, fmt.Errorf("lane %s names no published image (run.image), and this instance does not build one: that needs ynh on PATH and images.build not false", lane.Name)
		}
		img, err := telemetry.Call(s.ctx, e.Telemetry, telemetry.CallSystemYnh, "image", func(ctx context.Context) (string, error) {
			return e.BuildImage(ctx, wt, y.Cfg)
		})
		if err != nil {
			return job, false, fmt.Errorf("build agent image: %w", err)
		}
		job.Image = img
	}
	job.ImageUser = true
	if y.Cfg.AutoApprove != "" && e.ImageCapabilities != nil {
		caps, err := e.ImageCapabilities(s.ctx, job.Image)
		if err != nil {
			return job, false, fmt.Errorf("read the agent image's ynh capabilities: %w", err)
		}
		if !atLeast(caps, autoApproveCapabilities) {
			return job, false, fmt.Errorf("lane %s sets auto_approve, which needs ynh capabilities %s in the agent image; %s has %s: rebuild its base on a newer ynh", lane.Name, autoApproveCapabilities, job.Image, caps)
		}
	}
	job.Egress = withModelHosts(job.Egress, y)
	if y.Cfg.Vendor != "" {
		job.Env["YNH_VENDOR"] = y.Cfg.Vendor
	}
	return job, true, nil
}

// withModelHosts adds the lane's vendor's model API hosts to the hosts the lane allows, after
// them and without repeating one the lane already lists.
func withModelHosts(hosts []string, y runner.YnhRunner) []string {
	for _, h := range runner.ModelHosts[y.Vendor()] {
		if !slices.Contains(hosts, h) {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

func ynhUnusable(d runner.Detection) string {
	if d.Detail != "" {
		return "ynh is not usable on this host: " + d.Detail
	}
	return "ynh was not found on this host"
}

// autoApproveCapabilities is the first ynh capabilities version with --auto-approve.
const autoApproveCapabilities = "0.9.0"

// atLeast compares dotted versions numerically; anything unparseable is too old.
func atLeast(have, want string) bool { return runner.AtLeast(have, want) }

// proxyVars are what a worker behind the egress proxy needs to reach it.
var proxyVars = []string{"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY"}

// checkPassthrough refuses a ynh run whose worker could not use what ynf gives it. ynh passes its
// worker only the variables the harness lists in env_passthrough, deliberately, so a lane's
// run.env (the model key) or the egress proxy's variables that the harness does not list never
// arrive, and the run fails in a way that looks like the agent being stuck.
func (s *step) checkPassthrough(lane policy.Lane, y runner.YnhRunner, h runner.Harness, behindProxy bool) error {
	need := append([]string(nil), lane.Run.Env...)
	if behindProxy {
		need = append(need, proxyVars...)
	}
	if missing := h.NotPassed(need); len(missing) > 0 {
		return fmt.Errorf("harness %s does not pass %s to its agent worker: ynh gives the worker only what env_passthrough lists, so without them the agent cannot log in or reach the egress proxy; add them to env_passthrough in the harness manifest",
			harnessName(y), strings.Join(missing, ", "))
	}
	return nil
}

// harness reads what the harness that will run declares: from the image's own ynh, or from the
// harness folder ynh runs on the host. known is false for an installed harness id on the host,
// whose manifest ynh reads and reports on itself.
func (s *step) harness(y runner.YnhRunner, image string, inImage, built, inline bool, wt string) (h runner.Harness, known bool, err error) {
	if inline {
		// The harness installed in this image; a folder in the repository means the one it carries.
		if s.e.ImageHarness == nil {
			return h, false, errors.New("inline runs need ynh to read the harness installed here")
		}
		want := y.Cfg.Harness
		if want == "." || isFolder(wt, want) {
			want = ""
		}
		if h, err = s.e.ImageHarness(s.ctx, "", want); err != nil {
			// Nothing installed here for a lane that names the repository's harness: run the
			// one the repository carries, as the process executor does.
			if y.Cfg.Harness != "" && isFolder(wt, y.Cfg.Harness) {
				if hf, rerr := runner.ReadHarness(harnessDir(wt, y.Cfg.Harness)); rerr == nil {
					return hf, true, nil
				}
			}
			return h, false, fmt.Errorf("read the harness installed here: %w", err)
		}
		return h, true, nil
	}
	if inImage {
		if s.e.ImageHarness == nil {
			return h, false, nil
		}
		// An image ynf built from a harness folder carries exactly that harness; a published one
		// is held to the harness the lane names, if it names one.
		want := y.Cfg.Harness
		if built {
			want = ""
		}
		h, err = s.e.ImageHarness(s.ctx, image, want)
		if err != nil {
			return h, false, fmt.Errorf("read the harness in %s: %w", image, err)
		}
		return h, true, nil
	}
	if y.Cfg.Harness == "" {
		return h, false, errors.New("a ynh lane on the host needs ynh.harness, the harness to run")
	}
	if !isFolder(wt, y.Cfg.Harness) {
		return h, false, nil // an installed harness id, which ynh reads and reports on itself
	}
	// A folder that is there and cannot be read is the lane's mistake, not an installed id: the
	// lane is checked against it, or the run is refused.
	if h, err = runner.ReadHarness(harnessDir(wt, y.Cfg.Harness)); err != nil {
		return h, false, err
	}
	return h, true, nil
}

// installFor installs src into a ynh home of this run's own, <run>/ynh, and says what was
// installed. It is the one step that can reach the network (a pin is cloned), and it runs here, on
// the host, before the run's containment starts, so the run's egress is never asked for it.
//
// Folders and pins both come through here, so a ynh that can run a folder directly can skip the
// folder case in one place (the folder branch in runLane) and leave pins as they are.
func (s *step) installFor(src runner.HarnessSource, named, runDir string) (runner.Installed, error) {
	if s.e.InstallHarness == nil {
		return runner.Installed{}, fmt.Errorf("harness %s cannot be installed for this run: this instance has no ynh to install it with", named)
	}
	inst, err := telemetry.Call(s.ctx, s.e.Telemetry, telemetry.CallSystemYnh, "install", func(ctx context.Context) (runner.Installed, error) {
		return s.e.InstallHarness(ctx, src, filepath.Join(runDir, "ynh"))
	})
	if err != nil {
		return inst, fmt.Errorf("install the harness %s for this run: %w", named, err)
	}
	return inst, nil
}

// harnessDir is the folder a lane's harness value names: in the checkout, or absolute (shadow mode
// pins a harness folder outside the checkout).
func harnessDir(wt, harness string) string {
	if filepath.IsAbs(harness) {
		return harness
	}
	return filepath.Join(wt, filepath.FromSlash(harness))
}

// isFolder reports whether name is a folder in the worktree, rather than an installed harness id.
func isFolder(wt, name string) bool {
	if name == "" || strings.Contains(name, "@") {
		return false
	}
	fi, err := os.Stat(harnessDir(wt, name))
	return err == nil && fi.IsDir()
}

func harnessName(y runner.YnhRunner) string {
	if y.Cfg.Harness == "" {
		return "in the image"
	}
	return y.Cfg.Harness
}

// openPR gates, commits, pushes and opens (or reuses) the pull request for this step's change.
func (s *step) openPR(it item.Item, rp *RepoPolicy, lane policy.Lane) event.Event {
	e := s.e
	done := func(ok bool, detail string, data map[string]any) event.Event {
		e.log().Info("action", "item", it.Key, "action", decide.OpenPR, "ok", ok, "pr", data["pr"], "detail", oneLine(detail, 200))
		rec := ActionRecord{Action: decide.OpenPR, OK: ok, Detail: detail}
		if data != nil {
			rec.PR, _ = data["pr"].(int)
			rec.Commit, _ = data["head"].(string)
		}
		s.recordAction(it.Key, rec)
		if data == nil {
			data = map[string]any{}
		}
		data["action"], data["ok"], data["reason"] = decide.OpenPR, ok, detail
		return s.event(event.ActionDone, it, data)
	}
	if s.wt == "" || s.run == nil {
		return done(false, "no change from this step to propose", nil)
	}
	if err := gate.Check(s.run.Changed, lane.PR.AllowedPaths, lane.PR.ProtectedPaths); err != nil {
		return done(false, err.Error(), nil)
	}
	subject := fmt.Sprintf("ynf(%s): %s", lane.Name, s.text.Title)
	if len(subject) > 72 {
		subject = subject[:71] + "…"
	}
	msg := subject + "\n\n" + fmt.Sprintf("Proposed by ynf for %s, lane %s.\n\n", it.Ref(), lane.Name) + trailers(it, s.id, s.run.RunID, s.result)
	sha, err := s.g.Commit(s.ctx, s.wt, msg)
	if err != nil {
		return done(false, err.Error(), nil)
	}
	branch := it.BranchName()
	if err := s.g.Push(s.ctx, s.wt, e.repoOf(it), branch); err != nil {
		return done(false, err.Error(), nil)
	}
	fg, name, err := e.forgeFor(e.repoOf(it))
	if err != nil {
		return done(false, err.Error(), nil)
	}
	n, err := fg.FindPR(s.ctx, name, branch)
	if err != nil {
		return done(false, err.Error(), nil)
	}
	if n == 0 {
		n, err = fg.OpenPR(s.ctx, name, forge.NewPR{
			Head: branch, Base: rp.Base, Title: subject, Draft: lane.PR.IsDraft(),
			Body: prBody(it, lane, s.run) + "\n\n" + marker(it.Key, "item"),
		})
		if err != nil {
			return done(false, err.Error(), nil)
		}
	}
	s.commentQuietly(it, decide.OpenPR, "**ynf** proposed "+prLink(it, n)+".")
	return done(true, "", map[string]any{"pr": n, "branch": branch, "head": sha})
}

// pushCommit adds this step's change to an adopted pull request's branch: gated, never forced, and
// only if the author has not pushed since the run started (ADR-007).
func (s *step) pushCommit(it item.Item, lane policy.Lane) event.Event {
	e := s.e
	done := func(ok bool, detail string, data map[string]any) event.Event {
		e.log().Info("action", "item", it.Key, "action", decide.PushCommit, "ok", ok, "pr", it.PR, "detail", oneLine(detail, 200))
		rec := ActionRecord{Action: decide.PushCommit, OK: ok, Detail: detail, PR: it.PR}
		if data == nil {
			data = map[string]any{}
		}
		rec.Commit, _ = data["head"].(string)
		s.recordAction(it.Key, rec)
		data["action"], data["ok"], data["reason"] = decide.PushCommit, ok, detail
		return s.event(event.ActionDone, it, data)
	}
	if s.wt == "" || s.run == nil {
		return done(false, "no change from this step to propose", nil)
	}
	if err := gate.Check(s.run.Changed, lane.PR.AllowedPaths, lane.PR.ProtectedPaths); err != nil {
		return done(false, err.Error(), nil)
	}
	if _, err := s.g.Mirror(s.ctx, e.repoOf(it)); err != nil {
		return done(false, err.Error(), nil)
	}
	if tip, err := s.g.RemoteSHA(s.ctx, s.mirror, it.Branch); err != nil || tip != s.base {
		return done(false, fmt.Sprintf("%s moved from %.7s to %.7s while ynf worked", it.Branch, s.base, tip), map[string]any{"head_moved": true})
	}
	subject := fmt.Sprintf("ynf(%s): %s", lane.Name, s.text.Title)
	if len(subject) > 72 {
		subject = subject[:71] + "…"
	}
	msg := subject + "\n\n" + fmt.Sprintf("Added by ynf to %s, lane %s.\n\n", it.Ref(), lane.Name) + trailers(it, s.id, s.run.RunID, s.result)
	sha, err := s.g.Commit(s.ctx, s.wt, msg)
	if err != nil {
		return done(false, err.Error(), nil)
	}
	if err := s.g.PushFastForward(s.ctx, s.wt, e.repoOf(it), it.Branch); err != nil {
		// Someone pushed between the check and the push: never overwrite, start again.
		return done(false, err.Error(), map[string]any{"head_moved": true})
	}
	s.commentQuietly(it, decide.PushCommit,
		fmt.Sprintf("**ynf** added %.7s to this pull request (lane `%s`). The pull request stays yours: review the commit, and merge or revert it as you would any other.", sha, lane.Name))
	return done(true, "", map[string]any{"head": sha})
}

func (s *step) record(key, kind string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return s.e.Store.Append(s.ctx, key, store.LogEntry{ID: s.e.NewID(), Time: s.e.Now(), Kind: kind, Body: b})
}

func (s *step) recordRun(key string, r RunRecord) {
	if s.quiet {
		return
	}
	if err := s.record(key, "run", r); err != nil {
		s.e.log().Error("record run", "item", key, "err", err)
	}
}

func (s *step) recordAction(key string, a ActionRecord) {
	if err := s.record(key, "action", a); err != nil {
		s.e.log().Error("record action", "item", key, "err", err)
	}
}

// trailers are ynf's attribution (ADR-010): they survive a squash merge. Co-Authored-By is written
// only for a vendor whose address ynf knows.
func trailers(it item.Item, stepID, runID string, r runner.Result) string {
	var b strings.Builder
	if addr := runner.CoAuthorAddress(r); r.Model != "" && addr != "" {
		fmt.Fprintf(&b, "Co-Authored-By: %s <%s>\n", r.Model, addr)
	}
	fmt.Fprintf(&b, "YNF-Item: %s\nYNF-Step: %s\nYNF-Run: %s\n", it.Ref(), stepID, runID)
	if r.Session != "" {
		fmt.Fprintf(&b, "YNH-Session: %s\n", r.Session)
	}
	return b.String()
}

// prLink names a pull request on its ticket: #12 beside a GitHub issue in the same repository,
// which GitHub links; its URL anywhere else, such as on a JIRA ticket.
func prLink(it item.Item, n int) string {
	if repo, _, err := forge.ParseIssueKey(it.Ticket.Key); err == nil && it.Ticket.Host == it.Forge && repo == it.Repo {
		return fmt.Sprintf("#%d", n)
	}
	return fmt.Sprintf("https://%s/%s/pull/%d", it.Forge, it.Repo, n)
}

func prBody(it item.Item, lane policy.Lane, run *RunRecord) string {
	var b strings.Builder
	// Closes links a GitHub issue in the same repository; any other ticket is named.
	if repo, n, err := forge.ParseIssueKey(it.Ticket.Key); err == nil && it.Ticket.Host == it.Forge && repo == it.Repo {
		fmt.Fprintf(&b, "Closes #%d.\n\n", n)
	} else {
		fmt.Fprintf(&b, "For %s.\n\n", it.Ref())
	}
	fmt.Fprintf(&b, "Proposed by **ynf**: lane `%s`, runner `%s`, executor `%s`.\n\n", lane.Name, run.Runner, run.Executor)
	b.WriteString("| | |\n|---|---|\n")
	fmt.Fprintf(&b, "| Item | `%s` |\n| Run | `%s` (%s, %s) |\n| Changed | %d file(s) |\n", it.Key, run.RunID, run.Outcome, run.Duration, len(run.Changed))
	b.WriteString("\nA human reviews and merges this; ynf never merges.")
	return b.String()
}

// task is what the runner is asked to do. Ticket text is quoted data, never instructions to ynf
// (NFR-5).
func task(it item.Item, t forge.Text, feedback string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Task: %s\n\n%s\n\nThe ticket, quoted as the reporter wrote it:\n\n", it.Ref(), t.URL)
	for l := range strings.SplitSeq(strings.TrimSpace(t.Title+"\n\n"+t.Body), "\n") {
		b.WriteString("> " + l + "\n")
	}
	if feedback != "" {
		b.WriteString("\n## Feedback from the previous attempt\n\n" + feedback + "\n")
	}
	return b.String()
}

// progress logs a run that is still going, every ProgressEvery, with what its runner's trajectory
// says so far, so a long agent run is never silent. The returned func stops it.
func (s *step) progress(log *slog.Logger, trajectory string) func() {
	every := s.e.ProgressEvery
	if every == 0 {
		every = 30 * time.Second
	}
	if every < 0 {
		return func() {}
	}
	start := time.Now()
	done := make(chan struct{})
	ctx := s.ctx // phases swap s.ctx while this runs
	go func() {
		tick := time.NewTicker(every)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-tick.C:
				turns, last := trajectorySoFar(trajectory)
				log.Info("run in progress", "elapsed", time.Since(start).Round(time.Second).String(), "turns", turns, "last", last)
			}
		}
	}()
	return func() { close(done) }
}

// trajectorySoFar counts a ynh trajectory's turns and names its latest event; a runner without a
// trajectory reports nothing.
func trajectorySoFar(path string) (turns int, last string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, ""
	}
	for l := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		var ev struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(l), &ev) != nil || ev.Type == "" {
			continue
		}
		if ev.Type == "turn_start" {
			turns++
		}
		last = ev.Type
	}
	return turns, last
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

// label writes the lane's labels for the state the item has just entered, through its tracker.
// A label is a signal to people, not a decision: a failure is logged and recorded, never fatal.
func (s *step) label(it item.Item, lane policy.Lane) {
	c := lane.Labels.For(string(it.State))
	if c == nil || len(c.Add)+len(c.Remove) == 0 {
		return
	}
	tr, err := s.e.tracker(it.Ticket)
	if err == nil {
		err = tr.Label(s.ctx, it.Ticket.Key, c.Add, c.Remove)
	}
	s.e.log().Info("action", "item", it.Key, "action", decide.Label, "ok", err == nil, "add", strings.Join(c.Add, ","), "remove", strings.Join(c.Remove, ","))
	s.recordAction(it.Key, ActionRecord{Action: decide.Label, OK: err == nil, Detail: errString(err)})
}

// commentQuietly tells the ticket what an action did. The action itself has succeeded, so a
// comment that fails doesn't undo it; but it is logged, or a ticket that never hears about its
// pull request would go unnoticed.
func (s *step) commentQuietly(it item.Item, kind, body string) {
	if err := s.comment(it, marker(s.id, kind), body); err != nil {
		s.e.log().Warn("ticket comment", "item", it.Key, "action", kind, "err", err)
	}
}

// comment posts on the item's ticket through its tracker.
func (s *step) comment(it item.Item, marker, body string) error {
	tr, err := s.e.tracker(it.Ticket)
	if err != nil {
		return err
	}
	return tr.Comment(s.ctx, it.Ticket.Key, marker, body)
}

func marker(id, what string) string { return fmt.Sprintf("<!-- ynf:%s=%s -->", what, id) }

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 400 {
		return "…" + s[len(s)-400:]
	}
	return s
}

func anyList(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
