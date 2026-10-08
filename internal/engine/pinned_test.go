package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/engine"
	"github.com/eyelock/ynf/internal/executor"
	"github.com/eyelock/ynf/internal/item"
	"github.com/eyelock/ynf/internal/runner"
	"github.com/eyelock/ynf/internal/store"
)

const agenticLane = `      ynh: {harness: ".", focus: tidy}`

// pinnedHarness is a harness as ynh would have installed it from a repository: a folder with its
// manifest in it, whose focus is not the repository's own.
func pinnedHarness(t *testing.T, manifest string) runner.Installed {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "harnesses", "local--chain")
	if err := os.MkdirAll(filepath.Join(dir, ".agents", "harness"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".agents", "harness", "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return runner.Installed{ID: "local/chain", Name: "chain", Version: "1.2.0", Commit: "0123456789abcdef", Path: dir}
}

func runRecord(t *testing.T, h *harness) engine.RunRecord {
	t.Helper()
	entries, err := h.e.Store.Log(context.Background(), "item/github.com/o/r/issues/1")
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(entries, func(en store.LogEntry) bool { return en.Kind == "run" })
	if i < 0 {
		t.Fatalf("no run was recorded: %+v", entries)
	}
	var rec engine.RunRecord
	if err := json.Unmarshal(entries[i].Body, &rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

// TestPinnedHarnessOnTheHostRunsWithoutACopy: a lane that pins its harness from a repository has
// it installed into a ynh home of the run's own, is held to and takes its focus from what was
// installed (never the checkout's own harness), runs it by its id, and records which harness and
// commit that was.
func TestPinnedHarnessOnTheHostRunsWithoutACopy(t *testing.T) {
	operator := t.TempDir()
	t.Setenv("YNH_HOME", operator)
	h := newHarness(t)
	calls := fakeYnh(t)
	in := &installs{result: pinnedHarness(t, strings.Replace(testManifest, "TIDY FOCUS PROMPT", "PINNED FOCUS PROMPT", 1))}
	in.result.Pin = "file:///srv/chain.git@v1.2.0"
	h.e.InstallHarness = in.install
	h.f.lanes = strings.Replace(lanesYAML, agenticLane, `      ynh: {harness: "file:///srv/chain.git@v1.2.0", focus: tidy}`, 1)
	h.e.Getenv = func(k string) string { return map[string]string{"ANTHROPIC_API_KEY": "sk-test"}[k] }
	h.f.labels[1] = []string{"ynf:agentic"}
	if err := h.e.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if it := h.item(t, 1); it.State != item.Proposed {
		t.Fatalf("%s %s %+v", it.State, it.Reason, it.LastRun)
	}
	if len(in.pins) != 1 || in.pins[0].Repo != "file:///srv/chain.git" || in.pins[0].Ref != "v1.2.0" || len(in.dirs) != 0 {
		t.Fatalf("the pin should be installed once, and no folder: %+v %v", in.pins, in.dirs)
	}
	home := in.homes[0]
	if strings.HasPrefix(home, operator) || filepath.Base(home) != "ynh" || filepath.Base(filepath.Dir(home)) != "run" {
		t.Errorf("the pin was installed into %s, not a home of the run's own (operator's %s)", home, operator)
	}
	b, _ := os.ReadFile(calls)
	log := string(b)
	if !strings.Contains(log, "agent run --harness local/chain --task @") || !strings.Contains(log, "home="+home+"\n") || !strings.Contains(log, "--profile careful") {
		t.Errorf("ynh should run the installed id with the run's home:\n%s", log)
	}
	if !strings.Contains(log, "PINNED FOCUS PROMPT") || strings.Contains(log, "TIDY FOCUS PROMPT") {
		t.Errorf("the focus should come from the installed harness, not the checkout's:\n%s", log)
	}
	rec := runRecord(t, h)
	if rec.Harness != "local/chain@1.2.0" || rec.HarnessSHA != "0123456789abcdef" || rec.HarnessPin != "file:///srv/chain.git@v1.2.0" {
		t.Errorf("the run record should say which harness and commit ran: %q %q %q", rec.Harness, rec.HarnessSHA, rec.HarnessPin)
	}
	if es, _ := os.ReadDir(operator); len(es) != 0 {
		t.Errorf("the operator's ynh home was written to: %v", es)
	}
}

// TestPinnedHarnessIsCheckedAgainstTheLane: the lane is held to the installed harness's budgets, as
// it is to any other, before a run starts.
func TestPinnedHarnessIsCheckedAgainstTheLane(t *testing.T) {
	h := newHarness(t)
	calls := fakeYnh(t)
	h.e.InstallHarness = (&installs{result: pinnedHarness(t, testManifest)}).install
	h.f.lanes = strings.Replace(lanesYAML, agenticLane, `      ynh: {harness: "github.com/o/chain@v1", focus: tidy, budgets: {max_turns: 99}}`, 1)
	h.e.Getenv = func(k string) string { return map[string]string{"ANTHROPIC_API_KEY": "sk-test"}[k] }
	h.f.labels[1] = []string{"ynf:agentic"}
	if err := h.e.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	it := h.item(t, 1)
	if it.State != item.Escalated || it.LastRun.Outcome != "operator_error" || !strings.Contains(it.LastRun.Detail, "max_turns 99 loosens the harness's 12") {
		t.Fatalf("%s %+v", it.State, it.LastRun)
	}
	if b, _ := os.ReadFile(calls); len(b) != 0 {
		t.Errorf("ynh agent run should not have started:\n%s", b)
	}
}

// TestPinnedHarnessThatCannotBeInstalledIsAnOperatorError: a tag that is not there ends the run as
// operator_error, naming the pin and ynh's reason, before any model runs.
func TestPinnedHarnessThatCannotBeInstalledIsAnOperatorError(t *testing.T) {
	h := newHarness(t)
	calls := fakeYnh(t)
	h.e.InstallHarness = (&installs{err: errors.New("ynh install: Remote branch v9 not found in upstream origin")}).install
	h.f.lanes = strings.Replace(lanesYAML, agenticLane, `      ynh: {harness: "github.com/o/chain@v9", focus: tidy}`, 1)
	h.e.Getenv = func(k string) string { return map[string]string{"ANTHROPIC_API_KEY": "sk-test"}[k] }
	h.f.labels[1] = []string{"ynf:agentic"}
	if err := h.e.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	it := h.item(t, 1)
	if it.State != item.Escalated || it.LastRun.Outcome != "operator_error" ||
		!strings.Contains(it.LastRun.Detail, "install the harness github.com/o/chain@v9") || !strings.Contains(it.LastRun.Detail, "v9 not found") {
		t.Fatalf("%s %+v", it.State, it.LastRun)
	}
	if b, _ := os.ReadFile(calls); len(b) != 0 {
		t.Errorf("ynh agent run should not have started:\n%s", b)
	}
}

// TestPinnedHarnessIsRefusedOnDocker: an image is built from a folder in the repository, so a
// harness pinned from a repository has no place to be installed for the run, and is refused with
// the reason rather than failing as a missing folder.
func TestPinnedHarnessIsRefusedOnDocker(t *testing.T) {
	h := newHarness(t)
	h.e.Interactive = false
	bin, _ := fakeDocker(t)
	proxy := filepath.Join(t.TempDir(), "ynf-linux")
	_ = os.WriteFile(proxy, []byte("x"), 0o755)
	h.e.Executor = func(string) (executor.Executor, error) { return executor.Docker{Bin: bin, ProxyBinary: proxy}, nil }
	in := &installs{result: pinnedHarness(t, testManifest)}
	h.e.InstallHarness = in.install
	h.f.lanes = strings.Replace(lanesYAML, agenticLane, `      ynh: {harness: "github.com/o/chain@v1", focus: tidy}`, 1)
	h.e.Getenv = func(k string) string { return map[string]string{"ANTHROPIC_API_KEY": "sk-test"}[k] }
	h.f.labels[1] = []string{"ynf:agentic"}
	if err := h.e.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	it := h.item(t, 1)
	if it.State != item.Escalated || it.LastRun.Outcome != "operator_error" ||
		!strings.Contains(it.LastRun.Detail, "pins harness github.com/o/chain@v1 from a repository") || !strings.Contains(it.LastRun.Detail, "host executors") {
		t.Fatalf("%s %+v", it.State, it.LastRun)
	}
	if len(in.pins) != 0 {
		t.Errorf("nothing should be fetched for a lane docker refuses: %v", in.pins)
	}
}

// TestHarnessFolderThatCannotBeReadIsAnOperatorError: a folder the lane names that is there and
// fails to read (here a manifest that is not JSON) fails the run with the error, rather than
// running the lane unchecked as if the folder were an installed id (#139).
func TestHarnessFolderThatCannotBeReadIsAnOperatorError(t *testing.T) {
	broken := t.TempDir()
	if err := os.MkdirAll(filepath.Join(broken, ".agents", "harness"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, ".agents", "harness", "plugin.json"), []byte(`{"name": "oops",`), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t)
	calls := fakeYnh(t)
	in := &installs{}
	h.e.InstallHarness = in.install
	h.f.lanes = strings.Replace(lanesYAML, agenticLane, `      ynh: {harness: "`+broken+`", focus: tidy}`, 1)
	h.e.Getenv = func(k string) string { return map[string]string{"ANTHROPIC_API_KEY": "sk-test"}[k] }
	h.f.labels[1] = []string{"ynf:agentic"}
	if err := h.e.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	it := h.item(t, 1)
	if it.State != item.Escalated || it.LastRun.Outcome != "operator_error" || !strings.Contains(it.LastRun.Detail, "harness manifest in "+broken) {
		t.Fatalf("%s %+v", it.State, it.LastRun)
	}
	if len(in.dirs) != 0 {
		t.Errorf("an unreadable folder should not be installed: %v", in.dirs)
	}
	if b, _ := os.ReadFile(calls); len(b) != 0 {
		t.Errorf("ynh agent run should not have started:\n%s", b)
	}
}

// TestHarnessFolderIsAlwaysChecked: a readable folder is held to the lane even when the lane sets
// no focus, so a budget that loosens it is refused (#139).
func TestHarnessFolderIsAlwaysChecked(t *testing.T) {
	h := newHarness(t)
	fakeYnh(t)
	h.e.InstallHarness = (&installs{}).install
	h.f.lanes = strings.Replace(lanesYAML, agenticLane, `      ynh: {harness: ".", budgets: {max_turns: 99}}`, 1)
	h.e.Getenv = func(k string) string { return map[string]string{"ANTHROPIC_API_KEY": "sk-test"}[k] }
	h.f.labels[1] = []string{"ynf:agentic"}
	if err := h.e.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if it := h.item(t, 1); it.State != item.Escalated || !strings.Contains(it.LastRun.Detail, "max_turns 99 loosens the harness's 12") {
		t.Fatalf("%s %+v", it.State, it.LastRun)
	}
}

// TestLaneRunsSayWhatAPinResolvesTo: ynf harness installs a lane's pin into a temporary home and
// says which harness and commit it is, checks the lane against it, and says why a pin is refused.
func TestLaneRunsSayWhatAPinResolvesTo(t *testing.T) {
	h := newHarness(t)
	in := &installs{result: pinnedHarness(t, testManifest)}
	h.e.InstallHarness = in.install
	h.f.lanes = strings.Replace(lanesYAML, agenticLane, `      ynh: {harness: "github.com/o/chain@v1", focus: tidy}`, 1)
	h.f.lanes += `  greedy:
    kind: originate
    intake: [{github.search: "label:ynf:greedy", every: 5m}]
    run:
      runner: ynh
      ynh: {harness: "github.com/o/chain@v1", budgets: {max_turns: 99}}
    when: {converged: open_pr}
  imaged:
    kind: originate
    intake: [{github.search: "label:ynf:imaged", every: 5m}]
    run:
      runner: ynh
      executor: docker
      ynh: {harness: "github.com/o/chain@v1"}
    when: {converged: open_pr}
`
	runs, err := h.e.LaneRuns(context.Background(), "o/r")
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]engine.LaneRun{}
	for _, r := range runs {
		by[r.Lane] = r
	}
	if r := by["agentic"]; r.Problem != "" || r.Read == nil || r.Resolved != "local/chain@1.2.0 at 0123456789abcdef" || r.Pin == nil || r.Pin.Ref != "v1" || !strings.Contains(r.Where, "pinned from github.com/o/chain at v1") {
		t.Fatalf("a pinned harness: %+v", r)
	}
	if r := by["greedy"]; !strings.Contains(r.Problem, "max_turns 99 loosens") {
		t.Fatalf("a lane that does not fit its pin: %+v", r)
	}
	if r := by["imaged"]; !strings.Contains(r.Problem, "only on the host executors") {
		t.Fatalf("a pin on docker: %+v", r)
	}
	for _, home := range in.homes {
		if _, err := os.Stat(home); err == nil {
			t.Errorf("the temporary home %s was left behind", home)
		}
	}
	in.err = errors.New("no such tag")
	runs, _ = h.e.LaneRuns(context.Background(), "o/r")
	if i := slices.IndexFunc(runs, func(r engine.LaneRun) bool { return r.Lane == "agentic" }); !strings.Contains(runs[i].Problem, "no such tag") {
		t.Errorf("a pin that cannot be installed: %+v", runs[i])
	}
	h.e.InstallHarness = nil
	runs, _ = h.e.LaneRuns(context.Background(), "o/r")
	if i := slices.IndexFunc(runs, func(r engine.LaneRun) bool { return r.Lane == "agentic" }); !strings.Contains(runs[i].Problem, "ynh is not available") {
		t.Errorf("no ynh: %+v", runs[i])
	}
}
