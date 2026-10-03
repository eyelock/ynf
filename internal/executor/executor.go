// Package executor runs a runner's command somewhere (ADR-007). Contained executors (docker) are
// required for unattended lanes; process is for interactive steps and shadow mode only. Every run
// gets its own worktree, run folder and tool caches.
package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"
)

// Container paths: what a contained command sees.
const (
	WorkDir = "/work"
	RunDir  = "/run/ynf"
)

// ErrEgress means the lane allows hosts, which needs the egress proxy (slice 1b). A containment
// control that cannot be applied is an error, never a warning (ADR-007).
var ErrEgress = errors.New("egress allow lists need the egress proxy, which is not built yet; this lane cannot run contained")

// Job is one run.
type Job struct {
	Argv     []string
	Env      map[string]string
	Worktree string // host path, real (no symlinks)
	RunDir   string // host path for the run's files and caches
	Image    string
	Egress   []string
	Timeout  time.Duration
}

// Output is how the command ended.
type Output struct {
	Exit   int
	Stdout []byte
	Stderr []byte
}

// Executor runs jobs.
type Executor interface {
	Name() string
	// Contained reports whether the executor confines the run (ADR-007).
	Contained() bool
	// Paths returns the worktree and run folder as the command sees them.
	Paths(j Job) (worktree, runDir string)
	Run(ctx context.Context, j Job) (Output, error)
}

// For returns the executor a lane names.
func For(name string) (Executor, error) {
	switch name {
	case "docker":
		return Docker{Bin: "docker"}, nil
	case "process":
		return Process{}, nil
	case "ecs", "k8s-job", "ci-inline":
		return nil, fmt.Errorf("executor %s is not built yet", name)
	}
	return nil, fmt.Errorf("unknown executor %q", name)
}

// Docker runs the command in a container with no network unless the lane allows hosts.
type Docker struct{ Bin string }

// Name implements Executor.
func (Docker) Name() string { return "docker" }

// Contained implements Executor.
func (Docker) Contained() bool { return true }

// Paths implements Executor.
func (Docker) Paths(Job) (string, string) { return WorkDir, RunDir }

// Args builds the docker command line; it is separate from Run so it can be tested.
func (d Docker) Args(j Job) ([]string, error) {
	if j.Image == "" {
		return nil, errors.New("docker executor needs run.image in the lane")
	}
	if len(j.Egress) > 0 {
		return nil, ErrEgress
	}
	args := []string{"run", "--rm", "--network", "none",
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"-v", j.Worktree + ":" + WorkDir,
		"-v", j.RunDir + ":" + RunDir,
		"-w", WorkDir,
	}
	env := map[string]string{
		// A fresh cache per run (ADR-007): shared caches make sensors lie.
		"HOME":                RunDir + "/home",
		"XDG_CACHE_HOME":      RunDir + "/cache",
		"GOCACHE":             RunDir + "/cache/go-build",
		"GOLANGCI_LINT_CACHE": RunDir + "/cache/golangci-lint",
		"GOFLAGS":             "-mod=mod",
	}
	for k, v := range j.Env {
		env[k] = v
	}
	for _, k := range sortedKeys(env) {
		args = append(args, "-e", k+"="+env[k])
	}
	args = append(args, j.Image)
	return append(args, j.Argv...), nil
}

// Run implements Executor.
func (d Docker) Run(ctx context.Context, j Job) (Output, error) {
	args, err := d.Args(j)
	if err != nil {
		return Output{}, err
	}
	return run(ctx, j.Timeout, "", d.Bin, args, nil)
}

// Process runs the command directly on the host. It is not contained, so ynf only allows it for
// interactive steps and shadow mode (ADR-007).
type Process struct{}

// Name implements Executor.
func (Process) Name() string { return "process" }

// Contained implements Executor.
func (Process) Contained() bool { return false }

// Paths implements Executor.
func (Process) Paths(j Job) (string, string) { return j.Worktree, j.RunDir }

// Run implements Executor.
func (Process) Run(ctx context.Context, j Job) (Output, error) {
	if len(j.Argv) == 0 {
		return Output{}, errors.New("empty command")
	}
	cache := filepath.Join(j.RunDir, "cache")
	env := append(os.Environ(),
		"XDG_CACHE_HOME="+cache,
		"GOCACHE="+filepath.Join(cache, "go-build"),
		"GOLANGCI_LINT_CACHE="+filepath.Join(cache, "golangci-lint"),
	)
	for _, k := range sortedKeys(j.Env) {
		env = append(env, k+"="+j.Env[k])
	}
	return run(ctx, j.Timeout, j.Worktree, j.Argv[0], j.Argv[1:], env)
}

func run(ctx context.Context, timeout time.Duration, dir, name string, args, env []string) (Output, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	c := exec.CommandContext(ctx, name, args...)
	c.Dir, c.Env = dir, env
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	err := c.Run()
	out := Output{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	var exit *exec.ExitError
	switch {
	case err == nil:
		return out, nil
	case errors.As(err, &exit) && ctx.Err() == nil:
		out.Exit = exit.ExitCode()
		return out, nil
	case ctx.Err() != nil:
		return out, fmt.Errorf("%s: %w", name, ctx.Err())
	}
	return out, fmt.Errorf("%s: %w", name, err)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
