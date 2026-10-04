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
	// Contained is set when the run is contained (ADR-007): in an image, or inline in a container
	// the operator provides.
	Contained bool
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
	Usage
}

// Usage is what a run spent and on what, when its runner reports it, so outcomes can be compared
// by model and effort (ADR-011). A command runner's result file may report it too.
type Usage struct {
	Effort string `json:"effort,omitempty"` // reasoning effort, when the runner reports it
	// EffortRequested is the effort the run was asked for, when it was: a lane's effort or the
	// harness's default. A backend may run at another, which Effort then shows.
	EffortRequested string `json:"effort_requested,omitempty"`
	Turns           int    `json:"turns,omitempty"`  // agent turns
	Tokens          int    `json:"tokens,omitempty"` // tokens consumed: input and output
	// The split, when the runner reports it. Cache reads are not in Tokens, and are often most of
	// what a long session costs.
	InputTokens     int `json:"input_tokens,omitempty"`
	OutputTokens    int `json:"output_tokens,omitempty"`
	CacheReadTokens int `json:"cache_read_tokens,omitempty"`
	// Backend is the vendor CLI that ran, such as claude: what is known when the model is not.
	Backend string  `json:"backend,omitempty"`
	CostUSD float64 `json:"cost_usd,omitempty"` // when the runner reports it; ynf never prices tokens
	BoundBy string  `json:"bound_by,omitempty"` // the cap that ended the run, if one did
	// Harness is name@version, and HarnessSHA the commit it was installed from: a harness change is
	// a confounder when comparing models, not a model effect.
	Harness       string `json:"harness,omitempty"`
	HarnessSHA    string `json:"harness_sha,omitempty"`
	RunnerVersion string `json:"runner_version,omitempty"` // ynh's version
	// FailedSensors names the sensors that had failed when the run ended, in the runner's order.
	FailedSensors []string `json:"failed_sensors,omitempty"`
	AutoApprove   string   `json:"auto_approve,omitempty"`
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
