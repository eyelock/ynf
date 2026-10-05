package cli_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eyelock/ynf/internal/cli"
	"github.com/eyelock/ynf/internal/shadow"
	"github.com/eyelock/ynf/internal/store/sqlite"
)

// seedShadow gives the store a shadow run with two attempts: one with a patch, shown to the agent
// as B, and one with none.
func seedShadow(t *testing.T, e env) {
	t.Helper()
	ctx := context.Background()
	st, err := sqlite.Open(filepath.Join(e.dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	run := shadow.Run{ID: "01RUN", Lane: "fmt", Repos: []string{"o/r"}, Created: time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC), Candidates: 3, Attempted: 2,
		Pins: map[string]shadow.Pins{"o/r": {Lane: "fmt", PolicyHash: strings.Repeat("a", 64), Runner: "command", Executor: "docker"}}}
	attempts := []shadow.Attempt{
		{ID: "01A", Run: run.ID, Repo: "o/r", Ticket: "o/r#1", FixPR: 7, Outcome: "converged", GateAccepted: true, AgentPatch: "AGENT PATCH", HumanPatch: "HUMAN PATCH", AgentIsA: false},
		{ID: "01B", Run: run.ID, Repo: "o/r", Ticket: "o/r#2", FixPR: 8, Outcome: "stuck", HumanPatch: "HUMAN PATCH 2", AgentIsA: true},
	}
	if err := shadow.SaveRun(ctx, st, run); err != nil {
		t.Fatal(err)
	}
	for _, a := range attempts {
		if err := shadow.SaveAttempt(ctx, st, a); err != nil {
			t.Fatal(err)
		}
	}
}

func TestShadowLsGradeReport(t *testing.T) {
	e := setup(t)
	if code, out, _ := e.run("shadow", "ls"); code != 0 || !strings.Contains(out, "no shadow runs") {
		t.Fatalf("empty ls: %d %s", code, out)
	}
	if code, _, stderr := e.run("shadow", "report"); code != cli.ExitUsage || !strings.Contains(stderr, "no shadow runs") {
		t.Fatalf("report with no runs: %d %s", code, stderr)
	}
	seedShadow(t, e)

	code, out, _ := e.run("shadow", "ls")
	if code != 0 || !strings.Contains(out, "01RUN") || !strings.Contains(out, "2026-10-05 09:00") {
		t.Fatalf("ls: %d\n%s", code, out)
	}
	var rows []struct{ ID, Lane string }
	if code, out, _ := e.run("--format", "json", "shadow", "ls"); code != 0 || json.Unmarshal([]byte(out), &rows) != nil || rows[0].ID != "01RUN" {
		t.Fatalf("ls json: %d %s", code, out)
	}

	// Before any grading, the report has only the automatic upper bound, labelled as such.
	code, out, _ = e.run("shadow", "report")
	if code != 0 || !strings.Contains(out, "not graded yet") || !strings.Contains(out, "upper bound, not graded") {
		t.Fatalf("report: %d\n%s", code, out)
	}

	// Scripts and tests grade with --attempt, --a and --b. The agent's patch was shown as B here.
	for _, bad := range [][]string{
		{"--attempt", "01A"},
		{"--a", "equivalent", "--b", "wrong"},
		{"--attempt", "01A", "--a", "great", "--b", "wrong"},
		{"--attempt", "nope", "--a", "wrong", "--b", "wrong"},
		{"--attempt", "01B", "--a", "wrong", "--b", "wrong"}, // no patch: graded automatically
	} {
		if code, _, _ := e.run(append([]string{"shadow", "grade", "01RUN"}, bad...)...); code != cli.ExitUsage {
			t.Errorf("%v: %d, want a usage error", bad, code)
		}
	}
	if code, out, stderr := e.run("shadow", "grade", "01RUN", "--attempt", "01A", "--a", "wrong", "--b", "equivalent"); code != 0 || !strings.Contains(out, "agent's patch equivalent, the human's wrong") {
		t.Fatalf("grade: %d %s %s", code, out, stderr)
	}
	if code, _, stderr := e.run("shadow", "grade", "01RUN", "--attempt", "01A", "--a", "wrong", "--b", "wrong"); code != cli.ExitUsage || !strings.Contains(stderr, "--regrade") {
		t.Fatalf("a second grade needs --regrade: %d %s", code, stderr)
	}
	if code, _, stderr := e.run("shadow", "grade", "--attempt", "01A", "--a", "different-valid", "--b", "different-valid", "--regrade"); code != 0 {
		t.Fatalf("regrade: %d %s", code, stderr)
	}
	// Without a terminal, grading by hand is refused.
	if code, _, stderr := e.run("shadow", "grade", "01RUN"); code != cli.ExitUsage || !strings.Contains(stderr, "terminal") {
		t.Fatalf("no terminal: %d %s", code, stderr)
	}

	var rep struct {
		Attempted, Graded int
		Pooled            struct {
			Yield *struct {
				Successes, N int
				Low, High    float64
			}
		}
		Grader struct{ Graded int } `json:"grader_check"`
	}
	code, out, _ = e.run("--format", "json", "shadow", "report", "--lane", "fmt")
	if code != 0 || json.Unmarshal([]byte(out), &rep) != nil {
		t.Fatalf("report json: %d %s", code, out)
	}
	// The empty-patch attempt was graded wrong automatically when grading ran.
	if rep.Attempted != 2 || rep.Graded != 2 || rep.Pooled.Yield == nil || rep.Pooled.Yield.Successes != 1 || rep.Pooled.Yield.N != 2 || rep.Grader.Graded != 1 {
		t.Fatalf("%+v\n%s", rep, out)
	}
	if code, _, _ := e.run("shadow", "report", "01RUN", "--lane", "fmt"); code != cli.ExitUsage {
		t.Fatal("a run id and --lane together")
	}
	if code, _, _ := e.run("shadow", "report", "--lane", "nope"); code != cli.ExitUsage {
		t.Fatal("a lane with no runs")
	}
	if code, _, _ := e.run("shadow", "report", "nope"); code != cli.ExitUsage {
		t.Fatal("an unknown run")
	}
}

func TestShadowRunAndUsage(t *testing.T) {
	e := setup(t)
	for _, args := range [][]string{
		{"shadow"}, {"shadow", "nope"}, {"shadow", "run"}, {"shadow", "run", "fmt", "extra"}, {"shadow", "run", "fmt", "--since", "soon"},
		{"shadow", "run", "fmt", "--limit", "0"}, {"shadow", "run", "fmt", "--repo", "x"}, {"shadow", "run", "fmt", "--ticket", "x"},
		{"shadow", "ls", "x"}, {"shadow", "grade", "a", "b"}, {"shadow", "report", "a", "b"},
		{"shadow", "run", "nope"},                     // no such lane
		{"shadow", "run", "fmt", "--repo", "o/other"}, // not enrolled
	} {
		if code, _, stderr := e.run(args...); code != cli.ExitUsage {
			t.Errorf("%v: %d %s", args, code, stderr)
		}
	}
	// The disabled lane is still measured: shadow mode is for lanes before they are on. Its one
	// ticket is open, so there is no answer to compare with and it is skipped, and nothing runs.
	code, out, stderr := e.run("shadow", "run", "off", "--since", "30d", "--limit", "5")
	if code != 0 || !strings.Contains(out, "1 candidate(s), 0 attempted, 1 skipped") || !strings.Contains(out, "o/r#5: the ticket is not closed") {
		t.Fatalf("%d\n%s\n%s", code, out, stderr)
	}
	code, out, _ = e.run("--format", "json", "shadow", "run", "--repo", "o/r", "fmt")
	var run struct{ ID, Lane string }
	if code != 0 || json.Unmarshal([]byte(out), &run) != nil || run.Lane != "fmt" || run.ID == "" {
		t.Fatalf("%d %s", code, out)
	}
	if code, out, _ := e.run("shadow", "ls"); code != 0 || strings.Count(out, "\n") != 3 {
		t.Fatalf("two runs listed:\n%s", out)
	}
}
