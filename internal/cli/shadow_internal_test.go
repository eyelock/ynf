package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eyelock/ynf/internal/engine"
	"github.com/eyelock/ynf/internal/shadow"
	"github.com/eyelock/ynf/internal/store/sqlite"
)

func TestInteractiveGradingIsBlind(t *testing.T) {
	ctx := context.Background()
	st, err := sqlite.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	e := &engine.Engine{Store: st, Now: func() time.Time { return time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC) }}
	run := shadow.Run{ID: "R"}
	// The agent's patch is B in the first attempt and A in the second; the third ran stuck.
	attempts := []shadow.Attempt{
		{ID: "1", Run: "R", Ticket: "o/r#1", Title: "First", Body: "it is broken", FixPR: 4, Outcome: "converged", AgentPatch: "THE AGENT ONE", HumanPatch: "THE HUMAN ONE", AgentIsA: false},
		{ID: "2", Run: "R", Ticket: "o/r#2", Title: "Second", FixPR: 5, Outcome: "stuck", Detail: "no progress", AgentPatch: "THE AGENT TWO", HumanPatch: "THE HUMAN TWO", AgentIsA: true},
		{ID: "3", Run: "R", Ticket: "o/r#3", Title: "Third", FixPR: 6, Outcome: "stuck", AgentPatch: "THE AGENT THREE", HumanPatch: "THE HUMAN THREE", AgentIsA: true},
	}
	for _, a := range attempts {
		if err := shadow.SaveAttempt(ctx, st, a); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	g := grader{ctx: ctx, e: e, run: run, out: &out, by: "tester"}

	// A is the human's, B the agent's in attempt 1: "1" and "wrong" (an unknown word is asked
	// again). Attempt 2 is graded 2 and 3; the grader stops at attempt 3.
	in := strings.NewReader("1\nmeh\nwrong\n2\n3\nq\n")
	if err := g.interactive(in, attempts); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	reveal := strings.Index(text, "was the agent's patch")
	if reveal < 0 {
		t.Fatalf("no reveal:\n%s", text)
	}
	first := text[:reveal]
	for _, hidden := range []string{"converged", "agent's", "stuck"} {
		if strings.Contains(first, hidden) {
			t.Errorf("the first attempt showed %q before it was graded:\n%s", hidden, first)
		}
	}
	for _, shown := range []string{"First", "it is broken", "--- patch A ---\nTHE HUMAN ONE", "--- patch B ---\nTHE AGENT ONE", `"meh" is not a grade`} {
		if !strings.Contains(text, shown) {
			t.Errorf("missing %q:\n%s", shown, text)
		}
	}
	if !strings.Contains(text, "B was the agent's patch (converged), A the human's (fix pull request #4). Graded: agent wrong, human equivalent.") {
		t.Errorf("reveal:\n%s", text)
	}
	if !strings.Contains(text, "A was the agent's patch (stuck)") || !strings.Contains(text, "The run did not converge: no progress") {
		t.Errorf("second reveal:\n%s", text)
	}
	g1, ok, _ := shadow.LoadGrade(ctx, st, "R", "1")
	if !ok || g1.Agent != shadow.Wrong || g1.Human != shadow.Equivalent || g1.AgentIsA || g1.By != "tester" {
		t.Fatalf("%+v", g1)
	}
	g2, _, _ := shadow.LoadGrade(ctx, st, "R", "2")
	if g2.Agent != shadow.DifferentOK || g2.Human != shadow.Superficial || !g2.AgentIsA {
		t.Fatalf("%+v", g2)
	}
	if _, ok, _ := shadow.LoadGrade(ctx, st, "R", "3"); ok {
		t.Fatal("quitting leaves the rest ungraded")
	}

	// Run again: graded attempts are not shown, and the third is. End of input also stops.
	out.Reset()
	if err := g.interactive(strings.NewReader("1\n"), attempts); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "o/r#1") || !strings.Contains(out.String(), "attempt 1 of 1: o/r#3") {
		t.Fatalf("only the ungraded attempt:\n%s", out.String())
	}
	// With --regrade, all are offered again.
	out.Reset()
	g.regrade = true
	if err := g.interactive(strings.NewReader("q\n"), attempts); err != nil || !strings.Contains(out.String(), "attempt 1 of 3") {
		t.Fatalf("%v\n%s", err, out.String())
	}
	g.regrade = false
	if err := g.interactive(strings.NewReader(""), attempts[:2]); err != nil || !strings.Contains(out.String(), "nothing to grade") {
		t.Fatalf("%v\n%s", err, out.String())
	}
}

func TestAutoGradeAnEmptyPatch(t *testing.T) {
	ctx := context.Background()
	st, err := sqlite.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	e := &engine.Engine{Store: st, Now: time.Now}
	var out bytes.Buffer
	g := grader{ctx: ctx, e: e, run: shadow.Run{ID: "R"}, out: &out}
	empty := shadow.Attempt{ID: "E", Run: "R", Ticket: "o/r#9", AgentPatch: "  \n", HumanPatch: "H"}
	if err := g.autoGrade([]shadow.Attempt{empty}); err != nil {
		t.Fatal(err)
	}
	got, ok, _ := shadow.LoadGrade(ctx, st, "R", "E")
	if !ok || got.Agent != shadow.Wrong || !got.Auto || got.Note == "" || !strings.Contains(out.String(), "graded wrong automatically") {
		t.Fatalf("%+v %s", got, out.String())
	}
	// It is never shown to a grader, and a second pass leaves the grade alone.
	out.Reset()
	if err := g.autoGrade([]shadow.Attempt{empty}); err != nil || out.Len() != 0 {
		t.Fatalf("%v %q", err, out.String())
	}
	if err := g.interactive(strings.NewReader(""), []shadow.Attempt{empty}); err != nil || !strings.Contains(out.String(), "nothing to grade") {
		t.Fatalf("%v %s", err, out.String())
	}
	if _, err := g.record(empty, shadow.Wrong, shadow.Wrong); err == nil {
		t.Fatal("an attempt with no patch cannot be graded by hand")
	}
}
