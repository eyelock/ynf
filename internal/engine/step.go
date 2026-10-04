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
	s := &step{e: e, h: h, id: stepID, ctx: runCtx, g: e.gitFor(e.repoOf(h.Item()))}
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
		_ = s.g.RemoveWorktree(context.WithoutCancel(s.ctx), s.mirror, s.wt)
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

	rec := DecisionRecord{Input: in, Decision: d, Policy: PolicyRef{Repo: rp.Repo, Dir: rp.Dir, Ref: rp.Base, SHA: rp.SHA, Config: configRepo(rp), ConfigSHA: configSHA(rp), Hash: lane.Hash()}}
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
	if d.Item.State != it.State {
		s.label(d.Item, lane)
	}

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
	finished := func(rec RunRecord) event.Event {
		rec.RunID = runID
		s.run = &rec
		s.recordRun(it.Key, rec)
		return s.event(event.RunFinished, it, map[string]any{
			"run_id": runID, "outcome": rec.Outcome, "detail": rec.Detail, "changed": anyList(rec.Changed), "denied": anyList(rec.Denied),
		})
	}
	// A run refused or broken before it starts still says what it would have used: the lane's
	// runner and executor, until the executor is built and names itself.
	runnerName, executorName := lane.Run.Runner, lane.Run.Executor
	fail := func(outcome string, err error) event.Event {
		return finished(RunRecord{Runner: runnerName, Executor: executorName, Outcome: outcome, Detail: err.Error()})
	}

	r, err := runner.For(lane)
	if err != nil {
		return fail(runner.OperatorError, err)
	}
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
		_ = s.g.RemoveWorktree(s.ctx, s.mirror, s.wt)
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
	wt, err := s.g.Worktree(s.ctx, mirror, base, filepath.Join(stepDir, "wt"))
	if err != nil {
		return fail(runner.Error, err)
	}
	s.mirror, s.wt = mirror, wt
	if s.base, err = s.g.Head(s.ctx, wt); err != nil {
		return fail(runner.Error, err)
	}

	job, inImage, err := s.job(lane, r, ex, wt, runDir)
	if err != nil {
		return fail(runner.OperatorError, err)
	}
	inline := ex.Name() == "inline"
	if inline {
		job.Image = ""                        // the run is in this image, whatever the lane names for a container
		job.Share = append(job.Share, mirror) // the worktree's git data lives in the mirror
		// The job runner's network policy enforces egress here, not ynf (ADR-007): say what the
		// lane expects, so a mismatch is visible.
		e.log().Info("egress is the job runner's", "item", it.Key, "lane", lane.Name, "expects", strings.Join(job.Egress, ","))
	}
	// The harness the lane is held to is the one that will run: inside the image, or in the
	// folder ynh runs on the host (ADR-012).
	var focus *runner.Focus
	if y, ok := r.(runner.YnhRunner); ok {
		h, known, err := s.harness(y, job.Image, inImage, lane.Run.Image == "", inline, wt)
		if err != nil {
			return fail(runner.OperatorError, err)
		}
		if inline && h.ID != "" {
			y.Cfg.Harness = h.ID // run the harness installed here, by its id
			r = y
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
	labels := []string(nil)
	if tr, err := e.tracker(it.Ticket); err == nil {
		if t, _, err := tr.Get(s.ctx, it.Ticket.Key); err == nil {
			labels = t.Labels
		}
	}
	argv, err := r.Command(runner.Spec{Lane: lane, Labels: labels, TaskFile: cr + "/task.md", RunDir: cr, Feedback: feedback, InImage: inImage, Contained: ex.Contained(), Focus: focus, HostAutoApprove: e.HostAutoApprove})
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
		rec.Model, rec.Usage = s.result.Model, s.result.Usage
		if rec.Outcome == runner.Error && len(out.Stderr) > 0 && !strings.Contains(rec.Detail, ":") {
			rec.Detail += ": " + tail(string(out.Stderr))
		}
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
	if job.Image == "" {
		if e.BuildImage == nil {
			return job, false, fmt.Errorf("lane %s names no published image (run.image), and this instance does not build one: that needs ynh on PATH and images.build not false", lane.Name)
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
	h, err = runner.ReadHarness(filepath.Join(wt, filepath.FromSlash(y.Cfg.Harness)))
	return h, err == nil, nil
}

// isFolder reports whether name is a folder in the worktree, rather than an installed harness id.
func isFolder(wt, name string) bool {
	if name == "" || strings.Contains(name, "@") {
		return false
	}
	fi, err := os.Stat(filepath.Join(wt, filepath.FromSlash(name)))
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
	_ = s.comment(it, marker(s.id, decide.OpenPR), "**ynf** proposed "+prLink(it, n)+".")
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
	_ = s.comment(it, marker(s.id, decide.PushCommit),
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
