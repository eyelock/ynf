package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eyelock/ynf/internal/item"
	"github.com/eyelock/ynf/internal/runner"
)

// slowYnh is a ynh that takes a while, then converges: it stands for an agent run longer than the
// engine's RunTimeout.
func slowYnh(t *testing.T, sleep string) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
sleep ` + sleep + `
gofmt -w ./internal/format
echo '{"exit_code":0,"reason":"converged","session_id":"S-slow","backend":"claude","model":"opus"}'
`
	if err := os.WriteFile(filepath.Join(dir, "ynh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// wallHarness is a harness whose lane runs the ynh lane with budgets (max_wall wall), under a
// harness manifest that allows that much, with the engine's RunTimeout shortened to timeout.
func wallHarness(t *testing.T, wall string, timeout time.Duration) *harness {
	t.Helper()
	h := newHarness(t)
	src := filepath.Join(t.TempDir(), "src")
	git(t, "", "clone", "-q", h.remote, src)
	manifest := strings.Replace(testManifest, `"max_wall":"30m"`, `"max_wall":"`+wall+`"`, 1)
	if err := os.WriteFile(filepath.Join(src, ".agents/harness/plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, src, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-am", "a harness that allows a long wall")
	git(t, src, "push", "-q", "origin", "main")
	h.e.RunTimeout = timeout
	h.e.Getenv = func(k string) string { return map[string]string{"ANTHROPIC_API_KEY": "sk-test"}[k] }
	h.f.labels[1] = []string{"ynf:agentic"}
	return h
}

// TestAYnhLaneWithAMaxWallInDaysIsNotKilledAtTheRunTimeout drives a ynh lane through the engine:
// its max_wall is in days, as the lanes schema allows, so the executor's deadline is the max_wall
// plus a margin and a run longer than the engine's (shortened) RunTimeout still finishes.
func TestAYnhLaneWithAMaxWallInDaysIsNotKilledAtTheRunTimeout(t *testing.T) {
	h := wallHarness(t, "3d", 300*time.Millisecond)
	slowYnh(t, "1")
	h.f.lanes = strings.Replace(lanesYAML, agenticLane, `      ynh: {harness: ".", focus: tidy, budgets: {max_wall: 2d}}`, 1)
	if err := h.e.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if it := h.item(t, 1); it.State != item.Proposed {
		t.Fatalf("a 2d max_wall run was cut short: %s %s %+v", it.State, it.Reason, it.LastRun)
	}
}

// TestAYnhLaneWithNoMaxWallIsKilledAtTheRunTimeout is the other side: with no max_wall the engine's
// RunTimeout still applies, and its deadline reaches the run record as budget, not error.
func TestAYnhLaneWithNoMaxWallIsKilledAtTheRunTimeout(t *testing.T) {
	h := wallHarness(t, "3d", 300*time.Millisecond)
	slowYnh(t, "5")
	if err := h.e.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	it := h.item(t, 1)
	if it.State == item.Proposed || it.LastRun.Outcome != runner.Budget {
		t.Fatalf("a run past RunTimeout should end as %q: %s %s %+v", runner.Budget, it.State, it.Reason, it.LastRun)
	}
}
