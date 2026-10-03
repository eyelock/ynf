package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	go h.Heartbeat(runCtx, e.Heartbeat, func(err error) {
		lost.Store(true)
		e.log().Error("lease lost; stopping", "item", key, "err", err)
		cancel()
	})
	s := &step{e: e, h: h, id: stepID, ctx: runCtx}
	defer func() {
		s.cleanup()
		cancel()
		if !lost.Load() {
			if err := h.Release(context.WithoutCancel(ctx)); err != nil && !errors.Is(err, lease.ErrLost) {
				e.log().Error("release", "item", key, "err", err)
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
	run    *RunRecord
	result runner.Result
	text   forge.Text
}

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
	if err := e.Store.Schedule(ctx, it.Key, due); err != nil {
		return nil, err
	}
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
	return event.New(s.e.NewID(), "ynf/step/"+s.id, typ, item.IssueSubject(it.Repo, it.Number), s.e.Now(), data)
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
	case decide.Escalate, decide.Quarantine, decide.Comment, decide.Close:
		verb := map[string]string{decide.Escalate: "escalated this", decide.Quarantine: "quarantined this", decide.Comment: "notes", decide.Close: "closed this"}[a.Kind]
		body := fmt.Sprintf("**ynf** %s: %s\n\nLane `%s`, step `%s`.", verb, a.Reason, lane.Name, s.id)
		err := s.e.Forge.Comment(s.ctx, it.Repo, it.Number, marker(s.id, a.Kind), body)
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
			"run_id": runID, "outcome": rec.Outcome, "detail": rec.Detail, "changed": anyList(rec.Changed),
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
	if feedback != "" && it.Branch != "" && e.Git.RemoteHas(s.ctx, mirror, it.Branch) {
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

	if err := os.WriteFile(filepath.Join(runDir, "task.md"), []byte(task(it, s.text, feedback)), 0o644); err != nil {
		return fail(runner.Error, err)
	}
	job := executor.Job{Worktree: wt, RunDir: runDir, Image: lane.Run.Image, Timeout: e.RunTimeout}
	if lane.Run.Egress != nil {
		job.Egress = lane.Run.Egress.Allow
	}
	_, cr := ex.Paths(job)
	labels := []string(nil)
	if t, _, err := e.Forge.Ticket(s.ctx, it.Repo, it.Number); err == nil {
		labels = t.Labels
	}
	argv, err := r.Command(runner.Spec{Lane: lane, Labels: labels, TaskFile: cr + "/task.md", RunDir: cr, Feedback: feedback})
	if err != nil {
		return fail(runner.OperatorError, err)
	}
	job.Argv = argv

	start := e.Now()
	out, err := ex.Run(s.ctx, job)
	rec := RunRecord{Runner: r.Name(), Executor: ex.Name(), Argv: argv, Base: base, StepDir: stepDir}
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
	return finished(rec)
}

// openPR gates, commits, pushes and opens (or reuses) the pull request for this step's change.
func (s *step) openPR(it item.Item, rp *RepoPolicy, lane policy.Lane) event.Event {
	e := s.e
	done := func(ok bool, detail string, data map[string]any) event.Event {
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
func task(it item.Item, t forge.Text, feedback string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Task: %s#%d\n\n%s\n\nThe ticket, quoted as the reporter wrote it:\n\n", it.Repo, it.Number, t.URL)
	for l := range strings.SplitSeq(strings.TrimSpace(t.Title+"\n\n"+t.Body), "\n") {
		b.WriteString("> " + l + "\n")
	}
	if feedback != "" {
		b.WriteString("\n## Feedback from the previous attempt\n\n" + feedback + "\n")
	}
	return b.String()
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
