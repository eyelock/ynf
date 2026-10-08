package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/eyelock/ynf/internal/policy"
	"github.com/eyelock/ynf/internal/telemetry"
)

// HarnessSource is a harness to install for one run: a folder (on disk, in the run's checkout or
// absolute), or a pin from a repository. Exactly one of Dir and Pin is set.
type HarnessSource struct {
	Dir string
	Pin *policy.Pin
}

func (s HarnessSource) String() string {
	if s.Pin != nil {
		return s.Pin.String()
	}
	return s.Dir
}

// Installed is a harness as ynh installed it into a run's own home.
type Installed struct {
	// ID is what `ynh agent run --harness` takes for it.
	ID      string
	Name    string
	Version string
	// Commit is the commit it was installed from; empty for a folder, which has none.
	Commit string
	// Pin is the repository and ref it was installed from, for a pin; empty for a folder.
	Pin string
	// Path is where ynh keeps the installed harness, which holds its manifest.
	Path string
}

// Label is id@version, as ynh reports a harness in a run's result.
func (i Installed) Label() string {
	if i.Version == "" {
		return i.ID
	}
	return i.ID + "@" + i.Version
}

// InstallHarness installs src into a ynh home of its own, ynhHome, and says what ynh installed.
// `ynh agent run` accepts only an id, so a harness the lane names by folder or by pin is made one
// this way. A folder is run by ynh from where it is; a pin is cloned at its tag or commit, which
// is the one step that reaches the network, and it happens here, before the run starts and on the
// host, never inside the run's containment. The home is passed as YNH_HOME to every ynh command
// here and replaces the operator's, so the operator's own home is never read or written. The run
// needs the same YNH_HOME.
func InstallHarness(ctx context.Context, src HarnessSource, ynhHome string) (Installed, error) {
	args := []string{"install"}
	switch {
	case src.Pin != nil:
		p := *src.Pin
		if strings.HasPrefix(p.Repo, "-") || strings.HasPrefix(p.Ref, "-") {
			return Installed{}, fmt.Errorf("harness %s: a repository or ref cannot start with a dash", p)
		}
		if err := checkPinned(ctx, p); err != nil {
			return Installed{}, err
		}
		args = append(args, p.Repo, "--ref", p.Ref)
	case src.Dir != "":
		args = append(args, src.Dir)
	default:
		return Installed{}, errors.New("no harness to install")
	}
	if err := os.MkdirAll(ynhHome, 0o755); err != nil {
		return Installed{}, err
	}
	env := append(os.Environ(), "YNH_HOME="+ynhHome, "GIT_TERMINAL_PROMPT=0")
	run := func(args ...string) (string, error) {
		var out, errb bytes.Buffer
		c := exec.CommandContext(ctx, Ynh, args...)
		c.Env = env
		telemetry.Command(ctx, c)
		c.Stdout, c.Stderr = &out, &errb
		if err := c.Run(); err != nil {
			return "", fmt.Errorf("ynh %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
		}
		return out.String(), nil
	}
	if _, err := run(args...); err != nil {
		return Installed{}, err
	}
	out, err := run("ls", "--format", "json")
	if err != nil {
		return Installed{}, err
	}
	var ls struct {
		Harnesses []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Version string `json:"version_installed"`
			Path    string `json:"path"`
			From    struct {
				SHA string `json:"sha"`
			} `json:"installed_from"`
		} `json:"harnesses"`
	}
	if err := json.Unmarshal([]byte(out), &ls); err != nil || len(ls.Harnesses) != 1 || ls.Harnesses[0].ID == "" {
		return Installed{}, fmt.Errorf("ynh ls after installing %s: want the one harness, got %q", src, strings.TrimSpace(out))
	}
	h := ls.Harnesses[0]
	in := Installed{ID: h.ID, Name: h.Name, Version: h.Version, Commit: h.From.SHA, Path: h.Path}
	if src.Pin != nil {
		in.Pin = src.Pin.String()
	}
	return in, nil
}

// checkPinned refuses a pin whose ref is a branch: it moves, so the run could not be repeated.
// A ref that is neither a branch nor a tag there is left to ynh, which takes a commit and refuses
// anything it cannot find.
func checkPinned(ctx context.Context, p policy.Pin) error {
	var out, errb bytes.Buffer
	c := exec.CommandContext(ctx, "git", "ls-remote", "--", p.Repo, "refs/heads/"+p.Ref, "refs/tags/"+p.Ref)
	c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		return fmt.Errorf("harness %s: read its refs: %w: %s", p, err, strings.TrimSpace(errb.String()))
	}
	var branch, tag bool
	for _, l := range strings.Split(out.String(), "\n") {
		switch {
		case strings.HasSuffix(l, "\trefs/heads/"+p.Ref):
			branch = true
		case strings.HasSuffix(l, "\trefs/tags/"+p.Ref):
			tag = true
		}
	}
	if branch && !tag {
		return fmt.Errorf("harness %s: %s is a branch, which moves; pin a tag or a commit", p, p.Ref)
	}
	return nil
}
