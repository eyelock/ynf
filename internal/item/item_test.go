package item_test

import (
	"testing"
	"time"

	"github.com/eyelock/ynf/internal/item"
)

func TestSubjectRoundTrip(t *testing.T) {
	s := item.IssueSubject("eyelock/ynf-sandbox", 10)
	repo, n, err := item.ParseIssueSubject(s)
	if err != nil || repo != "eyelock/ynf-sandbox" || n != 10 {
		t.Fatalf("%s -> %s %d %v", s, repo, n, err)
	}
	for _, bad := range []string{"github:pr:o/r#1", "github:issue:o/r", "github:issue:o/r#x"} {
		if _, _, err := item.ParseIssueSubject(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
	if item.IssueKey("o/r", 3) != "item/github/o/r/issues/3" || item.BranchFor(3) != "ynf/issue-3" {
		t.Fatal("key or branch format changed")
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
