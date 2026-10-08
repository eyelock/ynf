package engine_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/eyelock/ynf/internal/engine"
	"github.com/eyelock/ynf/internal/item"
)

// TestARepositoryWithNoCIEscalates: a proposal whose head commit never gets a check or status
// waits on CI for no_ci_after, then escalates and says why, instead of waiting for ever. Every
// decision on the way replays the same.
func TestARepositoryWithNoCIEscalates(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.f.labels[1] = []string{"ynf:fmt", "pkg:internal/format"}
	if err := h.e.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	h.f.mu.Lock()
	h.f.prs[101].HeadSHA = "abc1234def"
	h.f.mu.Unlock()
	for range 29 {
		h.advance(time.Minute)
		if _, err := h.e.RunDue(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if it := h.item(t, 1); it.State != item.Proposed || it.NoCI == nil || it.NoCI.SHA != "abc1234def" {
		t.Fatalf("29 minutes in it still waits, and notes the bare commit: %s %+v", it.State, it.NoCI)
	}
	h.advance(2 * time.Minute)
	if _, err := h.e.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	it := h.item(t, 1)
	if it.State != item.Escalated || !strings.Contains(it.Reason, "no CI reported on abc1234 after 30m: does the repository have CI?") {
		t.Fatalf("%s %q", it.State, it.Reason)
	}
	log, _ := h.e.Store.Log(ctx, item.IssueKey("github.com", "o/r", 1))
	rs, err := engine.Replay(log, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		if !r.Same {
			t.Errorf("decision %s (%s) replayed differently", r.EntryID, r.Event)
		}
	}
}

// TestAnEmptySearchIsSaidOnce: a lane whose search finds nothing says so in one line, once until
// it finds something again, and a lane that is paused or switched off says nothing. The sweep
// still succeeds.
func TestAnEmptySearchIsSaidOnce(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	var buf bytes.Buffer
	h.e.Log = slog.New(slog.NewTextHandler(&buf, nil))
	if err := h.e.SetPaused(ctx, "o/r", "noop", true, "freeze", "david"); err != nil {
		t.Fatal(err)
	}
	if err := h.e.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "lane fmt: no matching items (GitHub search can lag a newly added label by a minute)") {
		t.Fatalf("an empty search should be said:\n%s", out)
	}
	for _, lane := range []string{"lane noop:", "lane off:"} {
		if strings.Contains(out, lane) {
			t.Errorf("%s is paused or off, so it should be quiet:\n%s", lane, out)
		}
	}
	buf.Reset()
	if err := h.e.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "no matching items") {
		t.Fatalf("said again on the next sweep:\n%s", buf.String())
	}
	// A match resets it, so the next empty search is said again.
	h.f.labels[1] = []string{"ynf:fmt", "pkg:internal/format"}
	if err := h.e.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	h.f.labels[1] = nil
	buf.Reset()
	if err := h.e.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "lane fmt: no matching items") {
		t.Fatalf("an empty search after a match should be said:\n%s", buf.String())
	}
}
