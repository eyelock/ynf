package engine_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/engine"
	"github.com/eyelock/ynf/internal/item"
	"github.com/eyelock/ynf/internal/runner"
)

// unnamed are lanes that name no runner (ADR-012): both blocks, and a ynh block alone.
const unnamed = `  auto:
    kind: originate
    intake: [{github.search: "label:ynf:auto", every: 5m}]
    run:
      env: [ANTHROPIC_API_KEY]
      ynh: {harness: ".", focus: tidy}
      command: {argv: [gofmt, -w, "./{label.pkg}"]}
    when: {converged: open_pr}
  ynhonly:
    kind: originate
    intake: [{github.search: "label:ynf:ynhonly", every: 5m}]
    run:
      ynh: {harness: ".", focus: tidy}
    when: {converged: open_pr}
`

func runOf(t *testing.T, h *harness, n string) engine.RunRecord {
	t.Helper()
	entries, _ := h.e.Store.Log(context.Background(), "item/github.com/o/r/issues/"+n)
	var rec engine.RunRecord
	for _, en := range entries {
		var r engine.RunRecord
		if en.Kind == "run" && json.Unmarshal(en.Body, &r) == nil {
			rec = r
		}
	}
	if rec.RunID == "" {
		t.Fatalf("no run record: %+v", entries)
	}
	return rec
}

func TestALaneWithNoRunnerUsesDetectedYnh(t *testing.T) {
	h := newHarness(t)
	fakeYnh(t)
	h.f.lanes = lanesYAML + unnamed
	h.e.DetectYnh = func(context.Context) runner.Detection { return runner.Detection{Found: true, Version: "0.10.0"} }
	h.f.labels[1] = []string{"ynf:auto", "pkg:internal/format"}
	if err := h.e.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if it := h.item(t, 1); it.State != item.Proposed {
		t.Fatalf("%s %+v", it.State, it.LastRun)
	}
	if rec := runOf(t, h, "1"); rec.Runner != "ynh" || !rec.RunnerDetected || rec.RunnerVersion != "0.10.0" {
		t.Fatalf("%+v", rec)
	}
}

func TestALaneWithNoRunnerFallsBackToItsCommand(t *testing.T) {
	h := newHarness(t)
	h.f.lanes = lanesYAML + unnamed
	h.e.DetectYnh = func(context.Context) runner.Detection { return runner.Detection{Detail: "ynh: not found"} }
	h.f.labels[1] = []string{"ynf:auto", "pkg:internal/format"}
	h.f.labels[2] = []string{"ynf:ynhonly"}
	h.f.labels[3] = []string{"ynf:agentic"}
	if err := h.e.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if it := h.item(t, 1); it.State != item.Proposed {
		t.Fatalf("%s %+v", it.State, it.LastRun)
	}
	if rec := runOf(t, h, "1"); rec.Runner != "command" || !rec.RunnerDetected || rec.RunnerVersion != "" {
		t.Fatalf("%+v", rec)
	}
	// With no command to fall back to, the lane is refused before anything runs.
	if it := h.item(t, 2); it.State != item.Escalated || it.LastRun.Outcome != runner.OperatorError || !strings.Contains(it.LastRun.Detail, "no command block to fall back to") {
		t.Fatalf("%s %+v", it.State, it.LastRun)
	}
	// A lane that names ynh never falls back, and is refused when ynh is not usable here.
	it := h.item(t, 3)
	if it.State != item.Escalated || it.LastRun.Outcome != runner.OperatorError || !strings.Contains(it.LastRun.Detail, "names runner ynh") {
		t.Fatalf("%s %+v", it.State, it.LastRun)
	}
	if rec := runOf(t, h, "3"); rec.Runner != "ynh" || rec.RunnerDetected {
		t.Fatalf("%+v", rec)
	}
}

func TestLaneRunsSayWhatAnUnnamedRunnerResolvesTo(t *testing.T) {
	h := newHarness(t)
	h.f.lanes = lanesYAML + unnamed
	for _, c := range []struct {
		det  runner.Detection
		want string
	}{
		{runner.Detection{Found: true, Version: "0.10.0"}, "ynh (detected 0.10.0)"},
		{runner.Detection{}, "command (ynh was not found)"},
	} {
		h.e.DetectYnh = func(context.Context) runner.Detection { return c.det }
		runs, err := h.e.LaneRuns(context.Background(), "o/r")
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range runs {
			switch r.Lane {
			case "auto":
				if !r.Detected || r.Resolves != c.want {
					t.Errorf("%+v", r)
				}
			case "ynhonly":
				if c.det.Found == (r.Problem != "") || (!c.det.Found && !strings.Contains(r.Problem, "no command block")) {
					t.Errorf("%+v", r)
				}
			case "fmt":
				if r.Detected || r.Resolves != "" || r.Runner != "command" {
					t.Errorf("a named runner: %+v", r)
				}
			}
		}
	}
}
