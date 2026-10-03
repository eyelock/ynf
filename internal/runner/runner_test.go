package runner_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/policy"
	"github.com/eyelock/ynf/internal/runner"
)

func TestCommandRunner(t *testing.T) {
	r := runner.CommandRunner{Cmd: policy.Command{Argv: []string{"gofmt", "-w", "./{label.pkg}", "{task_file}"}}}
	argv, err := r.Command(runner.Spec{Labels: []string{"pkg:internal/format"}, TaskFile: "/run/task.md"})
	if err != nil || !slices.Equal(argv, []string{"gofmt", "-w", "./internal/format", "/run/task.md"}) {
		t.Fatalf("%v %v", argv, err)
	}
	if got := r.Interpret(0, nil, ""); got.Outcome != runner.Converged {
		t.Fatalf("exit 0: %+v", got)
	}
	if got := r.Interpret(2, []byte("boom"), ""); got.Outcome != runner.Error || !strings.Contains(got.Detail, "boom") {
		t.Fatalf("exit 2: %+v", got)
	}
}

func TestCommandRunnerResultFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "result.json"), []byte(`{"outcome":"stuck","detail":"no progress"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r := runner.CommandRunner{Cmd: policy.Command{Argv: []string{"x"}, ResultFile: "{run_dir}/result.json"}}
	if got := r.Interpret(1, nil, dir); got.Outcome != runner.Stuck {
		t.Fatalf("%+v", got)
	}
}

func TestYnhRunner(t *testing.T) {
	y := runner.YnhRunner{Cfg: policy.Ynh{
		Harness: ".", Focus: "tidy", Budgets: &policy.Budgets{MaxTurns: 20},
		SensorScope: map[string]string{"lint": "golangci-lint run ./{label.pkg}/..."},
	}}
	if _, err := y.Command(runner.Spec{Labels: []string{"pkg:internal/store"}}); err == nil {
		t.Fatal("a focus that was not resolved from the harness should be an error")
	}
	argv, err := y.Command(runner.Spec{Labels: []string{"pkg:internal/store"}, TaskFile: "/r/task.md", RunDir: "/r", Focus: &runner.Focus{Prompt: "Tidy.", Profile: "careful"}})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	if strings.Contains(joined, "--focus") {
		t.Errorf("ynh agent run takes a focus or a task, never both: %s", joined)
	}
	for _, want := range []string{"agent run", "--profile careful", "--max-turns 20", "--task @/r/task.md", "--emit-jsonl /r/trajectory.jsonl", `golangci-lint run ./internal/store/...`} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv lacks %q: %s", want, joined)
		}
	}
	for exit, want := range map[int]string{0: "converged", 11: "budget", 13: "stuck", 14: "tamper", 22: "operator_error", 31: "aborted", 99: "error"} {
		if got := y.Interpret(exit, []byte(`{"reason":"r","session_id":"s","backend":"claude","model":"m"}`), ""); got.Outcome != want {
			t.Errorf("exit %d: %s, want %s", exit, got.Outcome, want)
		}
	}
	if got := y.Interpret(0, []byte(`{"session_id":"s1","backend":"claude","model":"opus"}`), ""); got.Session != "s1" || got.Model != "claude/opus" {
		t.Fatalf("%+v", got)
	}
}

func TestFor(t *testing.T) {
	cmd := policy.Lane{Name: "a", Run: policy.Run{Runner: "command", Command: &policy.Command{Argv: []string{"x"}}}}
	if r, err := runner.For(cmd); err != nil || r.Name() != "command" {
		t.Fatalf("%v %v", r, err)
	}
	ynh := policy.Lane{Name: "b", Run: policy.Run{Runner: "ynh", Ynh: &policy.Ynh{Harness: "."}}}
	if r, err := runner.For(ynh); err != nil || r.Name() != "ynh" {
		t.Fatalf("%v %v", r, err)
	}
	for _, l := range []policy.Lane{
		{Name: "no command block", Run: policy.Run{Runner: "command"}},
		{Name: "no ynh block", Run: policy.Run{Runner: "ynh"}},
		{Name: "detection", Run: policy.Run{}},
		{Name: "unknown", Run: policy.Run{Runner: "make"}},
	} {
		if _, err := runner.For(l); err == nil {
			t.Errorf("%s: no error", l.Name)
		}
	}
}

func TestCommandRunnerEdges(t *testing.T) {
	r := runner.CommandRunner{Cmd: policy.Command{Argv: []string{"x", "{label.pkg}"}}}
	if _, err := r.Command(runner.Spec{}); err == nil {
		t.Fatal("missing label accepted")
	}
	long := strings.Repeat("x", 1000)
	if got := r.Interpret(1, []byte(long), ""); len(got.Detail) > 420 || !strings.HasPrefix(got.Detail, "exit 1: …") {
		t.Fatalf("detail not truncated: %d", len(got.Detail))
	}
	if got := r.Interpret(1, nil, ""); got.Detail != "exit 1" {
		t.Fatalf("%q", got.Detail)
	}
	y := runner.YnhRunner{Cfg: policy.Ynh{Harness: ".", SensorScope: map[string]string{"lint": "x {label.pkg}"}}}
	if _, err := y.Command(runner.Spec{}); err == nil {
		t.Fatal("ynh sensor scope with a missing label accepted")
	}
	full := runner.YnhRunner{Cfg: policy.Ynh{Harness: "h", Profile: "p", Sandbox: "srt", Budgets: &policy.Budgets{MaxTokens: 5, MaxWall: "10m"}}}
	argv, _ := full.Command(runner.Spec{TaskFile: "t", RunDir: "r"})
	if s := strings.Join(argv, " "); !strings.Contains(s, "--profile p") || !strings.Contains(s, "--sandbox srt") || !strings.Contains(s, "--max-tokens 5") || !strings.Contains(s, "--max-wall 10m") {
		t.Fatal(s)
	}
}

func TestYnhInImage(t *testing.T) {
	y := runner.YnhRunner{Cfg: policy.Ynh{Harness: ".", Focus: "tidy"}}
	argv, err := y.Command(runner.Spec{InImage: true, TaskFile: "/run/ynf/task.md", RunDir: "/run/ynf", Focus: &runner.Focus{Prompt: "Tidy."}})
	if err != nil || argv[0] != "--task" || slices.Contains(argv, "agent") || slices.Contains(argv, "--harness") {
		t.Fatalf("in an agent image only flags are passed: %v %v", argv, err)
	}
	if y.Vendor() != "claude" || (runner.YnhRunner{Cfg: policy.Ynh{Vendor: "codex"}}).Vendor() != "codex" {
		t.Fatal("vendor default")
	}
	if !slices.Contains(runner.ModelHosts["claude"], "api.anthropic.com") {
		t.Fatal("claude's model host")
	}
}

func TestResolveFocus(t *testing.T) {
	dir := t.TempDir()
	if _, err := runner.ResolveFocus(dir, "tidy"); err == nil {
		t.Fatal("no manifest")
	}
	_ = os.MkdirAll(filepath.Join(dir, ".agents/harness"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, ".agents/harness/plugin.json"), []byte(`{"name":"h","focuses":{"tidy":{"prompt":"Fix lint.","profile":"p"},"empty":{"prompt":""}}}`), 0o644)
	f, err := runner.ResolveFocus(dir, "tidy")
	if err != nil || f.Prompt != "Fix lint." || f.Profile != "p" {
		t.Fatalf("%+v %v", f, err)
	}
	for _, name := range []string{"nope", "empty"} {
		if _, err := runner.ResolveFocus(dir, name); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	legacy := t.TempDir()
	_ = os.MkdirAll(filepath.Join(legacy, ".ynh-plugin"), 0o755)
	_ = os.WriteFile(filepath.Join(legacy, ".ynh-plugin/plugin.json"), []byte(`{"focuses":{"t":{"prompt":"x"}}}`), 0o644)
	if f, err := runner.ResolveFocus(legacy, "t"); err != nil || f.Prompt != "x" {
		t.Fatalf("a harness not yet on .agents/harness is still read: %+v %v", f, err)
	}
	bad := t.TempDir()
	_ = os.MkdirAll(filepath.Join(bad, ".agents/harness"), 0o755)
	_ = os.WriteFile(filepath.Join(bad, ".agents/harness/plugin.json"), []byte(`not json`), 0o644)
	if _, err := runner.ResolveFocus(bad, "t"); err == nil {
		t.Fatal("bad json")
	}
}
