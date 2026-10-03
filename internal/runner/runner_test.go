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
	argv, err := y.Command(runner.Spec{Labels: []string{"pkg:internal/store"}, TaskFile: "/r/task.md", RunDir: "/r"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{"agent run", "--focus tidy", "--max-turns 20", "--task @/r/task.md", "--emit-jsonl /r/trajectory.jsonl", `golangci-lint run ./internal/store/...`} {
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
