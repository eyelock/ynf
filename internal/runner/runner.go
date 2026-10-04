// Package runner is ynf's runner port (ADR-012): what a lane's inner loop is. ynh is one provider
// and any command is another; the core only ever sees ynf's own outcome vocabulary.
package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/eyelock/ynf/internal/policy"
)

// Outcomes are ynf's vocabulary (ADR-012). Lane rules branch on these, never on exit codes.
const (
	Converged     = "converged"
	Budget        = "budget"
	Stuck         = "stuck"
	Tamper        = "tamper"
	OperatorError = "operator_error"
	Error         = "error"
	Aborted       = "aborted"
)

// Spec is what a runner is asked to do.
type Spec struct {
	Lane     policy.Lane
	Labels   []string
	Task     string // the task text, written to TaskFile by the engine
	TaskFile string // path inside the worktree's step folder, as the command sees it
	RunDir   string // the step folder, as the command sees it
	Feedback string
	// InImage is set when the run happens in an image built by `ynh image --entrypoint agent`,
	// whose entrypoint is already `ynh agent run --harness local/<name>`.
	InImage bool
	// Focus is the lane's focus, resolved from the harness: its prompt is already in the task.
	Focus *Focus
	// HostAutoApprove is --auto-approve asked for by the person who started the work, for a run on
	// their own machine (ADR-007). A lane's setting never applies outside containment.
	HostAutoApprove string
}

// Result is a run's outcome in ynf's terms.
type Result struct {
	Outcome string `json:"outcome"`
	Detail  string `json:"detail,omitempty"`
	Model   string `json:"model,omitempty"`   // backend/model, when the runner reports one
	Session string `json:"session,omitempty"` // runner session id (ynh: session_id)
}

// Runner builds the command a lane runs and interprets how it ended.
type Runner interface {
	Name() string
	// Command returns the argv to run inside the worktree.
	Command(spec Spec) ([]string, error)
	// Interpret maps an exit code (and anything the run wrote to hostRunDir) to a Result.
	Interpret(exitCode int, stdout []byte, hostRunDir string) Result
}

// For returns the runner a lane names.
func For(lane policy.Lane) (Runner, error) {
	switch lane.Run.Runner {
	case "command":
		if lane.Run.Command == nil {
			return nil, fmt.Errorf("lane %s: runner command needs a command block", lane.Name)
		}
		return CommandRunner{Cmd: *lane.Run.Command}, nil
	case "ynh":
		if lane.Run.Ynh == nil {
			return nil, fmt.Errorf("lane %s: runner ynh needs a ynh block", lane.Name)
		}
		return YnhRunner{Cfg: *lane.Run.Ynh}, nil
	case "":
		return nil, fmt.Errorf("lane %s: no runner named and detection is not wired yet", lane.Name)
	}
	return nil, fmt.Errorf("lane %s: unknown runner %q", lane.Name, lane.Run.Runner)
}

// CommandRunner runs any command: exit 0 is converged, anything else an error, unless the command
// writes a result file saying more (ADR-012).
type CommandRunner struct{ Cmd policy.Command }

// Name implements Runner.
func (CommandRunner) Name() string { return "command" }

// Command implements Runner. It never uses a shell: each argument is expanded on its own.
func (c CommandRunner) Command(s Spec) ([]string, error) {
	argv := make([]string, len(c.Cmd.Argv))
	for i, a := range c.Cmd.Argv {
		a = strings.NewReplacer("{task_file}", s.TaskFile, "{run_dir}", s.RunDir).Replace(a)
		v, err := policy.Expand(a, s.Labels)
		if err != nil {
			return nil, err
		}
		argv[i] = v
	}
	return argv, nil
}

// Interpret implements Runner.
func (c CommandRunner) Interpret(exit int, stdout []byte, hostRunDir string) Result {
	if c.Cmd.ResultFile != "" {
		p := strings.NewReplacer("{run_dir}", hostRunDir).Replace(c.Cmd.ResultFile)
		if b, err := os.ReadFile(filepath.Clean(p)); err == nil {
			var r Result
			if json.Unmarshal(b, &r) == nil && r.Outcome != "" {
				return r
			}
		}
	}
	if exit == 0 {
		return Result{Outcome: Converged}
	}
	return Result{Outcome: Error, Detail: "exit " + strconv.Itoa(exit) + tail(stdout)}
}

func tail(b []byte) string {
	s := strings.TrimSpace(string(b))
	if s == "" {
		return ""
	}
	if len(s) > 400 {
		s = "…" + s[len(s)-400:]
	}
	return ": " + s
}
