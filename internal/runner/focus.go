package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// Focus is a ynh focus: a named prompt, optionally with a profile.
type Focus struct {
	Prompt  string `json:"prompt"`
	Profile string `json:"profile"`
}

// Harness is what ynf reads from a ynh harness manifest.
type Harness struct {
	Focuses map[string]Focus `json:"focuses"`
	// EnvPassthrough is every variable ynh lets reach the agent worker. ynh strips everything
	// else, deliberately, model credentials and proxy settings included.
	EnvPassthrough []string `json:"env_passthrough"`
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
	var h Harness
	if err := json.Unmarshal(b, &h); err != nil {
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
