package shadow_test

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eyelock/ynf/internal/runner"
	"github.com/eyelock/ynf/internal/shadow"
	"github.com/eyelock/ynf/internal/store/sqlite"
)

func near(a, b float64) bool { return math.Abs(a-b) < 0.005 }

func TestWilsonKnownValues(t *testing.T) {
	cases := []struct {
		k, n     int
		low, hig float64
	}{
		{12, 20, 0.39, 0.78}, // the method's worked example
		{0, 10, 0, 0.28},
		{10, 10, 0.72, 1},
		{1, 2, 0.09, 0.91},
		{0, 0, 0, 1},
	}
	for _, c := range cases {
		lo, hi := shadow.Wilson(c.k, c.n)
		if !near(lo, c.low) || !near(hi, c.hig) {
			t.Errorf("Wilson(%d, %d) = %.3f to %.3f, want %.2f to %.2f", c.k, c.n, lo, hi, c.low, c.hig)
		}
	}
	if shadow.NewRate(0, 0) != nil {
		t.Error("no trials is no rate")
	}
	if got := shadow.NewRate(12, 20).String(); !strings.Contains(got, "0.60 (12/20") {
		t.Errorf("rate text %q", got)
	}
}

func TestBlindGrading(t *testing.T) {
	now := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	// The grader saw the agent's patch as A.
	g, err := shadow.NewGrade("x", true, shadow.Equivalent, shadow.Wrong, now)
	if err != nil || g.Agent != shadow.Equivalent || g.Human != shadow.Wrong || !g.AgentIsA {
		t.Fatalf("%+v %v", g, err)
	}
	// The agent's patch was B: the grades swap back.
	g, err = shadow.NewGrade("x", false, shadow.Equivalent, shadow.Wrong, now)
	if err != nil || g.Agent != shadow.Wrong || g.Human != shadow.Equivalent || g.AgentIsA {
		t.Fatalf("%+v %v", g, err)
	}
	if _, err := shadow.NewGrade("x", true, "great", shadow.Wrong, now); err == nil {
		t.Fatal("an unknown grade must be refused")
	}
	a := shadow.Attempt{AgentPatch: "AGENT", HumanPatch: "HUMAN", AgentIsA: false}
	if pa, pb := a.Blind(); pa != "HUMAN" || pb != "AGENT" {
		t.Fatalf("blind order: %s %s", pa, pb)
	}
	a.AgentIsA = true
	if pa, pb := a.Blind(); pa != "AGENT" || pb != "HUMAN" {
		t.Fatalf("blind order: %s %s", pa, pb)
	}
	auto := shadow.AutoGrade("x", true, now)
	if auto.Agent != shadow.Wrong || !auto.Auto || auto.Human != "" || auto.Note == "" {
		t.Fatalf("%+v", auto)
	}
	seen := map[bool]bool{}
	for range 64 {
		seen[shadow.NewOrder()] = true
	}
	if len(seen) != 2 {
		t.Fatal("the blind order should vary")
	}
}

func TestStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	st, err := sqlite.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	run := shadow.Run{ID: "01B", Lane: "fmt", Candidates: 2, Attempted: 1}
	if err := shadow.SaveRun(ctx, st, run); err != nil {
		t.Fatal(err)
	}
	run.Attempted = 2 // a run is saved again when it ends
	if err := shadow.SaveRun(ctx, st, run); err != nil {
		t.Fatal(err)
	}
	if err := shadow.SaveRun(ctx, st, shadow.Run{ID: "01A", Lane: "fmt"}); err != nil {
		t.Fatal(err)
	}
	runs, err := shadow.Runs(ctx, st)
	if err != nil || len(runs) != 2 || runs[0].ID != "01A" || runs[1].Attempted != 2 {
		t.Fatalf("%+v %v", runs, err)
	}
	if _, err := shadow.LoadRun(ctx, st, "nope"); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("%v", err)
	}
	a := shadow.Attempt{ID: "01X", Run: "01B", Ticket: "o/r#1", AgentPatch: "p"}
	if err := shadow.SaveAttempt(ctx, st, a); err != nil {
		t.Fatal(err)
	}
	if as, err := shadow.Attempts(ctx, st, "01B"); err != nil || len(as) != 1 || as[0].AgentPatch != "p" {
		t.Fatalf("%+v %v", as, err)
	}
	if as, _ := shadow.Attempts(ctx, st, "01A"); len(as) != 0 {
		t.Fatal("attempts belong to their run")
	}
	if _, ok, _ := shadow.LoadGrade(ctx, st, "01B", "01X"); ok {
		t.Fatal("not graded yet")
	}
	g := shadow.Grade{Attempt: "01X", Agent: shadow.Equivalent, Human: shadow.Equivalent}
	if err := shadow.SaveGrade(ctx, st, "01B", g, false); err != nil {
		t.Fatal(err)
	}
	if err := shadow.SaveGrade(ctx, st, "01B", g, false); !errors.Is(err, shadow.ErrGraded) {
		t.Fatalf("a second grade needs --regrade: %v", err)
	}
	g.Agent = shadow.Wrong
	if err := shadow.SaveGrade(ctx, st, "01B", g, true); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := shadow.LoadGrade(ctx, st, "01B", "01X"); err != nil || !ok || got.Agent != shadow.Wrong {
		t.Fatalf("%+v %v %v", got, ok, err)
	}
	if n, _ := shadow.Graded(ctx, st, "01B"); n != 1 {
		t.Fatalf("graded %d", n)
	}
	// Shadow runs live under their own prefix: nothing an item listing or stats reads.
	if keys, _ := st.Keys(ctx, "item/"); len(keys) != 0 {
		t.Fatalf("shadow wrote items: %v", keys)
	}
}

func attempt(id, repo, outcome string, gate bool) shadow.Attempt {
	return shadow.Attempt{ID: id, Repo: repo, Outcome: outcome, GateAccepted: gate}
}

func TestReport(t *testing.T) {
	attempts := []shadow.Attempt{
		attempt("1", "o/a", runner.Converged, true),
		attempt("2", "o/a", runner.Converged, false), // the gate would have refused it
		attempt("3", "o/a", runner.Stuck, false),
		attempt("4", "o/b", runner.Converged, true),
	}
	attempts[0].CostUSD = 0.5
	attempts[3].CostUSD = 1.5
	runs := []shadow.Run{{ID: "R", Pins: map[string]shadow.Pins{"o/a": {Lane: "fmt", PolicyHash: "abcdef0123456789", Runner: "ynh", Executor: "docker"}}}}

	// Nothing graded: only the automatic upper bound.
	rep := shadow.BuildReport("fmt", runs, attempts, nil)
	if rep.Pooled.Yield != nil || rep.Pooled.UpperBound.Successes != 2 || rep.Pooled.UpperBound.N != 4 {
		t.Fatalf("%+v", rep.Pooled)
	}
	text := rep.Text()
	for _, want := range []string{"not graded yet", "upper bound, not graded", "o/a", "o/b", "abcdef012345"} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
	if rep.CostUSD != 2 || rep.CostPerRun != 1 || rep.Outcomes[runner.Converged] != 3 {
		t.Fatalf("cost %v per %v outcomes %v", rep.CostUSD, rep.CostPerRun, rep.Outcomes)
	}

	grades := map[string]shadow.Grade{
		"1": {Agent: shadow.Equivalent, Human: shadow.Equivalent},
		"2": {Agent: shadow.Superficial, Human: shadow.DifferentOK},
		"3": {Agent: shadow.Wrong, Auto: true},
		"4": {Agent: shadow.DifferentOK, Human: shadow.Wrong},
	}
	rep = shadow.BuildReport("fmt", runs, attempts, grades)
	if y := rep.Pooled.Yield; y == nil || y.Successes != 2 || y.N != 4 || !near(y.Value, 0.5) {
		t.Fatalf("pooled yield %+v", y)
	}
	if rep.Pooled.Superficial != 1 {
		t.Fatalf("superficial %d", rep.Pooled.Superficial)
	}
	if len(rep.Repos) != 2 || rep.Repos[0].Repo != "o/a" || rep.Repos[0].Yield.Successes != 1 || rep.Repos[0].Yield.N != 3 || rep.Repos[1].Yield.Value != 1 {
		t.Fatalf("per repo %+v", rep.Repos)
	}
	// The grader check: three human patches were graded (the auto grade has none), two passed.
	if rep.Grader.Graded != 3 || rep.Grader.Passed.Successes != 2 {
		t.Fatalf("grader check %+v", rep.Grader)
	}
	if rep.BreakEven != nil {
		t.Fatal("no lane declares h, so there is no break-even")
	}
	if text := rep.Text(); !strings.Contains(text, "superficial: 1 of 4") || !strings.Contains(text, "0.50 (2/4") {
		t.Errorf("text:\n%s", text)
	}
}

func TestPinsObserve(t *testing.T) {
	mk := func(h, sha, model string) shadow.Attempt {
		a := shadow.Attempt{Model: model}
		a.Harness, a.HarnessSHA, a.RunnerVersion, a.EffortRequested = h, sha, "0.10.0", "medium"
		return a
	}
	var p shadow.Pins
	p.Observe([]shadow.Attempt{mk("h@1", "aaa", "claude/x"), mk("h@1", "aaa", "claude/x")})
	if p.Observed.Harness != "h@1" || p.Observed.Ynh != "0.10.0" || p.Observed.Effort != "medium" || len(p.Varied) != 0 {
		t.Fatalf("%+v", p)
	}
	p.Observe([]shadow.Attempt{mk("h@1", "aaa", "claude/x"), mk("h@2", "bbb", "claude/x")})
	if strings.Join(p.Varied, ",") != "harness,harness_sha" {
		t.Fatalf("varied %v", p.Varied)
	}
	if !strings.Contains(shadow.BuildReport("l", []shadow.Run{{ID: "R", Pins: map[string]shadow.Pins{"o/a": p}}}, nil, nil).Text(), "NOT PINNED") {
		t.Fatal("a pin that varied must be said")
	}
}
