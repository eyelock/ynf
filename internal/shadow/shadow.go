// Package shadow is shadow mode's record and arithmetic (FR-26, ADR-011): what a shadow run
// pinned, what each attempt produced beside the human's patch, how a person graded the two blind,
// and the yield those grades give, with its confidence interval. Running the lane is the engine's;
// this package only keeps and reads the evidence, in ynf's own store under shadow/<run id>/, so it
// works on every store provider and never touches an item, a ticket or memory.
package shadow

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/eyelock/ynf/internal/runner"
	"github.com/eyelock/ynf/internal/store"
)

// Grades are ynh's five (its shadow-mode tutorial), the rubric a patch is graded against.
const (
	Equivalent    = "equivalent"
	DifferentOK   = "different-valid"
	Superficial   = "superficial"
	Wrong         = "wrong"
	DoesNotBuild  = "does-not-build"
	storePrefix   = "shadow/"
	runSuffix     = "/run"
	attemptPrefix = "/attempts/"
	gradePrefix   = "/grades/"
)

// AllGrades lists the grades in the order a grader is offered them.
var AllGrades = []string{Equivalent, DifferentOK, Superficial, Wrong, DoesNotBuild}

// ValidGrade reports whether g is one of the five.
func ValidGrade(g string) bool { return slices.Contains(AllGrades, g) }

// Success reports whether a grade counts toward yield: the patch fixes the defect, by the same
// means or by others.
func Success(g string) bool { return g == Equivalent || g == DifferentOK }

// Skip is a candidate that was not attempted, and why.
type Skip struct {
	Ticket string `json:"ticket"`
	Reason string `json:"reason"`
}

// Pins are what a shadow run held fixed for every candidate, so the sample is one experiment
// (the method's "pin the harness, do not inherit it"). The first group is resolved once before
// the first run; the observed group is what the runs themselves reported, and Varied names any
// that changed between attempts, which would make the sample two experiments.
type Pins struct {
	Lane       string `json:"lane"`
	PolicyHash string `json:"policy_hash"`
	PolicySHA  string `json:"policy_sha,omitempty"` // the repository's commit its lanes were read at
	ConfigSHA  string `json:"config_sha,omitempty"`
	Runner     string `json:"runner"`
	Executor   string `json:"executor"`
	// Image is the agent image every run used: the lane's own, or the one built once from the
	// default branch's harness. Empty for a run on the host or inline, which has no image.
	Image string `json:"image,omitempty"`
	// Harness is the harness folder every run used, as a path in the default branch's checkout,
	// when the lane runs one from the repository on the host.
	Harness string `json:"harness,omitempty"`
	Model   string `json:"model,omitempty"`  // as the lane configures it; empty is the vendor's default
	Effort  string `json:"effort,omitempty"` // as the lane configures it

	Observed Observed `json:"observed"`
	Varied   []string `json:"varied,omitempty"`
}

// Observed is what the runs reported having run on.
type Observed struct {
	Harness    string `json:"harness,omitempty"` // name@version
	HarnessSHA string `json:"harness_sha,omitempty"`
	Ynh        string `json:"ynh_version,omitempty"`
	Model      string `json:"model,omitempty"`
	Effort     string `json:"effort,omitempty"`
}

// Observe fills in what the attempts reported, and names each thing that was not the same in all
// of them.
func (p *Pins) Observe(attempts []Attempt) {
	p.Observed, p.Varied = Observed{}, nil
	vary := map[string]map[string]bool{}
	see := func(field, v string, dst *string) {
		if v == "" {
			return
		}
		if vary[field] == nil {
			vary[field] = map[string]bool{}
		}
		vary[field][v] = true
		if *dst == "" {
			*dst = v
		}
	}
	for _, a := range attempts {
		see("harness", a.Harness, &p.Observed.Harness)
		see("harness_sha", a.HarnessSHA, &p.Observed.HarnessSHA)
		see("ynh_version", a.RunnerVersion, &p.Observed.Ynh)
		see("model", a.Model, &p.Observed.Model)
		see("effort", firstOf(a.Effort, a.EffortRequested), &p.Observed.Effort)
	}
	for _, f := range []string{"harness", "harness_sha", "ynh_version", "model", "effort"} {
		if len(vary[f]) > 1 {
			p.Varied = append(p.Varied, f)
		}
	}
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Run is one `ynf shadow run`.
type Run struct {
	ID      string    `json:"id"` // ULID
	Lane    string    `json:"lane"`
	Repos   []string  `json:"repos"`
	Created time.Time `json:"created"`
	Since   string    `json:"since,omitempty"`
	Limit   int       `json:"limit,omitempty"`
	// Pins are what each repository's runs held fixed, by repository.
	Pins map[string]Pins `json:"pins"`
	// Candidates is how many closed tickets the lane would have taken; Attempted how many of
	// them ran (the limit, or a fix that could not be built from, leave some out).
	Candidates int    `json:"candidates"`
	Attempted  int    `json:"attempted"`
	Skipped    []Skip `json:"skipped,omitempty"`
	// Stopped says why the run ended before its candidates ran out, when it did: a run that
	// cannot start (a missing executor, an image that will not build) would fail every candidate
	// the same way, so it stops after the first.
	Stopped string `json:"stopped,omitempty"`
}

// Attempt is one candidate run against its fix's base: everything needed to grade it and to
// report on it, kept whole so a later reader needs nothing but the store.
type Attempt struct {
	ID       string `json:"id"` // ULID
	Run      string `json:"run"`
	Repo     string `json:"repo"`
	Ticket   string `json:"ticket"` // owner/name#number
	FixPR    int    `json:"fix_pr"`
	MergeSHA string `json:"merge_sha"`
	Base     string `json:"base"` // the commit before the human fix
	// Title and Body are the ticket as the agent was given it, for the grader to read.
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`

	Outcome  string   `json:"outcome"`
	Detail   string   `json:"detail,omitempty"`
	Exit     int      `json:"exit"`
	Duration string   `json:"duration,omitempty"`
	Model    string   `json:"model,omitempty"`
	Changed  []string `json:"changed,omitempty"`
	runner.Usage
	// GateAccepted is whether the lane's diff gate would have let the change through to a pull
	// request; GateDetail says why not.
	GateAccepted bool   `json:"gate_accepted"`
	GateDetail   string `json:"gate_detail,omitempty"`

	AgentPatch string `json:"agent_patch"`
	HumanPatch string `json:"human_patch"`
	RunDir     string `json:"run_dir,omitempty"` // the run's folder: task, stdout, stderr, trajectory
	// AgentIsA is the blind order, drawn when the attempt is recorded: which of the two patches a
	// grader sees as A. It is copied into the grade, and never shown before both are graded.
	AgentIsA bool      `json:"agent_is_a"`
	Started  time.Time `json:"started"`
}

// Converged reports whether the run ended converged.
func (a Attempt) Converged() bool { return a.Outcome == runner.Converged }

// Grade is a person's blind grading of one attempt: a grade for the agent's patch and one for the
// human's. Grading both is deliberate: the human patch is a check on the grader, and each grade is
// a labelled example.
type Grade struct {
	Attempt  string    `json:"attempt"`
	AgentIsA bool      `json:"agent_is_a"` // the order the grader saw
	Agent    string    `json:"agent"`
	Human    string    `json:"human,omitempty"` // empty when the human patch was not graded
	Auto     bool      `json:"auto,omitempty"`  // graded without a person: an empty agent patch
	Note     string    `json:"note,omitempty"`
	By       string    `json:"by,omitempty"`
	At       time.Time `json:"at"`
}

// NewGrade is the grade for what a grader gave patches A and B, recorded against the order the
// grader saw.
func NewGrade(attempt string, agentIsA bool, a, b string, now time.Time) (Grade, error) {
	for _, g := range []string{a, b} {
		if !ValidGrade(g) {
			return Grade{}, fmt.Errorf("%q is not a grade: want %s", g, strings.Join(AllGrades, ", "))
		}
	}
	g := Grade{Attempt: attempt, AgentIsA: agentIsA, Agent: a, Human: b, At: now}
	if !agentIsA {
		g.Agent, g.Human = b, a
	}
	return g, nil
}

// AutoGrade is the grade of an attempt that left no patch: wrong, without asking anyone.
func AutoGrade(attempt string, agentIsA bool, now time.Time) Grade {
	return Grade{Attempt: attempt, AgentIsA: agentIsA, Agent: Wrong, Auto: true, Note: "the agent left no patch", At: now}
}

// Blind is the two patches as a grader sees them: A and B in the attempt's recorded order.
func (a Attempt) Blind() (patchA, patchB string) {
	if a.AgentIsA {
		return a.AgentPatch, a.HumanPatch
	}
	return a.HumanPatch, a.AgentPatch
}

// NewOrder draws a blind order.
func NewOrder() bool {
	var b [1]byte
	_, _ = rand.Read(b[:])
	return b[0]&1 == 1
}

// Keys: shadow/<run>/run, shadow/<run>/attempts/<id>, shadow/<run>/grades/<attempt>.
func runKey(id string) string          { return storePrefix + id + runSuffix }
func attemptKey(run, id string) string { return storePrefix + run + attemptPrefix + id }
func gradeKey(run, id string) string   { return storePrefix + run + gradePrefix + id }

// SaveRun writes a run, replacing any earlier version of it.
func SaveRun(ctx context.Context, s store.Store, r Run) error {
	return put(ctx, s, runKey(r.ID), r, true)
}

// SaveAttempt writes an attempt.
func SaveAttempt(ctx context.Context, s store.Store, a Attempt) error {
	return put(ctx, s, attemptKey(a.Run, a.ID), a, true)
}

// ErrGraded means the attempt already has a grade and the caller did not ask to replace it.
var ErrGraded = errors.New("shadow: already graded; --regrade replaces the grade")

// SaveGrade writes an attempt's grade. An existing grade is kept unless regrade is set.
func SaveGrade(ctx context.Context, s store.Store, run string, g Grade, regrade bool) error {
	err := put(ctx, s, gradeKey(run, g.Attempt), g, regrade)
	if errors.Is(err, store.ErrConflict) {
		return ErrGraded
	}
	return err
}

func put(ctx context.Context, s store.Store, key string, v any, overwrite bool) error {
	doc, err := json.Marshal(v)
	if err != nil {
		return err
	}
	version := ""
	if overwrite {
		if _, version, err = s.Get(ctx, key); errors.Is(err, store.ErrNotFound) {
			version = ""
		} else if err != nil {
			return err
		}
	}
	_, err = s.Put(ctx, key, doc, version)
	return err
}

func get[T any](ctx context.Context, s store.Store, key string) (T, error) {
	var v T
	doc, _, err := s.Get(ctx, key)
	if err != nil {
		return v, err
	}
	return v, json.Unmarshal(doc, &v)
}

// LoadRun reads a run; an id that names none is an error naming it.
func LoadRun(ctx context.Context, s store.Store, id string) (Run, error) {
	r, err := get[Run](ctx, s, runKey(id))
	if errors.Is(err, store.ErrNotFound) {
		return r, fmt.Errorf("no shadow run %s", id)
	}
	return r, err
}

// Runs lists every shadow run, oldest first.
func Runs(ctx context.Context, s store.Store) ([]Run, error) {
	keys, err := s.Keys(ctx, storePrefix)
	if err != nil {
		return nil, err
	}
	var out []Run
	for _, k := range keys {
		if !strings.HasSuffix(k, runSuffix) {
			continue
		}
		r, err := get[Run](ctx, s, k)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b Run) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

// Attempts lists a run's attempts, in the order they ran.
func Attempts(ctx context.Context, s store.Store, run string) ([]Attempt, error) {
	keys, err := s.Keys(ctx, storePrefix+run+attemptPrefix)
	if err != nil {
		return nil, err
	}
	var out []Attempt
	for _, k := range keys {
		a, err := get[Attempt](ctx, s, k)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	slices.SortFunc(out, func(a, b Attempt) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

// LoadGrade reads an attempt's grade; ok is false when it has none.
func LoadGrade(ctx context.Context, s store.Store, run, attempt string) (g Grade, ok bool, err error) {
	g, err = get[Grade](ctx, s, gradeKey(run, attempt))
	if errors.Is(err, store.ErrNotFound) {
		return Grade{}, false, nil
	}
	return g, err == nil, err
}

// Graded counts a run's attempts that have a grade.
func Graded(ctx context.Context, s store.Store, run string) (int, error) {
	keys, err := s.Keys(ctx, storePrefix+run+gradePrefix)
	return len(keys), err
}
