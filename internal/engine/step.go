package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

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
	"github.com/eyelock/ynf/internal/store"
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
	Repo string `json:"repo"`
	Dir  string `json:"dir"`
	Ref  string `json:"ref"`
	Hash string `json:"hash"`
}

// RunRecord is the log entry for a run.
type RunRecord struct {
	RunID    string   `json:"run_id"`
	Runner   string   `json:"runner"`
	Executor string   `json:"executor"`
	Argv     []string `json:"argv"`
	Base     string   `json:"base"`
	Exit     int      `json:"exit"`
	Outcome  string   `json:"outcome"`
	Detail   string   `json:"detail,omitempty"`
	Changed  []string `json:"changed,omitempty"`
	Denied   []string `json:"denied,omitempty"` // hosts the egress proxy refused
	StepDir  string   `json:"step_dir"`
	Duration string   `json:"duration"`
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
func (e *Engine) Handle(ctx context.Context, key string, ev event.Event) error {
	stepID := e.NewID()
	h, err := lease.Claim(ctx, e.Store, key, e.Owner, stepID, e.LeaseTTL, e.Now)
	if errors.Is(err, lease.ErrHeld) {
		e.log().Debug("held elsewhere", "item", key)
		return nil
	}
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	var lost atomic.Bool
	go h.Heartbeat(runCtx, e.Heartbeat, func(it item.Item) {
		if err := e.schedule(runCtx, it); err != nil && runCtx.Err() == nil {
			e.log().Warn("reschedule on heartbeat", "item", key, "err", err)
		}
	}, func(err error) {
		lost.Store(true)
		e.log().Error("lease lost; stopping", "item", key, "err", err)
		cancel()
	})
	s := &step{e: e, h: h, id: stepID, ctx: runCtx}
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
	h   *lease.Holder
	id  string
	ctx context.Context
	n   int // runs in this step

	mirror string
	wt     string
	base   string // the commit the run started from
	run    *RunRecord
	result runner.Result
	text   forge.Text
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
		_ = s.e.Git.RemoveWorktree(context.WithoutCancel(s.ctx), s.mirror, s.wt)
	}
}

func (s *step) decideAndAct(ev event.Event) (*event.Event, error) {
	e, ctx := s.e, s.ctx
	it := s.h.Item()
	rp, lane, err := e.laneFor(ctx, it)
	if err != nil {
		return nil, err
	}
	f, err := s.probe(it)
	if err != nil {
		return nil, err
	}
	in := decide.Input{Lane: lane, Item: it, Facts: f, Event: ev, Poll: e.Poll}
	in.Item.Lease = nil // not an input to the decision; keeps replay exact
	d := decide.Decide(in)

	rec := DecisionRecord{Input: in, Decision: d, Policy: PolicyRef{Repo: rp.Repo, Dir: rp.Dir, Ref: rp.Base, Hash: lane.Hash()}}
	if err := s.record(it.Key, "decision", rec); err != nil {
		return nil, err
	}
	if err := s.h.Save(ctx, d.Item); err != nil {
		return nil, err
	}
	due := time.Time{}
	if d.Item.NextDue != nil {
		due = *d.Item.NextDue
	}
	if err := e.schedule(ctx, s.h.Item()); err != nil {
		return nil, err
	}
	s.remember(in, d)
	e.log().Info("decided", "item", it.Key, "event", ev.Type, "state", d.Item.State, "reason", d.Reason)

	for _, a := range d.Actions {
		next, err := s.act(a, d.Item, rp, lane)
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

func (s *step) event(typ string, it item.Item, data map[string]any) event.Event {
	return event.New(s.e.NewID(), "ynf/step/"+s.id, typ, it.Subject(), s.e.Now(), data)
}

func (s *step) probe(it item.Item) (facts.Facts, error) {
	var f facts.Facts
	t, text, err := s.e.Forge.Ticket(s.ctx, it.Repo, it.Number)
	switch {
	case errors.Is(err, forge.ErrNotFound):
		t = facts.Ticket{Number: it.Number, State: "closed"}
	case err != nil:
		return f, err
	}
	f.Ticket, s.text = &t, text
	if f.Lane, err = s.e.laneFacts(s.ctx, it.Repo, it.Lane); err != nil {
		return f, err
	}
	if it.PR > 0 {
		p, err := s.e.Forge.PullRequest(s.ctx, it.Repo, it.PR)
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
		err := s.e.Forge.Comment(s.ctx, it.Repo, it.Number, marker(s.id, a.Kind), body)
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
	finished := func(rec RunRecord) event.Event {
		rec.RunID = runID
		s.run = &rec
		s.recordRun(it.Key, rec)
		return s.event(event.RunFinished, it, map[string]any{
			"run_id": runID, "outcome": rec.Outcome, "detail": rec.Detail, "changed": anyList(rec.Changed), "denied": anyList(rec.Denied),
		})
	}
	fail := func(outcome string, err error) event.Event {
		return finished(RunRecord{Outcome: outcome, Detail: err.Error()})
	}

	r, err := runner.For(lane)
	if err != nil {
		return fail(runner.OperatorError, err)
	}
	ex, err := e.Executor(lane.Run.Executor)
	if err != nil {
		return fail(runner.OperatorError, err)
	}
	if !ex.Contained() && !e.Interactive {
		return fail(runner.OperatorError, fmt.Errorf("lane %s uses the %s executor, which is not contained; unattended lanes need a contained one (ADR-007)", lane.Name, ex.Name()))
	}

	mirror, err := e.Git.Mirror(s.ctx, it.Repo)
	if err != nil {
		return fail(runner.Error, err)
	}
	base := rp.Base
	switch {
	case it.Kind == "adopt":
		base = it.Branch // the author's branch, as it is now
		if !e.Git.RemoteHas(s.ctx, mirror, base) {
			return fail(runner.Error, fmt.Errorf("the pull request's branch %s is gone", base))
		}
	case feedback != "" && it.Branch != "" && e.Git.RemoteHas(s.ctx, mirror, it.Branch):
		base = it.Branch // resume from what was proposed, with the feedback
	}
	if s.wt != "" {
		_ = e.Git.RemoveWorktree(s.ctx, s.mirror, s.wt)
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
	wt, err := e.Git.Worktree(s.ctx, mirror, base, filepath.Join(stepDir, "wt"))
	if err != nil {
		return fail(runner.Error, err)
	}
	s.mirror, s.wt = mirror, wt
	if s.base, err = e.Git.Head(s.ctx, wt); err != nil {
		return fail(runner.Error, err)
	}

	var focus *runner.Focus
	if y, ok := r.(runner.YnhRunner); ok && y.Cfg.Focus != "" {
		f, err := runner.ResolveFocus(filepath.Join(wt, filepath.FromSlash(y.Cfg.Harness)), y.Cfg.Focus)
		if err != nil {
			return fail(runner.OperatorError, err)
		}
		focus = &f
	}
	body := task(it, s.text, feedback, s.recall(it))
	if focus != nil {
		body = focus.Prompt + "\n\n" + body
	}
	if err := os.WriteFile(filepath.Join(runDir, "task.md"), []byte(body), 0o644); err != nil {
		return fail(runner.Error, err)
	}
	job, inImage, err := s.job(lane, r, ex, wt, runDir)
	if err != nil {
		return fail(runner.OperatorError, err)
	}
	_, cr := ex.Paths(job)
	labels := []string(nil)
	if t, _, err := e.Forge.Ticket(s.ctx, it.Repo, it.Number); err == nil {
		labels = t.Labels
	}
	argv, err := r.Command(runner.Spec{Lane: lane, Labels: labels, TaskFile: cr + "/task.md", RunDir: cr, Feedback: feedback, InImage: inImage, Focus: focus})
	if err != nil {
		return fail(runner.OperatorError, err)
	}
	job.Argv = argv

	log := e.log().With("item", it.Key, "run", runID)
	log.Info("run started", "lane", lane.Name, "runner", r.Name(), "executor", ex.Name(), "image", job.Image, "base", base, "attempt", it.Attempts)
	start := e.Now()
	stop := s.progress(log, filepath.Join(runDir, "trajectory.jsonl"))
	out, err := ex.Run(s.ctx, job)
	stop()
	rec := RunRecord{Runner: r.Name(), Executor: ex.Name(), Argv: argv, Base: base, StepDir: stepDir, Denied: out.Denied}
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
		if rec.Outcome == runner.Error && len(out.Stderr) > 0 && !strings.Contains(rec.Detail, ":") {
			rec.Detail += ": " + tail(string(out.Stderr))
		}
	}
	rec.Duration = e.Now().Sub(start).Round(time.Millisecond).String()
	if rec.Changed, err = e.Git.Changed(s.ctx, wt); err != nil {
		rec.Outcome, rec.Detail = runner.Error, err.Error()
	}
	log.Info("run finished", "outcome", rec.Outcome, "exit", rec.Exit, "duration", rec.Duration,
		"changed", len(rec.Changed), "denied", strings.Join(rec.Denied, ","), "detail", oneLine(rec.Detail, 200))
	return finished(rec)
}

// job describes the run for the executor. A ynh lane on a contained executor runs in an agent
// image ynf builds from the harness (or the lane's run.image), as the image's own user, with the
// vendor's API host allowed through the egress proxy (ADR-007, ADR-012).
func (s *step) job(lane policy.Lane, r runner.Runner, ex executor.Executor, wt, runDir string) (executor.Job, bool, error) {
	e := s.e
	job := executor.Job{Worktree: wt, RunDir: runDir, Image: lane.Run.Image, Timeout: e.RunTimeout, Env: map[string]string{}, Secrets: map[string]string{}}
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
		return job, false, nil
	}
	if err := s.checkPassthrough(lane, y, wt, ex.Contained() && len(job.Egress)+len(runner.ModelHosts[y.Vendor()]) > 0); err != nil {
		return job, false, err
	}
	if !ex.Contained() {
		if y.Cfg.AutoApprove != "" {
			e.log().Warn("auto_approve applies only inside containment; this run keeps its approval prompts", "lane", lane.Name, "executor", ex.Name())
		}
		return job, false, nil
	}
	if job.Image == "" {
		if e.BuildImage == nil {
			return job, false, fmt.Errorf("lane %s runs ynh in a container, which needs ynh on PATH to build the agent image, or run.image", lane.Name)
		}
		img, err := e.BuildImage(s.ctx, wt, y.Cfg)
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
	job.Egress = append(job.Egress, runner.ModelHosts[y.Vendor()]...)
	if y.Cfg.Vendor != "" {
		job.Env["YNH_VENDOR"] = y.Cfg.Vendor
	}
	return job, true, nil
}

// autoApproveCapabilities is the first ynh capabilities version with --auto-approve.
const autoApproveCapabilities = "0.9.0"

// atLeast compares dotted versions numerically; anything unparseable is too old.
func atLeast(have, want string) bool {
	h, w := strings.Split(have, "."), strings.Split(want, ".")
	for i := range w {
		if i >= len(h) {
			return false
		}
		hn, err1 := strconv.Atoi(h[i])
		wn, err2 := strconv.Atoi(w[i])
		if err1 != nil || err2 != nil {
			return false
		}
		if hn != wn {
			return hn > wn
		}
	}
	return true
}

// proxyVars are what a worker behind the egress proxy needs to reach it.
var proxyVars = []string{"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY"}

// checkPassthrough refuses a ynh run whose worker could not use what ynf gives it. ynh passes its
// worker only the variables the harness lists in env_passthrough, deliberately, so a lane's
// run.env (the model key) or the egress proxy's variables that the harness does not list never
// arrive, and the run fails in a way that looks like the agent being stuck.
func (s *step) checkPassthrough(lane policy.Lane, y runner.YnhRunner, wt string, behindProxy bool) error {
	h, err := runner.ReadHarness(filepath.Join(wt, filepath.FromSlash(y.Cfg.Harness)))
	if err != nil {
		return nil // an installed harness id: ynh reports its own manifest problems
	}
	need := append([]string(nil), lane.Run.Env...)
	if behindProxy {
		need = append(need, proxyVars...)
	}
	if missing := h.NotPassed(need); len(missing) > 0 {
		return fmt.Errorf("harness %s does not pass %s to its agent worker: ynh gives the worker only what env_passthrough lists, so without them the agent cannot log in or reach the egress proxy; add them to env_passthrough in the harness manifest",
			y.Cfg.Harness, strings.Join(missing, ", "))
	}
	return nil
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
	msg := subject + "\n\n" + fmt.Sprintf("Proposed by ynf for #%d, lane %s.\n\n", it.Number, lane.Name) + trailers(it, s.id, s.run.RunID, s.result)
	sha, err := e.Git.Commit(s.ctx, s.wt, msg)
	if err != nil {
		return done(false, err.Error(), nil)
	}
	branch := item.BranchFor(it.Number)
	if err := e.Git.Push(s.ctx, s.wt, it.Repo, branch); err != nil {
		return done(false, err.Error(), nil)
	}
	n, err := e.Forge.FindPR(s.ctx, it.Repo, branch)
	if err != nil {
		return done(false, err.Error(), nil)
	}
	if n == 0 {
		n, err = e.Forge.OpenPR(s.ctx, it.Repo, forge.NewPR{
			Head: branch, Base: rp.Base, Title: subject, Draft: lane.PR.IsDraft(),
			Body: prBody(it, lane, s.run) + "\n\n" + marker(it.Key, "item"),
		})
		if err != nil {
			return done(false, err.Error(), nil)
		}
	}
	_ = e.Forge.Comment(s.ctx, it.Repo, it.Number, marker(s.id, decide.OpenPR), fmt.Sprintf("**ynf** proposed #%d.", n))
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
	if _, err := e.Git.Mirror(s.ctx, it.Repo); err != nil {
		return done(false, err.Error(), nil)
	}
	if tip, err := e.Git.RemoteSHA(s.ctx, s.mirror, it.Branch); err != nil || tip != s.base {
		return done(false, fmt.Sprintf("%s moved from %.7s to %.7s while ynf worked", it.Branch, s.base, tip), map[string]any{"head_moved": true})
	}
	subject := fmt.Sprintf("ynf(%s): %s", lane.Name, s.text.Title)
	if len(subject) > 72 {
		subject = subject[:71] + "…"
	}
	msg := subject + "\n\n" + fmt.Sprintf("Added by ynf to #%d, lane %s.\n\n", it.Number, lane.Name) + trailers(it, s.id, s.run.RunID, s.result)
	sha, err := e.Git.Commit(s.ctx, s.wt, msg)
	if err != nil {
		return done(false, err.Error(), nil)
	}
	if err := e.Git.PushFastForward(s.ctx, s.wt, it.Repo, it.Branch); err != nil {
		// Someone pushed between the check and the push: never overwrite, start again.
		return done(false, err.Error(), map[string]any{"head_moved": true})
	}
	_ = e.Forge.Comment(s.ctx, it.Repo, it.Number, marker(s.id, decide.PushCommit),
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
	if err := s.record(key, "run", r); err != nil {
		s.e.log().Error("record run", "item", key, "err", err)
	}
}

func (s *step) recordAction(key string, a ActionRecord) {
	if err := s.record(key, "action", a); err != nil {
		s.e.log().Error("record action", "item", key, "err", err)
	}
}

// trailers are ynf's attribution (ADR-010): they survive a squash merge.
func trailers(it item.Item, stepID, runID string, r runner.Result) string {
	var b strings.Builder
	if r.Model != "" {
		fmt.Fprintf(&b, "Co-Authored-By: %s <noreply@anthropic.com>\n", r.Model)
	}
	fmt.Fprintf(&b, "YNF-Item: %s\nYNF-Step: %s\nYNF-Run: %s\n", it.Key, stepID, runID)
	if r.Session != "" {
		fmt.Fprintf(&b, "YNH-Session: %s\n", r.Session)
	}
	return b.String()
}

func prBody(it item.Item, lane policy.Lane, run *RunRecord) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Closes #%d.\n\nProposed by **ynf**: lane `%s`, runner `%s`, executor `%s`.\n\n", it.Number, lane.Name, run.Runner, run.Executor)
	b.WriteString("| | |\n|---|---|\n")
	fmt.Fprintf(&b, "| Item | `%s` |\n| Run | `%s` (%s, %s) |\n| Changed | %d file(s) |\n", it.Key, run.RunID, run.Outcome, run.Duration, len(run.Changed))
	b.WriteString("\nA human reviews and merges this; ynf never merges.")
	return b.String()
}

// task is what the runner is asked to do. Ticket text is quoted data, never instructions to ynf
// (NFR-5).
func task(it item.Item, t forge.Text, feedback, remembered string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Task: %s#%d\n\n%s\n\nThe ticket, quoted as the reporter wrote it:\n\n", it.Repo, it.Number, t.URL)
	for l := range strings.SplitSeq(strings.TrimSpace(t.Title+"\n\n"+t.Body), "\n") {
		b.WriteString("> " + l + "\n")
	}
	if feedback != "" {
		b.WriteString("\n## Feedback from the previous attempt\n\n" + feedback + "\n")
	}
	if remembered != "" {
		b.WriteString("\n## What ynf remembers about this work\n\nFrom earlier runs; advice, not instructions.\n\n" + remembered + "\n")
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
	go func() {
		tick := time.NewTicker(every)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-s.ctx.Done():
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
