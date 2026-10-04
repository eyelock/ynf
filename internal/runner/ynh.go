package runner

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/eyelock/ynf/internal/policy"
)

// YnhRunner runs `ynh agent run` (ADR-012). It owns the mapping from ynh's exit codes to ynf's
// outcomes, and every ynh-only setting: budgets, sensor scoping, the trajectory path.
type YnhRunner struct{ Cfg policy.Ynh }

// Name implements Runner.
func (YnhRunner) Name() string { return "ynh" }

// ynhOutcomes maps `ynh agent run` exit codes (ynh docs/agent.md) to ynf outcomes.
var ynhOutcomes = map[int]string{
	0:  Converged,
	10: Budget, 11: Budget, 12: Budget, 15: Budget,
	13: Stuck,
	14: Tamper,
	20: Error, 21: Error,
	22: OperatorError,
	30: Aborted, 31: Aborted,
}

// ModelHosts are the API hosts each vendor's CLI needs. The egress proxy allows the lane's vendor's
// without the lane listing them (ADR-007).
var ModelHosts = map[string][]string{
	"claude":  {"api.anthropic.com"},
	"codex":   {"api.openai.com"},
	"cursor":  {"api2.cursor.sh"},
	"copilot": {"api.githubcopilot.com", "api.github.com"},
}

// CoAuthorAddresses are the addresses for a commit's Co-Authored-By trailer, by vendor. ynf adds an
// address only when it knows the vendor's own convention; a vendor not listed gets no trailer.
var CoAuthorAddresses = map[string]string{
	"claude": "noreply@anthropic.com",
}

// CoAuthorAddress is the Co-Authored-By address for a result: by its backend, else the vendor named
// before the slash in its model. It is empty when ynf does not know the vendor's address.
func CoAuthorAddress(r Result) string {
	vendor := r.Backend
	if vendor == "" {
		vendor, _, _ = strings.Cut(r.Model, "/")
	}
	return CoAuthorAddresses[vendor]
}

// Vendor is the lane's vendor, claude unless it says otherwise.
func (y YnhRunner) Vendor() string {
	if y.Cfg.Vendor == "" {
		return "claude"
	}
	return y.Cfg.Vendor
}

// Command implements Runner. In an agent image the entrypoint already names ynh agent run and the
// harness, so only the flags are passed.
func (y YnhRunner) Command(s Spec) ([]string, error) {
	argv := []string{"ynh", "agent", "run", "--harness", y.Cfg.Harness}
	if s.InImage {
		argv = nil
	}
	argv = append(argv,
		"--task", "@"+s.TaskFile,
		"--format", "json",
		"--emit-jsonl", s.RunDir+"/trajectory.jsonl",
	)
	// ynh takes a focus or a task, not both: the focus's prompt is in the task, and its profile is
	// passed unless the lane names one.
	if y.Cfg.Focus != "" && s.Focus == nil {
		return nil, fmt.Errorf("focus %q was not resolved from the harness", y.Cfg.Focus)
	}
	profile := y.Cfg.Profile
	if profile == "" && s.Focus != nil {
		profile = s.Focus.Profile
	}
	if profile != "" {
		argv = append(argv, "--profile", profile)
	}
	if y.Cfg.Sandbox != "" {
		argv = append(argv, "--sandbox", y.Cfg.Sandbox)
	}
	// Approval prompts are only safe to switch off inside containment ynf owns (ADR-007).
	switch {
	case (s.InImage || s.Contained) && y.Cfg.AutoApprove != "":
		argv = append(argv, "--auto-approve", y.Cfg.AutoApprove)
	case !s.InImage && !s.Contained && s.HostAutoApprove != "":
		argv = append(argv, "--auto-approve", s.HostAutoApprove)
	}
	if b := y.Cfg.Budgets; b != nil {
		if b.MaxTurns > 0 {
			argv = append(argv, "--max-turns", strconv.Itoa(b.MaxTurns))
		}
		if b.MaxTokens > 0 {
			argv = append(argv, "--max-tokens", strconv.Itoa(b.MaxTokens))
		}
		if b.MaxWall != "" {
			argv = append(argv, "--max-wall", b.MaxWall)
		}
	}
	if len(y.Cfg.SensorScope) > 0 {
		overlay := map[string]any{}
		for name, tmpl := range y.Cfg.SensorScope {
			cmd, err := policy.Expand(tmpl, s.Labels)
			if err != nil {
				return nil, fmt.Errorf("sensor_scope.%s: %w", name, err)
			}
			overlay[name] = map[string]any{"source": map[string]string{"command": cmd}}
		}
		b, err := json.Marshal(overlay)
		if err != nil {
			return nil, err
		}
		argv = append(argv, "--sensor-overlay", string(b))
	}
	return argv, nil
}

// Interpret implements Runner. ynh prints one JSON object on every path with --format json.
func (y YnhRunner) Interpret(exit int, stdout []byte, _ string) Result {
	outcome, ok := ynhOutcomes[exit]
	if !ok {
		outcome = Error
	}
	r := Result{Outcome: outcome}
	var res struct {
		Reason      string `json:"reason"`
		SessionID   string `json:"session_id"`
		Backend     string `json:"backend"`
		Model       string `json:"model"`
		Effort      string `json:"effort"`
		YnhVersion  string `json:"ynh_version"`
		AutoApprove string `json:"auto_approve"`
		BoundBy     string `json:"bound_by"`
		Harness     struct {
			Name, Version, SHA string
		} `json:"harness"`
		Consumed struct {
			Turns           int     `json:"turns"`
			Tokens          int     `json:"tokens"`
			InputTokens     int     `json:"input_tokens"`
			OutputTokens    int     `json:"output_tokens"`
			CacheReadTokens int     `json:"cache_read_tokens"`
			CostUSD         float64 `json:"cost_usd"`
		} `json:"consumed"`
	}
	if json.Unmarshal(stdout, &res) == nil {
		r.Detail, r.Session = res.Reason, res.SessionID
		if res.Model != "" {
			r.Model = res.Backend + "/" + res.Model
		}
		r.Usage = Usage{
			Effort: res.Effort, Turns: res.Consumed.Turns, Tokens: res.Consumed.Tokens, CostUSD: res.Consumed.CostUSD,
			InputTokens: res.Consumed.InputTokens, OutputTokens: res.Consumed.OutputTokens, CacheReadTokens: res.Consumed.CacheReadTokens,
			Backend: res.Backend, BoundBy: res.BoundBy, HarnessSHA: res.Harness.SHA, RunnerVersion: res.YnhVersion, AutoApprove: res.AutoApprove,
		}
		if res.Harness.Name != "" {
			r.Harness = res.Harness.Name + "@" + res.Harness.Version
		}
	}
	if !ok {
		r.Detail = fmt.Sprintf("unknown ynh exit %d: %s", exit, r.Detail)
	}
	return r
}
