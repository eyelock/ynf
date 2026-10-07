package item_test

import (
	"testing"
	"time"

	"github.com/eyelock/ynf/internal/item"
	"github.com/eyelock/ynf/internal/tracker"
)

// TestKeysAreTheSystemsOwn: keys and references carry the system's host and the tracker's own
// key, never a configured name (ADR-002).
func TestKeysAreTheSystemsOwn(t *testing.T) {
	gh := item.Item{Ticket: tracker.Ref{Host: "github.com", Key: "eyelock/ynf-sandbox#10"}}
	if k := item.Key(gh.Ticket); k != "item/github.com/eyelock/ynf-sandbox/issues/10" {
		t.Fatal(k)
	}
	if gh.Subject() != "github.com/eyelock/ynf-sandbox#10" || gh.Ref() != gh.Subject() || gh.BranchName() != "ynf/issue-10" {
		t.Fatalf("%s %s %s", gh.Subject(), gh.Ref(), gh.BranchName())
	}
	if item.IssueKey("github.example.internal", "example-org/x", 3) != "item/github.example.internal/example-org/x/issues/3" {
		t.Fatal("an Enterprise Server key")
	}
	if item.PRKey("github.com", "o/r", 412) != "item/github.com/o/r/pulls/412" {
		t.Fatal("a pull request key")
	}
	jira := item.Item{Ticket: tracker.Ref{Host: "example.atlassian.net", Key: "PLAT-881"}}
	if item.Key(jira.Ticket) != "item/example.atlassian.net/PLAT-881" || jira.Ref() != "example.atlassian.net/PLAT-881" || jira.BranchName() != "ynf/plat-881" {
		t.Fatalf("%s %s %s", item.Key(jira.Ticket), jira.Ref(), jira.BranchName())
	}
	odd := item.Item{Ticket: tracker.Ref{Host: "x.example", Key: "A B/C#d"}}
	if odd.BranchName() != "ynf/a-b-c-d" {
		t.Fatal(odd.BranchName())
	}
}

func TestStates(t *testing.T) {
	for s, want := range map[item.State][2]bool{
		item.Ready: {false, false}, item.Running: {false, false}, item.Proposed: {false, false},
		item.InReview: {true, false}, item.Escalated: {true, false}, item.Quarantined: {true, false},
		item.Done: {true, true}, item.Closed: {true, true}, item.Ignored: {true, true},
	} {
		if s.Settled() != want[0] || s.Final() != want[1] {
			t.Errorf("%s: settled %v final %v, want %v", s, s.Settled(), s.Final(), want)
		}
	}
}

func TestLeaseAndCounters(t *testing.T) {
	now := time.Now()
	var nilLease *item.Lease
	if nilLease.Held(now) {
		t.Fatal("nil lease held")
	}
	l := &item.Lease{ExpiresAt: now.Add(time.Second)}
	if !l.Held(now) || l.Held(now.Add(2*time.Second)) {
		t.Fatal("lease expiry wrong")
	}
	var it item.Item
	if it.Bump("x") != 1 || it.Bump("x") != 2 || it.Counter("x") != 2 || it.Counter("y") != 0 {
		t.Fatal("counters wrong")
	}
}
