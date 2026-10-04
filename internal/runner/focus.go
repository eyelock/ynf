package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/eyelock/ynf/internal/policy"
)

// Focus is a ynh focus: a named prompt, optionally with a profile.
type Focus struct {
	Prompt  string `json:"prompt"`
	Profile string `json:"profile"`
}

// Harness is what ynf reads from a ynh harness manifest: from the image that runs it (ADR-012),
// or from the folder ynh runs it from on the host.
type Harness struct {
	// ID is the installed harness's id, when it was read from an installed ynh (an image's).
	ID      string           `json:"-"`
	Focuses map[string]Focus `json:"focuses"`
	// EnvPassthrough is every variable ynh lets reach the agent worker. ynh strips everything
	// else, deliberately, model credentials and proxy settings included.
	EnvPassthrough []string `json:"env_passthrough"`
	// Agent is the harness's own budgets, which a lane may only tighten (ADR-006).
	Agent struct {
		MaxTurns  int    `json:"max_turns"`
		MaxTokens int    `json:"max_tokens"`
		MaxWall   string `json:"max_wall"`
	} `json:"agent"`
	// Sensors are the sensors the harness declares, by name; a lane may only scope these.
	Sensors map[string]json.RawMessage `json:"sensors"`
}

// ParseManifest reads a harness manifest.
func ParseManifest(b []byte) (Harness, error) {
	var h Harness
	err := json.Unmarshal(b, &h)
	return h, err
}

// CheckLane holds a lane to its harness (ADR-006): budgets may only tighten the harness's own,
// and a sensor scope may only name a sensor the harness declares.
func (h Harness) CheckLane(y policy.Ynh) error {
	var problems []string
	if b := y.Budgets; b != nil {
		if h.Agent.MaxTurns > 0 && b.MaxTurns > h.Agent.MaxTurns {
			problems = append(problems, fmt.Sprintf("max_turns %d loosens the harness's %d", b.MaxTurns, h.Agent.MaxTurns))
		}
		if h.Agent.MaxTokens > 0 && b.MaxTokens > h.Agent.MaxTokens {
			problems = append(problems, fmt.Sprintf("max_tokens %d loosens the harness's %d", b.MaxTokens, h.Agent.MaxTokens))
		}
		if b.MaxWall != "" && h.Agent.MaxWall != "" {
			lw, err1 := time.ParseDuration(b.MaxWall)
			hw, err2 := time.ParseDuration(h.Agent.MaxWall)
			if err1 != nil {
				problems = append(problems, fmt.Sprintf("max_wall %q is not a duration", b.MaxWall))
			} else if err2 == nil && lw > hw {
				problems = append(problems, fmt.Sprintf("max_wall %s loosens the harness's %s", b.MaxWall, h.Agent.MaxWall))
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(y.SensorScope)) {
		if _, ok := h.Sensors[name]; !ok {
			problems = append(problems, fmt.Sprintf("sensor_scope names %q, which the harness does not declare", name))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("the lane does not fit its harness: %s", strings.Join(problems, "; "))
	}
	return nil
}

// ReadHarness reads the manifest of the harness in harnessDir.
func ReadHarness(harnessDir string) (Harness, error) {
	var b []byte
	var err error
	for _, m := range []string{".agents/harness/plugin.json", ".ynh-plugin/plugin.json"} {
		if b, err = os.ReadFile(filepath.Join(harnessDir, m)); err == nil {
			break
		}
	}
	if err != nil {
		return Harness{}, fmt.Errorf("no harness manifest in %s", harnessDir)
	}
	h, err := ParseManifest(b)
	if err != nil {
		return Harness{}, fmt.Errorf("harness manifest in %s: %w", harnessDir, err)
	}
	return h, nil
}

// NotPassed returns the names the harness does not pass through to its worker.
func (h Harness) NotPassed(names []string) []string {
	var out []string
	for _, n := range names {
		if !slices.Contains(h.EnvPassthrough, n) && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// Focus returns a named focus. `ynh agent run` takes a focus or a task, never both, and ynf
// always has a task (the ticket), so it puts the focus's prompt in the task itself and passes the
// focus's profile.
func (h Harness) Focus(name string) (Focus, error) {
	f, ok := h.Focuses[name]
	if !ok {
		return Focus{}, fmt.Errorf("the harness has no focus %q", name)
	}
	if f.Prompt == "" {
		return Focus{}, errors.New("focus " + name + " has no prompt")
	}
	return f, nil
}

// ResolveFocus reads a focus from the harness manifest in harnessDir.
func ResolveFocus(harnessDir, name string) (Focus, error) {
	h, err := ReadHarness(harnessDir)
	if err != nil {
		return Focus{}, fmt.Errorf("focus %q: %w", name, err)
	}
	return h.Focus(name)
}
