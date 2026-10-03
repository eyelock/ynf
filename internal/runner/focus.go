package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Focus is a ynh focus: a named prompt, optionally with a profile.
type Focus struct {
	Prompt  string `json:"prompt"`
	Profile string `json:"profile"`
}

// ResolveFocus reads a focus from the harness manifest in harnessDir. `ynh agent run` takes a
// focus or a task, never both, and ynf always has a task (the ticket), so it puts the focus's
// prompt in the task itself and passes the focus's profile.
func ResolveFocus(harnessDir, name string) (Focus, error) {
	var b []byte
	var err error
	for _, m := range []string{".agents/harness/plugin.json", ".ynh-plugin/plugin.json"} {
		if b, err = os.ReadFile(filepath.Join(harnessDir, m)); err == nil {
			break
		}
	}
	if err != nil {
		return Focus{}, fmt.Errorf("focus %q: no harness manifest in %s", name, harnessDir)
	}
	var manifest struct {
		Focuses map[string]Focus `json:"focuses"`
	}
	if err := json.Unmarshal(b, &manifest); err != nil {
		return Focus{}, fmt.Errorf("focus %q: %w", name, err)
	}
	f, ok := manifest.Focuses[name]
	if !ok {
		return Focus{}, fmt.Errorf("the harness has no focus %q", name)
	}
	if f.Prompt == "" {
		return Focus{}, errors.New("focus " + name + " has no prompt")
	}
	return f, nil
}
