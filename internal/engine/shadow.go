package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/eyelock/ynf/internal/facts"
	"github.com/eyelock/ynf/internal/forge"
	"github.com/eyelock/ynf/internal/gate"
	"github.com/eyelock/ynf/internal/item"
	"github.com/eyelock/ynf/internal/policy"
	"github.com/eyelock/ynf/internal/runner"
	"github.com/eyelock/ynf/internal/shadow"
	"github.com/eyelock/ynf/internal/tracker"
)

// ShadowGit is what shadow mode needs from the workspace besides Git: the commit before a merge,
// and the two patches to compare.
type ShadowGit interface {
	FirstParent(ctx context.Context, mirror, sha string) (string, error)
	RangeDiff(ctx context.Context, mirror, from, to string) (string, error)
	Diff(ctx context.Context, wt, base string) (string, error)
}

// ShadowRequest is `ynf shadow run`.
type ShadowRequest struct {
	Lane  string
	Repos []string // owner/name, as Policy takes them; empty: every enrolled repository with the lane
	// Since bounds how recently a candidate was closed; default 90 days.
	Since time.Duration
	// Limit is how many candidates to run per repository; default 20.
	Limit int
	// Tickets names candidates directly, in place of the lane's intake.
	Tickets []tracker.Ref
}

const (
	defaultShadowSince = 90 * 24 * time.Hour
	defaultShadowLimit = 20
)

// openState is a search's open qualifier, which shadow mode turns into closed.
var openState = regexp.MustCompile(`\b(is|state):open\b`)

// ShadowQuery rewrites a lane's intake search to find what it would have taken once closed: open
// becomes closed, and only tickets closed since the given time are kept.
func ShadowQuery(q string, since time.Time) string {
	if openState.MatchString(q) {
		q = openState.ReplaceAllString(q, "${1}:closed")
	} else {
		q += " is:closed"
	}
	return strings.TrimSpace(q) + " closed:>=" + since.UTC().Format("2006-01-02")
}

// shadowTarget is one repository a shadow run works in.
type shadowTarget struct {
	repo, name, host string
	rp               *RepoPolicy
	lane             policy.Lane
	fg               forge.Forge
	fixes            forge.Fixes
	g                Git
	sg               ShadowGit
}

// ShadowRun runs a lane against closed tickets whose answer is known (FR-26): each candidate is
// built into a task as start would build it, run in the lane's own runner and containment on the
// commit before the human fix, and kept beside the human's patch for blind grading. It proposes
// nothing: no push, commit to a remote, pull request, ticket comment or label, memory write, item
// or count in stats. Everything it records is under shadow/<run id>/ in ynf's own store.
func (e *Engine) ShadowRun(ctx context.Context, req ShadowRequest) (shadow.Run, error) {
	if req.Lane == "" {
		return shadow.Run{}, refuse("shadow run needs a lane")
	}
	if req.Since <= 0 {
		req.Since = defaultShadowSince
	}
	if req.Limit <= 0 {
		req.Limit = defaultShadowLimit
	}
	targets, err := e.shadowTargets(ctx, req)
	if err != nil {
		return shadow.Run{}, err
	}
	run := shadow.Run{ID: e.NewID(), Lane: req.Lane, Created: e.Now(), Limit: req.Limit, Pins: map[string]shadow.Pins{}}
	run.Since = req.Since.String()
	since := run.Created.Add(-req.Since)

	type work struct {
		t     *shadowTarget
		cands []int
	}
	var all []work
	for _, t := range targets {
		run.Repos = append(run.Repos, t.repo)
		cands, err := e.shadowCandidates(ctx, t, req, since)
		if err != nil {
			return run, err
		}
		run.Candidates += len(cands)
		all = append(all, work{t, cands})
	}
	if err := shadow.SaveRun(ctx, e.Store, run); err != nil {
		return run, err
	}

	for _, w := range all {
		attempts, err := e.shadowRepo(ctx, &run, w.t, w.cands, req.Limit)
		if len(attempts) > 0 {
			pins := run.Pins[w.t.repo]
			pins.Observe(attempts)
			run.Pins[w.t.repo] = pins
		}
		if err != nil {
			_ = shadow.SaveRun(ctx, e.Store, run)
			return run, err
		}
	}
	return run, shadow.SaveRun(ctx, e.Store, run)
}

// shadowTargets finds the repositories and lanes the request means, and refuses what shadow mode
// cannot read yet.
func (e *Engine) shadowTargets(ctx context.Context, req ShadowRequest) ([]*shadowTarget, error) {
	repos := req.Repos
	explicit := len(repos) > 0
	if !explicit {
		var err error
		if repos, err = e.Enrolled(ctx); err != nil {
			return nil, err
		}
	} else {
		enrolled, err := e.Enrolled(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range repos {
			if !slices.Contains(enrolled, r) {
				return nil, refuse("%s is not an enrolled repository", r)
			}
		}
	}
	var out []*shadowTarget
	for _, repo := range repos {
		rp, err := e.Policy(ctx, repo)
		if err != nil {
			if explicit {
				return nil, refuse("%s's lanes cannot be read: %v", repo, err)
			}
			continue
		}
		lane, ok := rp.File.Lanes[req.Lane]
		if !ok {
			if explicit {
				return nil, refuse("%s has no lane %s; its lanes: %s", repo, req.Lane, strings.Join(rp.File.Names(), ", "))
			}
			continue
		}
		if lane.Kind == "adopt" {
			return nil, refuse("lane %s adopts pull requests; shadow mode takes tickets that were closed by a merged pull request, so it cannot measure an adopting lane yet", lane.Name)
		}
		if !slices.ContainsFunc(lane.Intake, func(in policy.Intake) bool { return in.GitHubSearch != "" }) {
			return nil, refuse("lane %s takes tickets from a search shadow mode cannot read as closed (only github.search intakes can be, for now)", lane.Name)
		}
		t := &shadowTarget{repo: repo, rp: rp, lane: lane}
		t.host, t.name = e.splitRepo(repo)
		if t.fg, _, err = e.forgeFor(repo); err != nil {
			return nil, refuse("%v", err)
		}
		var ok2 bool
		if t.fixes, ok2 = t.fg.(forge.Fixes); !ok2 {
			return nil, refuse("the forge for %s cannot say which pull request closed a ticket", repo)
		}
		t.g = e.gitFor(repo)
		if t.sg, ok2 = t.g.(ShadowGit); !ok2 {
			return nil, refuse("the git workspace for %s cannot compute patches", repo)
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, refuse("no enrolled repository has a lane %s", req.Lane)
	}
	for _, ref := range req.Tickets {
		r, _, err := forge.ParseIssueKey(ref.Key)
		if err != nil || !e.isForge(ref.Host) {
			return nil, refuse("%s: --ticket takes a GitHub issue", ref)
		}
		own := e.qualify(ref.Host, r)
		if !slices.ContainsFunc(out, func(t *shadowTarget) bool { return t.repo == own }) {
			return nil, refuse("%s is in %s, which this shadow run does not cover", ref, own)
		}
	}
	return out, nil
}

// shadowCandidates are the closed tickets the lane's intake would have taken in this repository,
// or the tickets the request names there.
func (e *Engine) shadowCandidates(ctx context.Context, t *shadowTarget, req ShadowRequest, since time.Time) ([]int, error) {
	var nums []int
	add := func(n int) {
		if !slices.Contains(nums, n) {
			nums = append(nums, n)
		}
	}
	if len(req.Tickets) > 0 {
		for _, ref := range req.Tickets {
			if r, n, _ := forge.ParseIssueKey(ref.Key); e.qualify(ref.Host, r) == t.repo {
				add(n)
			}
		}
		return nums, nil
	}
	for _, in := range t.lane.Intake {
		if in.GitHubSearch == "" {
			continue
		}
		hits, err := t.fg.Search(ctx, ShadowQuery(in.GitHubSearch, since))
		if err != nil {
			return nil, err
		}
		for _, h := range hits {
			if _, name := e.splitRepo(h.Repo); name == t.name && !h.IsPR {
				add(h.Number)
			}
		}
	}
	return nums, nil
}

// shadowRepo runs one repository's candidates, up to limit, recording each attempt and each skip.
func (e *Engine) shadowRepo(ctx context.Context, run *shadow.Run, t *shadowTarget, cands []int, limit int) ([]shadow.Attempt, error) {
	var attempts []shadow.Attempt
	if len(cands) == 0 {
		return nil, nil
	}
	// The clone is fetched when the first candidate has a fix to compare with, not before.
	var mirror string
	getMirror := func() (string, error) {
		if mirror != "" {
			return mirror, nil
		}
		var err error
		if mirror, err = t.g.Mirror(ctx, t.repo); err != nil {
			return "", fmt.Errorf("%s: %w", t.repo, err)
		}
		return mirror, nil
	}
	var pin *shadowPin
	defer func() {
		if pin != nil {
			pin.cleanup()
		}
	}()
	for _, n := range cands {
		if len(attempts) >= limit {
			break
		}
		ref := tracker.Ref{Host: t.host, Key: forge.IssueKey(t.name, n)}
		name := fmt.Sprintf("%s#%d", t.repo, n)
		skip := func(format string, args ...any) {
			reason := fmt.Sprintf(format, args...)
			e.log().Info("shadow skipped", "ticket", name, "reason", reason)
			run.Skipped = append(run.Skipped, shadow.Skip{Ticket: name, Reason: reason})
		}
		c, reason, err := e.shadowResolve(ctx, t, getMirror, ref, n)
		if err != nil {
			return attempts, err
		}
		if reason != "" {
			skip("%s", reason)
			continue
		}
		if pin == nil {
			m, err := getMirror()
			if err != nil {
				return attempts, err
			}
			if pin, err = e.shadowPin(ctx, t, m); err != nil {
				return attempts, fmt.Errorf("%s: %w", t.repo, err)
			}
			run.Pins[t.repo] = pin.pins
		}
		a := e.shadowAttempt(ctx, run, t, pin, c)
		if err := shadow.SaveAttempt(ctx, e.Store, a); err != nil {
			return attempts, err
		}
		run.Attempted++
		attempts = append(attempts, a)
		if a.Outcome == runner.OperatorError {
			run.Stopped = fmt.Sprintf("%s ended operator_error (%s); every candidate would end the same way", name, oneLine(a.Detail, 200))
			break
		}
	}
	return attempts, nil
}

// shadowCandidate is a closed ticket resolved to its fix: everything needed to run it.
type shadowCandidate struct {
	ref      tracker.Ref
	number   int
	text     forge.Text
	labels   []string
	fix      forge.Fix
	base     string
	humanDif string
}

// shadowResolve reads a candidate as it was known when it was worked: its text and labels, with
// ynf's own lifecycle labels dropped, and the merged pull request that fixed it. A reason says why
// it is not a candidate after all.
func (e *Engine) shadowResolve(ctx context.Context, t *shadowTarget, getMirror func() (string, error), ref tracker.Ref, n int) (shadowCandidate, string, error) {
	c := shadowCandidate{ref: ref, number: n}
	tr, err := e.tracker(ref)
	if err != nil {
		return c, "", err
	}
	tk, text, err := tr.Get(ctx, ref.Key)
	switch {
	case errors.Is(err, tracker.ErrNotFound):
		return c, "the ticket cannot be found", nil
	case err != nil:
		return c, "", err
	case tk.State != "closed":
		return c, "the ticket is not closed", nil
	}
	c.text, c.labels = text, withoutLifecycleLabels(tk.Labels, t.lane)
	if err := t.lane.CheckLabels(c.labels); err != nil {
		return c, fmt.Sprintf("the lane cannot run it: %v", err), nil
	}
	// The guard reads the ticket as it was when the lane would have met it: open, with the labels
	// people gave it.
	f := facts.Facts{Ticket: &facts.Ticket{Key: ref.Key, Number: n, State: "open", Labels: c.labels}}
	ok, err := policy.Guard(t.lane.Guards.Eligible, f.CEL(), map[string]any{"state": string(item.Intake), "attempts": 0, "lane": t.lane.Name, "counters": map[string]any{}})
	switch {
	case err != nil:
		return c, fmt.Sprintf("the lane's eligibility guard failed: %v", err), nil
	case !ok:
		return c, "the lane would not have taken it (its eligibility guard is false)", nil
	}
	if c.fix, err = t.fixes.FixFor(ctx, t.name, n); errors.Is(err, forge.ErrNoFix) {
		return c, "no merged pull request closed it", nil
	} else if errors.Is(err, forge.ErrRebased) {
		return c, "rebase-merged; base unknown", nil
	} else if errors.Is(err, forge.ErrNotFound) {
		return c, "the fixing pull request was not found", nil
	} else if err != nil {
		return c, "", err
	}
	mirror, err := getMirror()
	if err != nil {
		return c, "", err
	}
	if c.base, err = t.sg.FirstParent(ctx, mirror, c.fix.MergeSHA); err != nil {
		return c, fmt.Sprintf("the fix's merge commit %.7s is not in the clone (a force-pushed or deleted branch): %v", c.fix.MergeSHA, err), nil
	}
	if c.humanDif, err = t.sg.RangeDiff(ctx, mirror, c.base, c.fix.MergeSHA); err != nil {
		return c, "", err
	}
	if strings.TrimSpace(c.humanDif) == "" {
		return c, fmt.Sprintf("pull request #%d changed no files", c.fix.PR), nil
	}
	return c, "", nil
}

// withoutLifecycleLabels drops the labels ynf itself adds to a ticket as it works (a lane's
// labels.on_* add lists): they say what ynf did, not what the ticket was, and a run must read the
// ticket as it was before ynf touched it.
func withoutLifecycleLabels(labels []string, lane policy.Lane) []string {
	var ours []string
	if l := lane.Labels; l != nil {
		for _, c := range []*policy.LabelChange{l.OnClaim, l.OnPropose, l.OnReview, l.OnEscalate, l.OnDone} {
			if c != nil {
				ours = append(ours, c.Add...)
			}
		}
	}
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		if !slices.Contains(ours, l) {
			out = append(out, l)
		}
	}
	return out
}

// shadowPin is what a shadow run holds fixed for a repository's candidates: the lane as resolved
// once (its policy, and the agent image or harness folder built or read once from the default
// branch, never from each candidate's older tree), and the pins it records.
type shadowPin struct {
	lane       policy.Lane
	pins       shadow.Pins
	imageBuilt bool
	cleanup    func()
}

func (e *Engine) shadowPin(ctx context.Context, t *shadowTarget, mirror string) (*shadowPin, error) {
	lane := t.lane
	p := &shadowPin{lane: lane, cleanup: func() {}}
	ex, err := e.Executor(lane.Run.Executor)
	if err != nil {
		return nil, err
	}
	p.pins = shadow.Pins{Lane: lane.Name, PolicyHash: lane.Hash(), PolicySHA: t.rp.SHA, ConfigSHA: configSHA(t.rp), Runner: lane.Run.Runner, Executor: ex.Name(), Image: lane.Run.Image}
	y := lane.Run.Ynh
	if y == nil {
		return p, nil
	}
	p.pins.Model, p.pins.Effort = y.Model, y.Effort
	if !ex.Contained() && ex.Name() != "inline" {
		p.pins.HostAutoApprove = e.HostAutoApprove
	}
	if ex.Name() == "inline" {
		return p, nil // the harness installed here, which no candidate changes
	}
	// The harness is the instrument: build or read it once from the default branch, then run every
	// historical tree with it, as the method says.
	dir := filepath.Join(e.WorkDir, "shadow", "pin-"+strings.ReplaceAll(t.repo, "/", "_")+"-"+e.NewID())
	needsFolder := (ex.Contained() && lane.Run.Image == "" && e.BuildImage != nil) || (!ex.Contained() && maybeFolder(y.Harness))
	if !needsFolder {
		return p, nil
	}
	wt, err := t.g.Checkout(ctx, mirror, t.rp.Base, dir)
	if err != nil {
		return nil, fmt.Errorf("check out %s to pin the harness: %w", t.rp.Base, err)
	}
	p.cleanup = func() { _ = t.g.RemoveCheckout(wt) }
	if ex.Contained() {
		img, err := e.BuildImage(ctx, wt, *y)
		if err != nil {
			p.cleanup()
			return nil, fmt.Errorf("build the agent image to pin: %w", err)
		}
		p.lane.Run.Image, p.imageBuilt, p.pins.Image = img, true, img
		p.cleanup()
		p.cleanup = func() {}
		return p, nil
	}
	if !isFolder(wt, y.Harness) {
		return p, nil // an installed harness id, which ynh resolves the same for every run
	}
	pinned := *y
	pinned.Harness = filepath.Join(wt, filepath.FromSlash(y.Harness))
	p.lane.Run.Ynh = &pinned
	p.pins.Harness = y.Harness
	return p, nil
}

// maybeFolder reports whether a harness setting may be a folder in the repository rather than an
// installed harness id (which carries an @ version); the checkout settles it.
func maybeFolder(h string) bool { return h != "" && !strings.Contains(h, "@") }

// shadowAttempt runs one candidate on the commit before its fix, in the lane's own runner and
// executor, and keeps what came out beside the human's patch. Nothing leaves the machine.
func (e *Engine) shadowAttempt(ctx context.Context, run *shadow.Run, t *shadowTarget, pin *shadowPin, c shadowCandidate) shadow.Attempt {
	id := e.NewID()
	a := shadow.Attempt{
		ID: id, Run: run.ID, Repo: t.repo, Ticket: fmt.Sprintf("%s#%d", t.repo, c.number), FixPR: c.fix.PR, MergeSHA: c.fix.MergeSHA, Base: c.base,
		HumanPatch: c.humanDif, AgentIsA: shadow.NewOrder(), Started: e.Now(), Title: c.text.Title, Body: c.text.Body,
	}
	it := item.Item{
		Key: fmt.Sprintf("shadow/%s/%s#%d", run.ID, t.repo, c.number), Kind: "originate", Lane: t.lane.Name, Ticket: c.ref,
		Forge: t.host, Repo: t.name, Number: c.number, State: item.Intake, Created: e.Now(), Updated: e.Now(),
	}
	s := &step{e: e, id: id, ctx: ctx, g: t.g, quiet: true, text: c.text, labels: c.labels, labelsSet: true, imageBuilt: pin.imageBuilt}
	defer s.cleanup()
	rp := *t.rp
	rp.Base = c.base // the checkout is the commit before the fix
	e.log().Info("shadow attempt", "ticket", a.Ticket, "fix", c.fix.PR, "base", c.base)
	s.runLane(it, &rp, pin.lane, "")
	rec := s.run
	if rec == nil {
		a.Outcome = runner.Error
		return a
	}
	a.Outcome, a.Detail, a.Exit, a.Duration, a.Model, a.Changed, a.Usage, a.RunDir = rec.Outcome, rec.Detail, rec.Exit, rec.Duration, rec.Model, rec.Changed, rec.Usage, rec.StepDir
	switch err := gate.Check(rec.Changed, t.lane.PR.AllowedPaths, t.lane.PR.ProtectedPaths); {
	case len(rec.Changed) == 0:
		a.GateDetail = "no change to propose"
	case err != nil:
		a.GateDetail = err.Error()
	default:
		a.GateAccepted = true
	}
	if s.wt != "" {
		if patch, err := t.sg.Diff(ctx, s.wt, c.base); err == nil {
			a.AgentPatch = patch
		} else {
			e.log().Warn("shadow patch", "ticket", a.Ticket, "err", err)
		}
	}
	if a.RunDir != "" {
		_ = os.WriteFile(filepath.Join(a.RunDir, "shadow-agent.patch"), []byte(a.AgentPatch), 0o644)
		_ = os.WriteFile(filepath.Join(a.RunDir, "shadow-human.patch"), []byte(a.HumanPatch), 0o644)
	}
	return a
}
