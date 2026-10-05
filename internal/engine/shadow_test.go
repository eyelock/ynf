package engine_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eyelock/ynf/internal/engine"
	"github.com/eyelock/ynf/internal/forge"
	"github.com/eyelock/ynf/internal/memory"
	"github.com/eyelock/ynf/internal/runner"
	"github.com/eyelock/ynf/internal/shadow"
	"github.com/eyelock/ynf/internal/store"
	"github.com/eyelock/ynf/internal/tracker"
	"github.com/eyelock/ynf/internal/workspace"
)

func (f *fakeForge) FixFor(_ context.Context, _ string, n int) (forge.Fix, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if fix, ok := f.fixes[n]; ok {
		return fix, nil
	}
	return forge.Fix{}, forge.ErrNoFix
}

// outward records every way a step could reach outside the machine, so a test can prove shadow
// mode used none of them: a forge or tracker write, a push or a commit, memory, or any store key
// outside shadow/.
type outward struct {
	mu    sync.Mutex
	calls []string
}

func (o *outward) note(what string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, what)
}

type spyGit struct {
	workspace.Workspace
	o *outward
}

func (g spyGit) Commit(ctx context.Context, wt, msg string) (string, error) {
	g.o.note("git commit")
	return g.Workspace.Commit(ctx, wt, msg)
}

func (g spyGit) Push(ctx context.Context, wt, repo, branch string) error {
	g.o.note("git push")
	return g.Workspace.Push(ctx, wt, repo, branch)
}

func (g spyGit) PushFastForward(ctx context.Context, wt, repo, branch string) error {
	g.o.note("git push (fast-forward)")
	return g.Workspace.PushFastForward(ctx, wt, repo, branch)
}

type spyTracker struct {
	tracker.Tracker
	o *outward
}

func (s spyTracker) Comment(ctx context.Context, key, marker, body string) error {
	s.o.note("ticket comment")
	return s.Tracker.Comment(ctx, key, marker, body)
}

func (s spyTracker) Label(ctx context.Context, key string, add, remove []string) error {
	s.o.note("ticket label")
	return s.Tracker.Label(ctx, key, add, remove)
}

type spyForge struct {
	*fakeForge
	o *outward
}

func (s spyForge) OpenPR(ctx context.Context, repo string, p forge.NewPR) (int, error) {
	s.o.note("open pull request")
	return s.fakeForge.OpenPR(ctx, repo, p)
}

func (s spyForge) Comment(ctx context.Context, repo string, n int, marker, body string) error {
	s.o.note("pull request comment")
	return s.fakeForge.Comment(ctx, repo, n, marker, body)
}

type spyMemory struct{ o *outward }

func (m spyMemory) Remember(context.Context, memory.Record) error {
	m.o.note("memory write")
	return nil
}

type spyStore struct {
	store.Store
	o *outward
}

func (s spyStore) Put(ctx context.Context, key string, doc []byte, v string) (string, error) {
	if !strings.HasPrefix(key, "shadow/") {
		s.o.note("store put " + key)
	}
	return s.Store.Put(ctx, key, doc, v)
}

func (s spyStore) Append(ctx context.Context, key string, e store.LogEntry) error {
	s.o.note("store append " + key)
	return s.Store.Append(ctx, key, e)
}

func (s spyStore) Schedule(ctx context.Context, key string, at time.Time) error {
	s.o.note("store schedule " + key)
	return s.Store.Schedule(ctx, key, at)
}

// shadowHarness is the harness with its git remote given a fix: the seed commit has the unformatted
// file, and the next commit, the merge commit of the human fix, formats it.
type shadowHarness struct {
	*harness
	o         *outward
	base, fix string
}

func newShadowHarness(t *testing.T) *shadowHarness {
	t.Helper()
	h := newHarness(t)
	o := &outward{}
	dir := t.TempDir()
	git(t, "", "clone", "-q", h.remote, dir)
	base := strings.TrimSpace(git(t, dir, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(dir, "internal/format/f.go"), []byte("package format\n\nfunc F() int { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "-c", "user.name=h", "-c", "user.email=h@h", "commit", "-q", "-m", "fix: format (#9)")
	fix := strings.TrimSpace(git(t, dir, "rev-parse", "HEAD"))
	git(t, dir, "push", "-q", "origin", "main")

	h.e.Forge = spyForge{h.f, o}
	h.e.Trackers = map[string]tracker.Tracker{"github.com": spyTracker{forge.IssueTracker(h.f), o}}
	h.e.Git = spyGit{workspace.Workspace{Root: filepath.Join(filepath.Dir(h.remote), "work"), RemoteURL: func(string) string { return h.remote }}, o}
	h.e.Memory = spyMemory{o}
	h.e.Store = spyStore{h.e.Store, o}
	return &shadowHarness{harness: h, o: o, base: base, fix: fix}
}

// closedFixed makes issue n a closed ticket the fmt lane would have taken, fixed by the pull request
// whose merge commit formats the file.
func (h *shadowHarness) closedFixed(n int, labels ...string) {
	h.f.mu.Lock()
	defer h.f.mu.Unlock()
	h.f.labels[n] = labels
	h.f.closed[n] = true
	if h.f.fixes == nil {
		h.f.fixes = map[int]forge.Fix{}
	}
	h.f.fixes[n] = forge.Fix{PR: 9, MergeSHA: h.fix}
}

func (h *shadowHarness) attempts(t *testing.T, run shadow.Run) []shadow.Attempt {
	t.Helper()
	as, err := shadow.Attempts(context.Background(), h.e.Store, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return as
}

func TestShadowRunsALaneAgainstTheCommitBeforeTheFix(t *testing.T) {
	h := newShadowHarness(t)
	ctx := context.Background()
	fmtLabels := []string{"ynf:fmt", "pkg:internal/format"}
	h.closedFixed(1, fmtLabels...)
	h.closedFixed(2, "ynf:fmt", "pkg:internal/format", "ynf:working") // a lifecycle label ynf added later
	delete(h.f.fixes, 2)                                              // closed by hand
	h.f.labels[3], h.f.closed[3] = fmtLabels, false                   // still open
	h.f.labels[4], h.f.closed[4] = []string{"ynf:fmt"}, true          // no pkg label: the lane cannot run it
	h.closedFixed(4, "ynf:fmt")
	labelsBefore := map[int]string{}
	for n, ls := range h.f.labels {
		labelsBefore[n] = strings.Join(ls, ",")
	}
	refs := git(t, h.remote, "for-each-ref")

	run, err := h.e.ShadowRun(ctx, engine.ShadowRequest{Lane: "fmt", Repos: []string{"o/r"}})
	if err != nil {
		t.Fatal(err)
	}
	if run.Candidates != 4 || run.Attempted != 1 || len(run.Skipped) != 3 {
		t.Fatalf("candidates %d attempted %d skipped %+v", run.Candidates, run.Attempted, run.Skipped)
	}
	reasons := map[string]string{}
	for _, s := range run.Skipped {
		reasons[s.Ticket] = s.Reason
	}
	for ticket, want := range map[string]string{"o/r#2": "no merged pull request", "o/r#3": "not closed", "o/r#4": "cannot run"} {
		if !strings.Contains(reasons[ticket], want) {
			t.Errorf("%s skipped for %q, want %q", ticket, reasons[ticket], want)
		}
	}

	as := h.attempts(t, run)
	if len(as) != 1 {
		t.Fatalf("%d attempts", len(as))
	}
	a := as[0]
	if a.Ticket != "o/r#1" || a.FixPR != 9 || a.MergeSHA != h.fix || a.Base != h.base {
		t.Fatalf("attempt %+v", a)
	}
	if a.Outcome != runner.Converged || !a.GateAccepted || len(a.Changed) != 1 {
		t.Fatalf("outcome %s changed %v gate %v %s", a.Outcome, a.Changed, a.GateAccepted, a.GateDetail)
	}
	// The run was on the base, so it had something to fix, and its patch is the human's.
	if !strings.Contains(a.AgentPatch, "+func F() int") || a.AgentPatch != a.HumanPatch {
		t.Fatalf("agent patch:\n%s\nhuman patch:\n%s", a.AgentPatch, a.HumanPatch)
	}
	if a.RunDir == "" {
		t.Fatal("the attempt keeps its run folder")
	}
	if _, err := os.Stat(filepath.Join(a.RunDir, "run", "task.md")); err != nil {
		t.Fatalf("the task is kept: %v", err)
	}
	task, _ := os.ReadFile(filepath.Join(a.RunDir, "run", "task.md"))
	if !strings.Contains(string(task), "Issue 1") || strings.Contains(string(task), "format (#9)") {
		t.Fatalf("the task comes from the ticket, never the fix:\n%s", task)
	}
	if p := run.Pins["o/r"]; p.Lane != "fmt" || len(p.PolicyHash) != 64 || p.Runner != "command" || p.Executor != "process" {
		t.Fatalf("pins %+v", p)
	}
	if back, err := shadow.LoadRun(ctx, h.e.Store, run.ID); err != nil || back.Attempted != 1 {
		t.Fatalf("the run is stored: %+v %v", back, err)
	}

	// Nothing outward: no write to the forge, the tickets, git or memory, and no key of ynf's
	// store outside shadow/. No item exists for stats to count.
	if len(h.o.calls) != 0 {
		t.Fatalf("shadow mode acted outward: %v", h.o.calls)
	}
	if len(h.f.opened) != 0 || len(h.f.comments) != 0 {
		t.Fatalf("pull requests %v comments %v", h.f.opened, h.f.comments)
	}
	for n, ls := range h.f.labels {
		if strings.Join(ls, ",") != labelsBefore[n] {
			t.Errorf("#%d was relabelled: %v", n, ls)
		}
	}
	if after := git(t, h.remote, "for-each-ref"); after != refs {
		t.Fatalf("the remote moved:\n%s\n%s", refs, after)
	}
	if items, _ := h.e.Items(ctx); len(items) != 0 {
		t.Fatalf("items: %v", items)
	}
	stats, err := h.e.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range stats {
		if s.Proposed+s.Merged+s.Rejected != 0 || len(s.States) != 0 || len(s.Models) != 0 {
			t.Fatalf("shadow counted in stats: %+v", s)
		}
	}
}

func TestShadowTakesATicketCarryingYnfsOwnLabels(t *testing.T) {
	h := newShadowHarness(t)
	// gofmt needs pkg:internal/format, which only the agent lane's templates read; the lifecycle
	// label ynf adds must not reach the run, nor stop the ticket being taken.
	h.closedFixed(1, "ynf:fmt", "pkg:internal/format", "ynf:working")
	lanes := strings.Replace(lanesYAML, `    when: {converged: open_pr}
    pr: {allowed_paths: ["**/*.go"]}
    stop:`, `    labels:
      on_claim: {add: [ynf:working], remove: [ynf:fmt]}
    when: {converged: open_pr}
    pr: {allowed_paths: ["**/*.go"]}
    stop:`, 1)
	if lanes == lanesYAML {
		t.Fatal("the test lane was not changed")
	}
	h.f.lanes = lanes
	run, err := h.e.ShadowRun(context.Background(), engine.ShadowRequest{Lane: "fmt", Tickets: []tracker.Ref{{Host: "github.com", Key: "o/r#1"}}})
	if err != nil || run.Attempted != 1 {
		t.Fatalf("%+v %v", run, err)
	}
}

func TestShadowTicketsNameCandidatesDirectly(t *testing.T) {
	h := newShadowHarness(t)
	ctx := context.Background()
	h.closedFixed(1, "pkg:internal/format") // no intake label: only --ticket finds it
	h.closedFixed(2, "pkg:internal/format")
	ref := func(k string) tracker.Ref { return tracker.Ref{Host: "github.com", Key: k} }

	run, err := h.e.ShadowRun(ctx, engine.ShadowRequest{Lane: "fmt", Tickets: []tracker.Ref{ref("o/r#1"), ref("o/r#1")}})
	if err != nil || run.Candidates != 1 || run.Attempted != 1 {
		t.Fatalf("%+v %v", run, err)
	}
	// The limit is per repository.
	run, err = h.e.ShadowRun(ctx, engine.ShadowRequest{Lane: "fmt", Limit: 1, Tickets: []tracker.Ref{ref("o/r#1"), ref("o/r#2")}})
	if err != nil || run.Candidates != 2 || run.Attempted != 1 {
		t.Fatalf("%+v %v", run, err)
	}
	for _, bad := range []tracker.Ref{ref("o/other#1"), ref("PLAT-1"), {Host: "jira.example", Key: "o/r#1"}} {
		_, err := h.e.ShadowRun(ctx, engine.ShadowRequest{Lane: "fmt", Tickets: []tracker.Ref{bad}})
		var refused *engine.RefusedError
		if !errors.As(err, &refused) {
			t.Errorf("%s: %v, want a refusal", bad, err)
		}
	}
}

func TestShadowRefusesWhatItCannotRead(t *testing.T) {
	h := newShadowHarness(t)
	ctx := context.Background()
	h.f.lanes = lanesYAML + `  jira-only:
    kind: originate
    intake: [{jira.search: "project = X", every: 5m}]
    run:
      runner: command
      command: {argv: ["true"]}
    when: {converged: open_pr}
`
	for lane, want := range map[string]string{
		"adopt":     "adopts pull requests",
		"jira-only": "cannot read as closed",
		"nope":      "no lane nope",
		"":          "needs a lane",
	} {
		_, err := h.e.ShadowRun(ctx, engine.ShadowRequest{Lane: lane, Repos: []string{"o/r"}})
		var refused *engine.RefusedError
		if !errors.As(err, &refused) || !strings.Contains(err.Error(), want) {
			t.Errorf("lane %q: %v, want a refusal saying %q", lane, err, want)
		}
	}
	if _, err := h.e.ShadowRun(ctx, engine.ShadowRequest{Lane: "fmt", Repos: []string{"o/elsewhere"}}); err == nil || !strings.Contains(err.Error(), "not an enrolled") {
		t.Errorf("an unenrolled repository: %v", err)
	}
	// Without --repo, a repository that lacks the lane is left out, and none having it is refused.
	if _, err := h.e.ShadowRun(ctx, engine.ShadowRequest{Lane: "nope"}); err == nil || !strings.Contains(err.Error(), "no enrolled repository has a lane nope") {
		t.Errorf("%v", err)
	}
	// A lane that takes a mixed intake is shadowed through its GitHub search.
	h.closedFixed(1, "ynf:agent")
	if run, err := h.e.ShadowRun(ctx, engine.ShadowRequest{Lane: "agent"}); err != nil || run.Candidates != 1 || run.Attempted != 1 {
		t.Fatalf("%+v %v", run, err)
	}
	if items, _ := h.e.Items(ctx); len(items) != 0 || len(h.o.calls) != 0 {
		t.Fatalf("items %v calls %v", items, h.o.calls)
	}
}

func TestShadowAnEmptyPatchAndAStoppedRun(t *testing.T) {
	h := newShadowHarness(t)
	ctx := context.Background()
	h.closedFixed(1, "ynf:noop")
	run, err := h.e.ShadowRun(ctx, engine.ShadowRequest{Lane: "noop"})
	if err != nil || run.Attempted != 1 {
		t.Fatalf("%+v %v", run, err)
	}
	a := h.attempts(t, run)[0]
	if a.Outcome != runner.Converged || a.AgentPatch != "" || a.GateAccepted || a.GateDetail == "" || a.HumanPatch == "" {
		t.Fatalf("a converged run that changed nothing: %+v", a)
	}

	// An executor the run may not use ends every candidate the same way, so the run stops after one.
	h.e.Interactive = false
	h.closedFixed(2, "ynf:noop")
	run, err = h.e.ShadowRun(ctx, engine.ShadowRequest{Lane: "noop"})
	if err != nil || run.Attempted != 1 || !strings.Contains(run.Stopped, "operator_error") {
		t.Fatalf("%+v %v", run, err)
	}
}

func TestShadowQuery(t *testing.T) {
	since := time.Date(2026, 7, 7, 15, 0, 0, 0, time.UTC)
	for q, want := range map[string]string{
		`repo:o/r is:issue is:open label:"ynf:lint"`: `repo:o/r is:issue is:closed label:"ynf:lint" closed:>=2026-07-07`,
		`label:x state:open`:                         `label:x state:closed closed:>=2026-07-07`,
		`label:x`:                                    `label:x is:closed closed:>=2026-07-07`,
	} {
		if got := engine.ShadowQuery(q, since); got != want {
			t.Errorf("%s\n got %s\nwant %s", q, got, want)
		}
	}
}

// TestShadowPinsTheHarnessFromTheDefaultBranch: a lane that runs a harness folder on the host runs
// every candidate with the folder as the default branch has it, not as each older tree had it.
func TestShadowPinsTheHarnessFromTheDefaultBranch(t *testing.T) {
	h := newShadowHarness(t)
	ctx := context.Background()
	h.closedFixed(1, "ynf:agentic", "pkg:internal/format")
	run, err := h.e.ShadowRun(ctx, engine.ShadowRequest{Lane: "agentic"})
	if err != nil || run.Attempted != 1 {
		t.Fatalf("%+v %v", run, err)
	}
	p := run.Pins["o/r"]
	if p.Runner != "ynh" || p.Executor != "process" || p.Harness != "." {
		t.Fatalf("pins %+v", p)
	}
	// ynh is not installed here, so the run itself fails; what matters is that the harness was
	// read from the pinned folder (an unreadable one is an operator error) and the run started.
	if a := h.attempts(t, run)[0]; a.Outcome != runner.Error {
		t.Fatalf("outcome %s (%s)", a.Outcome, a.Detail)
	}
	if len(h.o.calls) != 0 {
		t.Fatalf("outward: %v", h.o.calls)
	}
}
