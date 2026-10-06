// Command calibrate proves the sandbox's fixtures can still tell a fixed state from an unfixed one.
//
// It tests the test rig, not the factory: no agent and no ynf run. It clones the live sandbox and,
// for each fixture in fixtures.yaml, runs the lane's scoped sensors through `ynh check` with the
// same --sensor-overlay the lane would use: the sensors named in `before` must fail, and after
// the fixture's known fix only those in `after` may. A fixture that cannot produce that
// reproducible negative cannot tell a good agent run from a bad one, which is what an end-to-end
// test of the factory relies on. The idea is ynh's `ynh check --calibrate`, applied to fixtures.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

type fixtureFile struct {
	Fixtures []fixture `yaml:"fixtures"`
}

type fixture struct {
	ID          string   `yaml:"id"`
	Kind        string   `yaml:"kind"`
	Labels      []string `yaml:"labels"`
	Lane        string   `yaml:"lane"`
	PullRequest *struct {
		Branch string `yaml:"branch"`
	} `yaml:"pull_request"`
	Calibrate calibration `yaml:"calibrate"`
}

type calibration struct {
	Before   []string                      `yaml:"before"`
	Fix      string                        `yaml:"fix"`
	After    []string                      `yaml:"after"`
	Flaky    *flaky                        `yaml:"flaky"`
	Command  *struct{ Deterministic bool } `yaml:"command"`
	Disabled bool                          `yaml:"disabled"`
	Refused  bool                          `yaml:"refused"`
}

type flaky struct {
	Sensor      string `yaml:"sensor"`
	Runs        int    `yaml:"runs"`
	MinFailures int    `yaml:"min_failures"`
}

type laneFile struct {
	Lanes map[string]lane `yaml:"lanes"`
}

type lane struct {
	Enabled *bool `yaml:"enabled"`
	Run     struct {
		Runner string `yaml:"runner"`
		Ynh    struct {
			SensorScope map[string]string `yaml:"sensor_scope"`
		} `yaml:"ynh"`
		Command struct {
			Argv []string `yaml:"argv"`
		} `yaml:"command"`
	} `yaml:"run"`
}

var safeValue = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

func main() {
	root := flag.String("root", ".", "the sandbox/ directory")
	repo := flag.String("repo", "eyelock/ynf-sandbox", "the sandbox repository")
	only := flag.String("only", "", "calibrate only this fixture id")
	flag.Parse()

	if err := run(*root, *repo, "ynh", *only); err != nil {
		fmt.Fprintln(os.Stderr, "calibrate:", err)
		os.Exit(1)
	}
}

func run(root, repo, ynh, only string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	var ff fixtureFile
	if err := readYAML(filepath.Join(root, "fixtures.yaml"), &ff); err != nil {
		return err
	}

	tmp, err := os.MkdirTemp("", "ynf-calibrate-")
	if err != nil {
		return err
	}
	// macOS's temp dir is under /var, a symlink to /private/var. git reports changed files under
	// the real path and the linter under the symlinked one, so --new-from-merge-base matches
	// nothing and a sensor passes code it should fail. Work under the real path.
	if tmp, err = filepath.EvalSymlinks(tmp); err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }() // best effort: it is a temp dir

	origin := filepath.Join(tmp, "origin")
	if _, err := cmd("", "gh", "repo", "clone", repo, origin, "--", "-q"); err != nil {
		return fmt.Errorf("clone %s: %w", repo, err)
	}
	if _, err := cmd(origin, "git", "fetch", "-q", "origin", "+refs/heads/*:refs/remotes/origin/*"); err != nil {
		return err
	}
	if err := preflight(ynh, origin); err != nil {
		return err
	}

	var lf laneFile
	if err := readYAML(filepath.Join(origin, ".agents", "factory", "lanes.yaml"), &lf); err != nil {
		return err
	}

	failed := 0
	for _, f := range ff.Fixtures {
		if only != "" && f.ID != only {
			continue
		}
		v := calibrator{root: root, origin: origin, tmp: tmp, ynh: ynh}
		detail, err := v.fixture(f, lf.Lanes[f.Lane])
		mark := "ok  "
		if err != nil {
			mark, detail = "FAIL", err.Error()
			failed++
		}
		fmt.Printf("%s  %-26s %s\n", mark, f.ID, detail)
	}
	if failed > 0 {
		return fmt.Errorf("%d fixture(s) cannot tell fixed from unfixed", failed)
	}
	return nil
}

// preflight fails early, and legibly, when ynh cannot read the sandbox's harness.
func preflight(ynh, dir string) error {
	if _, err := exec.LookPath(ynh); err != nil {
		return fmt.Errorf("ynh is not on PATH (%s)", ynh)
	}
	_, err := cmd(dir, ynh, "check", dir, "--cwd", dir, "--no-baseline", "--only", "docs", "--format", "json")
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() == 2) {
		return fmt.Errorf("%s cannot run the sandbox harness at .agents/harness/; it needs ynh from develop 027dc19 or later, first on PATH: %w", ynh, err)
	}
	return nil
}

type calibrator struct {
	root, origin, tmp, ynh string
}

func (v calibrator) fixture(f fixture, l lane) (string, error) {
	switch {
	case f.Calibrate.Disabled:
		if l.Enabled == nil || *l.Enabled {
			return "", fmt.Errorf("lane %s is enabled; expected it switched off", f.Lane)
		}
		return fmt.Sprintf("lane %s is switched off", f.Lane), nil
	case f.Calibrate.Refused:
		// ynf refuses the lane's scope before any run (ADR-006); `make e2e` watches it do so.
		if len(l.Run.Ynh.SensorScope) == 0 {
			return "", fmt.Errorf("lane %s has no sensor_scope, so there is nothing for ynf to refuse", f.Lane)
		}
		return fmt.Sprintf("lane %s is refused before any run; nothing to calibrate", f.Lane), nil
	case f.Calibrate.Command != nil:
		return v.command(f, l)
	default:
		return v.sensors(f, l)
	}
}

func (v calibrator) sensors(f fixture, l lane) (string, error) {
	pkg, err := label(f, "pkg")
	if err != nil {
		return "", err
	}
	overlay := map[string]any{}
	for name, tmpl := range l.Run.Ynh.SensorScope {
		overlay[name] = map[string]any{"source": map[string]string{"command": strings.ReplaceAll(tmpl, "{label.pkg}", pkg)}}
	}
	ov, _ := json.Marshal(overlay)

	wt, err := v.worktree(f, "check")
	if err != nil {
		return "", err
	}
	var ignore string
	if f.Calibrate.Flaky != nil {
		ignore = f.Calibrate.Flaky.Sensor
	}

	before, err := v.failing(wt, string(ov), ignore)
	if err != nil {
		return "", err
	}
	if !sameSet(before, f.Calibrate.Before) {
		return "", fmt.Errorf("before the fix %v failed, expected %v", before, f.Calibrate.Before)
	}
	detail := fmt.Sprintf("before %v", before)

	if f.Calibrate.Flaky != nil {
		fl := f.Calibrate.Flaky
		fails := 0
		for range fl.Runs {
			out, err := v.failing(wt, string(ov), "", "--only", fl.Sensor)
			if err != nil {
				return "", err
			}
			if slices.Contains(out, fl.Sensor) {
				fails++
			}
		}
		if fails < fl.MinFailures {
			return "", fmt.Errorf("flaky %s failed %d/%d, expected at least %d", fl.Sensor, fails, fl.Runs, fl.MinFailures)
		}
		detail += fmt.Sprintf(", flaky %s %d/%d", fl.Sensor, fails, fl.Runs)
	}

	if f.Calibrate.Fix == "" {
		return detail + ", no fix to apply", nil
	}
	if _, err := cmd(wt, "git", "apply", filepath.Join(v.root, f.Calibrate.Fix)); err != nil {
		return "", fmt.Errorf("apply %s: %w", f.Calibrate.Fix, err)
	}
	after, err := v.failing(wt, string(ov), ignore)
	if err != nil {
		return "", err
	}
	if !sameSet(after, f.Calibrate.After) {
		return "", fmt.Errorf("after the fix %v failed, expected %v", after, f.Calibrate.After)
	}
	return detail + fmt.Sprintf(", after %v", after), nil
}

// command runs a command-runner lane twice on fresh worktrees: it must change something, and the
// same input must produce the same diff.
func (v calibrator) command(f fixture, l lane) (string, error) {
	pkg, err := label(f, "pkg")
	if err != nil {
		return "", err
	}
	argv := slices.Clone(l.Run.Command.Argv)
	if len(argv) == 0 {
		return "", fmt.Errorf("lane %s has no command", f.Lane)
	}
	for i := range argv {
		argv[i] = strings.ReplaceAll(argv[i], "{label.pkg}", pkg)
	}
	var diffs [2]string
	for i := range diffs {
		wt, err := v.worktree(f, fmt.Sprintf("cmd%d", i))
		if err != nil {
			return "", err
		}
		if _, err := cmd(wt, argv[0], argv[1:]...); err != nil {
			return "", fmt.Errorf("%s: %w", strings.Join(argv, " "), err)
		}
		if diffs[i], err = cmd(wt, "git", "diff"); err != nil {
			return "", err
		}
	}
	if diffs[0] == "" {
		return "", fmt.Errorf("%s changed nothing", strings.Join(argv, " "))
	}
	if diffs[0] != diffs[1] {
		return "", fmt.Errorf("%s produced different diffs on identical input", strings.Join(argv, " "))
	}
	return fmt.Sprintf("%s: same %d-line diff twice", strings.Join(argv, " "), strings.Count(diffs[0], "\n")), nil
}

func (v calibrator) worktree(f fixture, suffix string) (string, error) {
	ref := "origin/main"
	if f.Kind == "pull_request" && f.PullRequest != nil {
		ref = "origin/" + f.PullRequest.Branch
	}
	wt := filepath.Join(v.tmp, f.ID+"-"+suffix)
	if _, err := cmd(v.origin, "git", "worktree", "add", "-q", "--detach", wt, ref); err != nil {
		return "", fmt.Errorf("worktree at %s: %w", ref, err)
	}
	return wt, nil
}

// failing runs ynh check and returns the names of the sensors that failed, minus ignore.
func (v calibrator) failing(dir, overlay, ignore string, extra ...string) ([]string, error) {
	args := append([]string{"check", dir, "--cwd", dir, "--no-baseline", "--format", "json", "--sensor-overlay", overlay}, extra...)
	out, err := cmd(dir, v.ynh, args...)
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
		return nil, fmt.Errorf("ynh check: %w\n%s", err, out)
	}
	var res struct {
		Sensors []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"sensors"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return nil, fmt.Errorf("ynh check output: %w", err)
	}
	var names []string
	for _, s := range res.Sensors {
		if s.Status == "fail" && s.Name != ignore {
			names = append(names, s.Name)
		}
	}
	slices.Sort(names)
	return names, nil
}

func label(f fixture, prefix string) (string, error) {
	for _, l := range f.Labels {
		if v, ok := strings.CutPrefix(l, prefix+":"); ok {
			if !safeValue.MatchString(v) {
				return "", fmt.Errorf("label %q is not a safe value", l)
			}
			return v, nil
		}
	}
	return "", fmt.Errorf("no %s: label", prefix)
}

func sameSet(got, want []string) bool {
	w := slices.Clone(want)
	slices.Sort(w)
	return slices.Equal(got, w)
}

// cmd runs name in dir and returns stdout; stderr is folded into the error.
func cmd(dir, name string, args ...string) (string, error) {
	c := exec.Command(name, args...)
	c.Dir = dir
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		if s := strings.TrimSpace(stderr.String()); s != "" {
			return stdout.String(), fmt.Errorf("%w: %s", err, s)
		}
		return stdout.String(), err
	}
	return stdout.String(), nil
}

func readYAML(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}
