package decide_test

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/eyelock/ynf/internal/decide"
	"github.com/eyelock/ynf/internal/event"
	"github.com/eyelock/ynf/internal/facts"
	"github.com/eyelock/ynf/internal/item"
	"github.com/eyelock/ynf/internal/policy"
)

var t0 = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

func lanes(t *testing.T) *policy.File {
	t.Helper()
	doc, err := os.ReadFile("../../sandbox/seed/.agents/factory/lanes.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f, err := policy.Load(doc)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func ev(typ string, data map[string]any) event.Event {
	return event.New("e", "test", typ, item.IssueSubject("o/r", 1), t0, data)
}

func open() *facts.Ticket {
	return &facts.Ticket{Number: 1, State: "open", Labels: []string{"ynf:fmt", "pkg:internal/format"}}
}

func pr(conclusions ...string) *facts.PR {
	p := &facts.PR{Number: 7, State: "open", Draft: true, HeadSHA: "abc"}
	for i, c := range conclusions {
		status := "completed"
		if c == "" {
			status = "in_progress"
		}
		p.Checks = append(p.Checks, facts.Check{Name: []string{"lint", "test", "docs"}[i], Status: status, Conclusion: c})
	}
	return p
}

func TestLifecycle(t *testing.T) {
	f := lanes(t)
	gofmt, lint, deps := f.Lanes["gofmt"], f.Lanes["lint-paydown"], f.Lanes["deps"]
	poll := decide.Poll{CI: 30 * time.Second, Review: 5 * time.Minute}

	cases := []struct {
		name    string
		lane    policy.Lane
		item    item.Item
		facts   facts.Facts
		event   event.Event
		state   item.State
		actions []string
		wake    time.Duration // -1: no wake
		reason  string
	}{
		{"new eligible item is ready", gofmt, item.Item{}, facts.Facts{Ticket: open()}, ev(event.TicketMatched, nil),
			item.Ready, nil, 0, "eligible"},
		{"disabled lane ignores", deps, item.Item{}, facts.Facts{Ticket: open()}, ev(event.TicketMatched, nil),
			item.Ignored, nil, -1, "switched off"},
		{"ready starts a run", gofmt, item.Item{State: item.Ready}, facts.Facts{Ticket: open()}, ev(event.TimerDue, nil),
			item.Running, []string{decide.Run}, 2 * time.Hour, "starting run"},
		{"converged with a diff proposes", gofmt, item.Item{State: item.Running, Attempts: 1}, facts.Facts{Ticket: open()},
			ev(event.RunFinished, map[string]any{"outcome": "converged", "changed": []any{"internal/format/format.go"}}),
			item.Running, []string{decide.OpenPR}, -1, "proposing"},
		{"converged with no diff escalates", gofmt, item.Item{State: item.Running}, facts.Facts{Ticket: open()},
			ev(event.RunFinished, map[string]any{"outcome": "converged"}),
			item.Escalated, []string{decide.Escalate}, -1, "without changing anything"},
		{"runner error escalates by default", gofmt, item.Item{State: item.Running}, facts.Facts{Ticket: open()},
			ev(event.RunFinished, map[string]any{"outcome": "error", "detail": "exit 2"}),
			item.Escalated, []string{decide.Escalate}, -1, "outcome.error"},
		{"pull request opened", gofmt, item.Item{State: item.Running}, facts.Facts{Ticket: open()},
			ev(event.ActionDone, map[string]any{"action": "open_pr", "ok": true, "pr": float64(7), "branch": "ynf/issue-1"}),
			item.Proposed, nil, 30 * time.Second, "proposed as #7"},
		{"diff gate refusal escalates", gofmt, item.Item{State: item.Running}, facts.Facts{Ticket: open()},
			ev(event.ActionDone, map[string]any{"action": "open_pr", "ok": false, "reason": "touches .github/workflows/ci.yml"}),
			item.Escalated, []string{decide.Escalate}, -1, ".github/workflows"},
		{"CI pending waits", gofmt, item.Item{State: item.Proposed, PR: 7}, facts.Facts{Ticket: open(), PR: pr("success", "")},
			ev(event.TimerDue, nil), item.Proposed, nil, 30 * time.Second, "CI pending"},
		{"no checks yet waits", gofmt, item.Item{State: item.Proposed, PR: 7}, facts.Facts{Ticket: open(), PR: pr()},
			ev(event.TimerDue, nil), item.Proposed, nil, 30 * time.Second, "CI pending"},
		{"CI green is in review", gofmt, item.Item{State: item.Proposed, PR: 7}, facts.Facts{Ticket: open(), PR: pr("success", "success", "skipped")},
			ev(event.TimerDue, nil), item.InReview, nil, 5 * time.Minute, "CI green"},
		{"CI red escalates on gofmt", gofmt, item.Item{State: item.Proposed, PR: 7}, facts.Facts{Ticket: open(), PR: pr("failure", "success")},
			ev(event.TimerDue, nil), item.Escalated, []string{decide.Escalate}, -1, "ci_failed"},
		{"CI red retries on lint", lint, item.Item{State: item.Proposed, PR: 7}, facts.Facts{Ticket: open(), PR: pr("failure", "success")},
			ev(event.TimerDue, nil), item.Running, []string{decide.Run}, 2 * time.Hour, "retry 1 of 2"},
		{"CI red after retries escalates", lint, item.Item{State: item.Proposed, PR: 7, Counters: map[string]int{"retry/ci_failed": 2}},
			facts.Facts{Ticket: open(), PR: pr("failure")}, ev(event.TimerDue, nil),
			item.Escalated, []string{decide.Escalate}, -1, "after 2 retries"},
		{"merged is done", gofmt, item.Item{State: item.InReview, PR: 7}, facts.Facts{Ticket: open(), PR: &facts.PR{Number: 7, State: "closed", Merged: true}},
			ev(event.TimerDue, nil), item.Done, nil, -1, "merged"},
		{"closed pull request is closed", gofmt, item.Item{State: item.InReview, PR: 7}, facts.Facts{Ticket: open(), PR: &facts.PR{Number: 7, State: "closed"}},
			ev(event.TimerDue, nil), item.Closed, nil, -1, "without merging"},
		{"dead holder restarts the run", gofmt, item.Item{State: item.Running, Attempts: 1}, facts.Facts{Ticket: open()},
			ev(event.TimerDue, nil), item.Running, []string{decide.Run}, 2 * time.Hour, "did not finish"},
		{"too many dead holders quarantines", gofmt, item.Item{State: item.Running, Attempts: 3}, facts.Facts{Ticket: open()},
			ev(event.TimerDue, nil), item.Quarantined, []string{decide.Quarantine}, -1, "3 attempts"},
		{"closed ticket before any pull request", gofmt, item.Item{State: item.Ready}, facts.Facts{Ticket: &facts.Ticket{Number: 1, State: "closed"}},
			ev(event.TimerDue, nil), item.Closed, nil, -1, "ticket closed"},
		{"escalated waits for a human", gofmt, item.Item{State: item.Escalated}, facts.Facts{Ticket: open()},
			ev(event.TicketMatched, nil), item.Escalated, nil, -1, "without a human"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.item.Lane = c.lane.Name
			d := decide.Decide(decide.Input{Lane: c.lane, Item: c.item, Facts: c.facts, Event: c.event, Poll: poll})
			if d.Item.State != c.state {
				t.Errorf("state %s, want %s (%s)", d.Item.State, c.state, d.Reason)
			}
			var kinds []string
			for _, a := range d.Actions {
				kinds = append(kinds, a.Kind)
			}
			if !reflect.DeepEqual(kinds, c.actions) {
				t.Errorf("actions %v, want %v", kinds, c.actions)
			}
			switch {
			case c.wake < 0 && d.Item.NextDue != nil:
				t.Errorf("wakes at %v, want no wake", d.Item.NextDue)
			case c.wake >= 0 && (d.Item.NextDue == nil || d.Item.NextDue.Sub(t0) != c.wake):
				t.Errorf("wakes at %v, want +%v", d.Item.NextDue, c.wake)
			}
			if !strings.Contains(d.Reason, c.reason) {
				t.Errorf("reason %q, want it to mention %q", d.Reason, c.reason)
			}
		})
	}
}

func TestPureAndDeterministic(t *testing.T) {
	lane := lanes(t).Lanes["lint-paydown"]
	in := decide.Input{
		Lane:  lane,
		Item:  item.Item{State: item.Proposed, PR: 7, Counters: map[string]int{"retry/ci_failed": 1}},
		Facts: facts.Facts{Ticket: open(), PR: pr("failure")},
		Event: ev(event.TimerDue, nil),
		Poll:  decide.Poll{CI: time.Second, Review: time.Minute},
	}
	a, b := decide.Decide(in), decide.Decide(in)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("same input, different decisions")
	}
	if in.Item.Counters["retry/ci_failed"] != 1 {
		t.Fatal("Decide mutated its input")
	}
	if a.Actions[0].Feedback == "" || !strings.Contains(a.Actions[0].Feedback, "lint") {
		t.Fatalf("retry should carry the failing checks as feedback, got %q", a.Actions[0].Feedback)
	}
}
