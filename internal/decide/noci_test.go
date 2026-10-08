package decide_test

import (
	"strings"
	"testing"
	"time"

	"github.com/eyelock/ynf/internal/decide"
	"github.com/eyelock/ynf/internal/event"
	"github.com/eyelock/ynf/internal/facts"
	"github.com/eyelock/ynf/internal/item"
	"github.com/eyelock/ynf/internal/policy"
)

// TestNoCI: a head commit with no check or status of any kind for no_ci_after escalates, saying
// why; before that, with checks, for another commit, with the reaction off, or with a required
// check that is only expected, it waits as it always did.
func TestNoCI(t *testing.T) {
	gofmt := lanes(t).Lanes["gofmt"]
	poll := decide.Poll{CI: 30 * time.Second, Review: 5 * time.Minute}
	seenAt := func(ago time.Duration, sha string) *item.NoCI { return &item.NoCI{SHA: sha, Since: t0.Add(-ago)} }
	off := gofmt
	off.NoCIAfter = "0s"
	short := gofmt
	short.NoCIAfter = "10m"
	comment := gofmt
	comment.When = map[string]policy.Reaction{"converged": gofmt.When["converged"], "no_ci": {Action: "comment"}}

	cases := []struct {
		name   string
		lane   policy.Lane
		item   item.Item
		pr     *facts.PR
		state  item.State
		reason string
	}{
		{"just seen bare", gofmt, item.Item{State: item.Proposed, PR: 7, NoCI: seenAt(0, "abc")}, pr(), item.Proposed, "CI pending"},
		{"bare for 29 minutes", gofmt, item.Item{State: item.Proposed, PR: 7, NoCI: seenAt(29*time.Minute, "abc")}, pr(), item.Proposed, "CI pending"},
		{"bare for the default 30 minutes", gofmt, item.Item{State: item.Proposed, PR: 7, NoCI: seenAt(30*time.Minute, "abc")}, pr(), item.Escalated,
			"no CI reported on abc after 30m: does the repository have CI?"},
		{"the lane's own wait", short, item.Item{State: item.Proposed, PR: 7, NoCI: seenAt(11*time.Minute, "abc")}, pr(), item.Escalated, "after 10m"},
		{"switched off", off, item.Item{State: item.Proposed, PR: 7, NoCI: seenAt(24*time.Hour, "abc")}, pr(), item.Proposed, "CI pending"},
		{"a different head commit", gofmt, item.Item{State: item.Proposed, PR: 7, NoCI: seenAt(time.Hour, "old")}, pr(), item.Proposed, "CI pending"},
		{"something reported", gofmt, item.Item{State: item.Proposed, PR: 7, NoCI: seenAt(time.Hour, "abc")}, pr(""), item.Proposed, "CI pending"},
		{"a required check expected", gofmt, item.Item{State: item.Proposed, PR: 7, NoCI: seenAt(time.Hour, "abc")}, notStarted(), item.Proposed, "CI pending"},
		{"a record from before this existed", gofmt, item.Item{State: item.Proposed, PR: 7}, pr(), item.Proposed, "CI pending"},
		{"the reaction is the lane's", comment, item.Item{State: item.Proposed, PR: 7, NoCI: seenAt(time.Hour, "abc")}, pr(), item.Proposed, "no CI reported on abc"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := decide.Decide(decide.Input{Lane: c.lane, Item: c.item, Facts: facts.Facts{Ticket: open(), PR: c.pr}, Event: ev(event.TimerDue, nil), Poll: poll})
			if d.Item.State != c.state || !strings.Contains(d.Reason, c.reason) {
				t.Fatalf("%s %q, want %s containing %q", d.Item.State, d.Reason, c.state, c.reason)
			}
			if d.Item.State == item.Escalated && (len(d.Actions) != 1 || d.Actions[0].Kind != decide.Escalate || d.Item.NoCI != nil) {
				t.Fatalf("an escalation tells a human and drops the marker: %+v %+v", d.Actions, d.Item.NoCI)
			}
		})
	}
}
