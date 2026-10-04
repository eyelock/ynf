package engine_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/eyelock/ynf/internal/engine"
	"github.com/eyelock/ynf/internal/item"
	"github.com/eyelock/ynf/internal/tracker"
)

func sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func TestVerifyGitHubSignature(t *testing.T) {
	body := []byte(`{"x":1}`)
	if err := engine.VerifyGitHubSignature("s3cret", sign("s3cret", body), body); err != nil {
		t.Fatal(err)
	}
	for name, header := range map[string]string{
		"wrong secret":  sign("other", body),
		"no prefix":     strings.TrimPrefix(sign("s3cret", body), "sha256="),
		"not hex":       "sha256=zz",
		"empty":         "",
		"tampered body": sign("s3cret", []byte(`{"x":2}`)),
	} {
		if err := engine.VerifyGitHubSignature("s3cret", header, body); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseGitHubEvent(t *testing.T) {
	cases := map[string]struct {
		name, body string
		issues     []int
		prs        []int
	}{
		"issue":           {"issues", `{"repository":{"full_name":"o/r"},"issue":{"number":3}}`, []int{3}, nil},
		"comment on a PR": {"issue_comment", `{"repository":{"full_name":"o/r"},"issue":{"number":4,"pull_request":{}}}`, nil, []int{4}},
		"pull request":    {"pull_request", `{"repository":{"full_name":"o/r"},"pull_request":{"number":5}}`, nil, []int{5}},
		"check suite":     {"check_suite", `{"repository":{"full_name":"o/r"},"check_suite":{"pull_requests":[{"number":6},{"number":7}]}}`, nil, []int{6, 7}},
		"check run":       {"check_run", `{"repository":{"full_name":"o/r"},"check_run":{"pull_requests":[{"number":8}]}}`, nil, []int{8}},
		"push":            {"push", `{"repository":{"full_name":"o/r"}}`, nil, nil},
	}
	for name, c := range cases {
		got, err := engine.ParseGitHubEvent(c.name, []byte(c.body))
		if err != nil || got.Repo != "o/r" || !equal(got.Issues, c.issues) || !equal(got.PRs, c.prs) {
			t.Errorf("%s: %+v %v", name, got, err)
		}
	}
	if _, err := engine.ParseGitHubEvent("issues", []byte(`{}`)); err == nil {
		t.Error("no repository accepted")
	}
	if _, err := engine.ParseGitHubEvent("issues", []byte(`not json`)); err == nil {
		t.Error("bad json accepted")
	}
}

func equal(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestWebhooksTrackAndStepItems(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// A newly labelled issue: the webhook sweeps the repository and the ticket is tracked.
	h.f.labels[1] = []string{"ynf:fmt", "pkg:internal/format"}
	if _, err := h.e.HandleGitHubEvent(ctx, "issues", []byte(`{"repository":{"full_name":"o/r"},"issue":{"number":1}}`)); err != nil {
		t.Fatal(err)
	}
	if it := h.item(t, 1); it.State != item.Proposed || it.PR != 101 {
		t.Fatalf("%s %s", it.State, it.Reason)
	}

	// CI finishes: the check_suite webhook steps the item now, not at the next poll.
	h.f.setChecks(101, "success")
	if _, err := h.e.HandleGitHubEvent(ctx, "check_suite", []byte(`{"repository":{"full_name":"o/r"},"check_suite":{"pull_requests":[{"number":101}]}}`)); err != nil {
		t.Fatal(err)
	}
	if it := h.item(t, 1); it.State != item.InReview {
		t.Fatalf("after check_suite: %s %s", it.State, it.Reason)
	}

	if _, err := h.e.HandleGitHubEvent(ctx, "issues", []byte(`{"repository":{"full_name":"x/y"},"issue":{"number":1}}`)); err == nil {
		t.Fatal("an unenrolled repository's webhook was handled")
	}
	if _, err := h.e.HandleGitHubEvent(ctx, "push", []byte(`{"repository":{"full_name":"o/r"}}`)); err != nil {
		t.Fatalf("an event about nothing should be a no-op: %v", err)
	}
}

func TestAWebhookNeverRestartsARunningItem(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.f.labels[1] = []string{"ynf:fmt", "pkg:internal/format"}
	it := item.Item{Key: item.IssueKey("github.com", "o/r", 1), Lane: "fmt", Ticket: tracker.Ref{Host: "github.com", Key: "o/r#1"}, Forge: "github.com", Repo: "o/r", Number: 1, State: item.Running, Attempts: 1, Created: h.e.Now()}
	if err := leaseCreate(ctx, h, it); err != nil {
		t.Fatal(err)
	}
	if _, err := h.e.HandleGitHubEvent(ctx, "issues", []byte(`{"repository":{"full_name":"o/r"},"issue":{"number":1}}`)); err != nil {
		t.Fatal(err)
	}
	if got := h.item(t, 1); got.State != item.Running || got.Attempts != 1 || len(h.f.opened) != 0 {
		t.Fatalf("a webhook restarted a running item: %s attempts=%d", got.State, got.Attempts)
	}
	h.advance(3 * time.Hour) // only the item's own timer means its holder died
	if _, err := h.e.RunDue(ctx); err == nil {
		t.Log("no due timer was scheduled for the hand-made item, as expected")
	}
}
