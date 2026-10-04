package engine

import (
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/item"
	"github.com/eyelock/ynf/internal/policy"
	"github.com/eyelock/ynf/internal/tracker"
)

// TestPullRequestsAreNamedForTheirTicket: beside a GitHub issue in the same repository a pull
// request is #n, which GitHub links and closes; on any other ticket it is a URL and the ticket is
// named, not closed.
func TestPullRequestsAreNamedForTheirTicket(t *testing.T) {
	gh := item.Item{Ticket: tracker.Ref{Host: "github.com", Key: "o/r#7"}, Forge: "github.com", Repo: "o/r"}
	jira := item.Item{Ticket: tracker.Ref{Host: "acme.atlassian.net", Key: "PLAT-881"}, Forge: "github.com", Repo: "o/r"}
	other := item.Item{Ticket: tracker.Ref{Host: "github.com", Key: "o/elsewhere#7"}, Forge: "github.com", Repo: "o/r"}
	if prLink(gh, 12) != "#12" || prLink(jira, 12) != "https://github.com/o/r/pull/12" || prLink(other, 12) != "https://github.com/o/r/pull/12" {
		t.Fatalf("%s %s %s", prLink(gh, 12), prLink(jira, 12), prLink(other, 12))
	}
	run := &RunRecord{Runner: "command", Executor: "docker"}
	if b := prBody(gh, policy.Lane{Name: "l"}, run); !strings.HasPrefix(b, "Closes #7.") {
		t.Fatal(b)
	}
	if b := prBody(jira, policy.Lane{Name: "l"}, run); !strings.HasPrefix(b, "For acme.atlassian.net/PLAT-881.") || strings.Contains(b, "Closes") {
		t.Fatal(b)
	}
}
