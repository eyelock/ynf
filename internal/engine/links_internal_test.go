package engine

import (
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/item"
	"github.com/eyelock/ynf/internal/policy"
	"github.com/eyelock/ynf/internal/runner"
	"github.com/eyelock/ynf/internal/tracker"
)

// TestPullRequestsAreNamedForTheirTicket: beside a GitHub issue in the same repository a pull
// request is #n, which GitHub links and closes; on any other ticket it is a URL and the ticket is
// named, not closed.
func TestPullRequestsAreNamedForTheirTicket(t *testing.T) {
	gh := item.Item{Ticket: tracker.Ref{Host: "github.com", Key: "o/r#7"}, Forge: "github.com", Repo: "o/r"}
	jira := item.Item{Ticket: tracker.Ref{Host: "example.atlassian.net", Key: "PLAT-881"}, Forge: "github.com", Repo: "o/r"}
	other := item.Item{Ticket: tracker.Ref{Host: "github.com", Key: "o/elsewhere#7"}, Forge: "github.com", Repo: "o/r"}
	if prLink(gh, 12) != "#12" || prLink(jira, 12) != "https://github.com/o/r/pull/12" || prLink(other, 12) != "https://github.com/o/r/pull/12" {
		t.Fatalf("%s %s %s", prLink(gh, 12), prLink(jira, 12), prLink(other, 12))
	}
	run := &RunRecord{Runner: "command", Executor: "docker"}
	if b := prBody(gh, policy.Lane{Name: "l"}, run); !strings.HasPrefix(b, "Closes #7.") {
		t.Fatal(b)
	}
	if b := prBody(jira, policy.Lane{Name: "l"}, run); !strings.HasPrefix(b, "For example.atlassian.net/PLAT-881.") || strings.Contains(b, "Closes") {
		t.Fatal(b)
	}
}

// TestTrailersCreditOnlyKnownVendors: Co-Authored-By carries the vendor's own address and is left
// out for a vendor whose address ynf does not know; the other trailers always stay.
func TestTrailersCreditOnlyKnownVendors(t *testing.T) {
	it := item.Item{Ticket: tracker.Ref{Host: "github.com", Key: "o/r#7"}, Forge: "github.com", Repo: "o/r"}
	for _, tc := range []struct {
		name string
		r    runner.Result
		want string
	}{
		{"claude", runner.Result{Model: "claude/opus"}, "Co-Authored-By: claude/opus <noreply@anthropic.com>\n"},
		{"backend and model", runner.Result{Model: "claude/opus", Usage: runner.Usage{Backend: "claude"}}, "Co-Authored-By: claude/opus <noreply@anthropic.com>\n"},
		{"codex", runner.Result{Model: "codex/gpt-5", Usage: runner.Usage{Backend: "codex"}}, ""},
		{"cursor", runner.Result{Model: "cursor/auto"}, ""},
		{"self-reported model", runner.Result{Model: "my-model"}, ""},
		{"no model", runner.Result{}, ""},
	} {
		got := trailers(it, "s1", "r1", tc.r)
		if (tc.want == "") == strings.Contains(got, "Co-Authored-By") || !strings.HasPrefix(got, tc.want) || !strings.Contains(got, "YNF-Run: r1") {
			t.Errorf("%s: %q", tc.name, got)
		}
	}
}
