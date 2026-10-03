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

func TestReactions(t *testing.T) {
	lane := lanes(t).Lanes["lint-paydown"] // changes_requested: resume_with review_comments, max 3
	poll := decide.Poll{CI: time.Minute, Review: time.Minute}
	inReview := facts.Facts{Ticket: open(), PR: &facts.PR{Number: 7, State: "open", ChangesRequested: true}}

	d := decide.Decide(decide.Input{Lane: lane, Item: item.Item{State: item.InReview, PR: 7}, Facts: inReview, Event: ev(event.TimerDue, nil), Poll: poll})
	if d.Item.State != item.Running || d.Actions[0].Kind != decide.Run || !strings.Contains(d.Reason, "resuming with review_comments") {
		t.Fatalf("resume: %s %v %s", d.Item.State, d.Actions, d.Reason)
	}
	d = decide.Decide(decide.Input{Lane: lane, Item: item.Item{State: item.InReview, PR: 7, Counters: map[string]int{"resume/changes_requested": 3}}, Facts: inReview, Event: ev(event.TimerDue, nil), Poll: poll})
	if d.Item.State != item.Escalated || !strings.Contains(d.Reason, "changes_requested 4 times") {
		t.Fatalf("resume past max: %s %s", d.Item.State, d.Reason)
	}

	custom := lane
	custom.When = map[string]policy.Reaction{
		"outcome.error":     {Action: "close"},
		"outcome.stuck":     {Action: "comment"},
		"outcome.budget":    {Action: "quarantine"},
		"outcome.tamper":    {Action: "push_commit"},
		"outcome.aborted":   {Retry: 1}, // no then: escalate
		"changes_requested": {ResumeWith: "review_comments", Max: 1, Then: "quarantine"},
	}
	for outcome, want := range map[string]item.State{"error": item.Closed, "stuck": item.Running, "budget": item.Quarantined, "tamper": item.Escalated} {
		d := decide.Decide(decide.Input{Lane: custom, Item: item.Item{State: item.Running}, Facts: facts.Facts{Ticket: open()},
			Event: ev(event.RunFinished, map[string]any{"outcome": outcome}), Poll: poll})
		if d.Item.State != want {
			t.Errorf("%s: %s, want %s (%s)", outcome, d.Item.State, want, d.Reason)
		}
		if outcome == "tamper" && !strings.Contains(d.Reason, "push_commit is not supported yet") {
			t.Errorf("unsupported action should say so: %s", d.Reason)
		}
	}
	it := item.Item{State: item.Running, Counters: map[string]int{"retry/outcome.aborted": 1}}
	if d := decide.Decide(decide.Input{Lane: custom, Item: it, Facts: facts.Facts{Ticket: open()}, Event: ev(event.RunFinished, map[string]any{"outcome": "aborted"}), Poll: poll}); d.Item.State != item.Escalated {
		t.Errorf("retry without then: %s", d.Item.State)
	}
	it = item.Item{State: item.InReview, PR: 7, Counters: map[string]int{"resume/changes_requested": 1}}
	if d := decide.Decide(decide.Input{Lane: custom, Item: it, Facts: inReview, Event: ev(event.TimerDue, nil), Poll: poll}); d.Item.State != item.Quarantined {
		t.Errorf("resume then quarantine: %s", d.Item.State)
	}
}

func TestEdgeEvents(t *testing.T) {
	f := lanes(t)
	gofmt := f.Lanes["gofmt"]
	poll := decide.Poll{CI: time.Minute, Review: time.Minute}

	bad := gofmt
	bad.Guards.Eligible = `facts.nope.missing == 1`
	if d := decide.Decide(decide.Input{Lane: bad, Item: item.Item{}, Facts: facts.Facts{Ticket: open()}, Event: ev(event.TicketMatched, nil), Poll: poll}); d.Item.State != item.Escalated || !strings.Contains(d.Reason, "guard failed") {
		t.Errorf("guard error: %s %s", d.Item.State, d.Reason)
	}
	no := gofmt
	no.Guards.Eligible = `facts.ticket.state == "closed"`
	if d := decide.Decide(decide.Input{Lane: no, Item: item.Item{}, Facts: facts.Facts{Ticket: open()}, Event: ev(event.TicketMatched, nil), Poll: poll}); d.Item.State != item.Ignored {
		t.Errorf("not eligible: %s", d.Item.State)
	}
	if d := decide.Decide(decide.Input{Lane: gofmt, Item: item.Item{State: item.Running}, Facts: facts.Facts{Ticket: open()}, Event: ev(event.ActionDone, map[string]any{"action": "comment"}), Poll: poll}); d.Item.State != item.Running {
		t.Errorf("other action done: %s", d.Item.State)
	}
	if d := decide.Decide(decide.Input{Lane: gofmt, Item: item.Item{State: item.Running}, Facts: facts.Facts{Ticket: open()}, Event: ev(event.TicketMatched, nil), Poll: poll}); d.Item.State != item.Running || !strings.Contains(d.Reason, "changes nothing") {
		t.Errorf("unrelated event while running: %s %s", d.Item.State, d.Reason)
	}
	if d := decide.Decide(decide.Input{Lane: gofmt, Item: item.Item{State: item.Proposed, PR: 7}, Facts: facts.Facts{Ticket: open()}, Event: ev(event.TimerDue, nil), Poll: poll}); d.Item.State != item.Escalated || !strings.Contains(d.Reason, "#7 is gone") {
		t.Errorf("vanished pull request: %s %s", d.Item.State, d.Reason)
	}
	due := t0.Add(time.Hour)
	in := item.Item{State: item.InReview, PR: 7, NextDue: &due, LastRun: &item.Run{ID: "r"}}
	d := decide.Decide(decide.Input{Lane: gofmt, Item: in, Facts: facts.Facts{Ticket: open(), PR: pr("success")}, Event: ev(event.TimerDue, nil), Poll: poll})
	if d.Item.State != item.InReview || !strings.Contains(d.Reason, "in review") || in.NextDue != &due || *in.NextDue != due {
		t.Errorf("in review stays: %s %s", d.Item.State, d.Reason)
	}
	if d := decide.Decide(decide.Input{Lane: gofmt, Item: item.Item{State: item.Running}, Facts: facts.Facts{Ticket: open()},
		Event: ev(event.ActionDone, map[string]any{"action": "open_pr", "ok": true, "pr": 9, "branch": "b"}), Poll: poll}); d.Item.PR != 9 {
		t.Errorf("int pr number: %d", d.Item.PR)
	}
	if d := decide.Decide(decide.Input{Lane: gofmt, Item: item.Item{State: item.Running}, Facts: facts.Facts{Ticket: open()},
		Event: ev(event.RunFinished, map[string]any{"outcome": "converged", "changed": []string{"a.go"}}), Poll: poll}); len(d.Actions) != 1 || d.Actions[0].Kind != decide.OpenPR {
		t.Errorf("string list of changes: %v", d.Actions)
	}
}

func TestEgressDenialsAreSignatures(t *testing.T) {
	lane := lanes(t).Lanes["lint-paydown"]
	d := decide.Decide(decide.Input{Lane: lane, Item: item.Item{State: item.Running}, Facts: facts.Facts{Ticket: open()},
		Event: ev(event.RunFinished, map[string]any{"outcome": "converged", "changed": []any{"a.go"}, "denied": []any{"www.iana.org", "sum.golang.org"}}),
		Poll:  decide.Poll{CI: time.Minute}})
	if d.Item.Counter("sig/egress/denied/www.iana.org") != 1 || d.Item.Counter("sig/egress/denied/sum.golang.org") != 1 {
		t.Fatalf("%v", d.Item.Counters)
	}
}
