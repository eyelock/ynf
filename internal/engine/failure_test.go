package engine

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/eyelock/ynf/internal/decide"
	"github.com/eyelock/ynf/internal/facts"
	"github.com/eyelock/ynf/internal/item"
)

func TestFailureDetailOfARun(t *testing.T) {
	rec := &RunRecord{RunID: "r1", Exit: 3}
	rec.BoundBy, rec.FailedSensors, rec.Harness = "turns", []string{"unit-tests"}, "local/ynf-sandbox@0.1.0"
	s := &step{run: rec}
	it := item.Item{LastRun: &item.Run{ID: "r1", Outcome: "error", Detail: "exit 3: the build step failed"}}
	d := s.failureDetailFor(decide.Input{}, it, "sig/outcome/error")
	if d.Exit == nil || *d.Exit != 3 || d.Outcome != "error" || d.BoundBy != "turns" || d.Harness != "local/ynf-sandbox" || d.HarnessVersion != "0.1.0" {
		t.Fatalf("%+v", d)
	}
	got := d.sentence()
	for _, want := range []string{"the run ended error, exit 3", "bound by the turns cap", "failing sensors: unit-tests", "harness local/ynf-sandbox@0.1.0", `"exit 3: the build step failed"`} {
		if !strings.Contains(got, want) {
			t.Errorf("%q lacks %q", got, want)
		}
	}
	m := map[string]any{}
	d.data(m)
	if m["exit"] != 3 || m["outcome"] != "error" || m["excerpt"] != "exit 3: the build step failed" || m["harness_version"] != "0.1.0" || m["bound_by"] != "turns" {
		t.Fatalf("%v", m)
	}
}

func TestFailureDetailOfAnOlderRunHasNoExit(t *testing.T) {
	s := &step{run: &RunRecord{RunID: "other", Exit: 9}}
	it := item.Item{LastRun: &item.Run{ID: "r1", Outcome: "error", Detail: "boom"}}
	d := s.failureDetailFor(decide.Input{}, it, "sig/outcome/error")
	if d.Exit != nil || d.Excerpt != "boom" {
		t.Fatalf("%+v", d)
	}
	m := map[string]any{}
	d.data(m)
	if _, ok := m["exit"]; ok {
		t.Fatal("an exit code the step did not see is not claimed")
	}
}

func TestFailureDetailOfCI(t *testing.T) {
	pr := &facts.PR{Checks: []facts.Check{{Name: "lint", Status: "completed", Conclusion: "failure"}, {Name: "unit", Status: "completed", Conclusion: "success"}}}
	s := &step{}
	d := s.failureDetailFor(decide.Input{Facts: facts.Facts{PR: pr}}, item.Item{LastRun: &item.Run{Outcome: "converged", Detail: "old"}}, "sig/ci/lint")
	if len(d.FailedChecks) != 1 || d.FailedChecks[0] != "lint" || d.Outcome != "" || d.Excerpt != "" {
		t.Fatalf("%+v", d)
	}
	if !strings.Contains(d.sentence(), "failing checks: lint") {
		t.Fatal(d.sentence())
	}
}

func TestFailureExcerptIsScrubbed(t *testing.T) {
	tok := "ghp_" + strings.Repeat("a1B2", 6)
	got := excerpt("push failed with " + tok + " and password=hunter22 for dev@example.com")
	for _, leak := range []string{tok, "hunter22", "dev@example.com"} {
		if strings.Contains(got, leak) {
			t.Fatalf("%q leaks %q", got, leak)
		}
	}
	if !strings.Contains(got, "push failed") {
		t.Fatal(got)
	}
}

func TestFailureExcerptIsCutToASize(t *testing.T) {
	long := strings.Repeat("é", 2000) + "the last line"
	got := excerpt(long)
	if len(got) > excerptMax+len("…") || !utf8.ValidString(got) || !strings.HasSuffix(got, "the last line") || !strings.HasPrefix(got, "…") {
		t.Fatalf("%d bytes, valid %v", len(got), utf8.ValidString(got))
	}
	if excerpt("  short \n") != "short" {
		t.Fatal("a short excerpt is kept whole")
	}
	// A secret straddling the cut point is scrubbed before the cut, so no half of it survives.
	tok := "ghp_" + strings.Repeat("Z9", 12)
	if got := excerpt(strings.Repeat("x", excerptMax-5) + tok); strings.Contains(got, "Z9Z9") {
		t.Fatalf("half a token survived: %q", got)
	}
}

func TestSplitHarnessAndNames(t *testing.T) {
	if n, v := splitHarness("a/b@1.2"); n != "a/b" || v != "1.2" {
		t.Fatal(n, v)
	}
	if n, v := splitHarness("bare"); n != "bare" || v != "" {
		t.Fatal(n, v)
	}
	var many []string
	for range 30 {
		many = append(many, "s")
	}
	if len(scrubNames(many)) != namesMax {
		t.Fatal("names are capped")
	}
}
