// Package executor runs a runner's command somewhere (ADR-007). Contained executors (docker) are
// required for unattended lanes; process is for interactive steps and shadow mode only. Every run
// gets its own worktree, run folder and tool caches.
package executor

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/eyelock/ynf/internal/egress"
)

// Container paths: what a contained command sees.
const (
	WorkDir = "/work"
	RunDir  = "/run/ynf"
)

// ErrEgress means the lane allows hosts but the egress proxy cannot be started. A containment
// control that cannot be applied is an error, never a warning (ADR-007).
var ErrEgress = errors.New("this lane allows egress, which needs the egress proxy: a linux ynf binary (make build, or YNF_LINUX_BINARY)")

// Job is one run.
type Job struct {
	Argv     []string
	Env      map[string]string
	Worktree string // host path, real (no symlinks)
	RunDir   string // host path for the run's files and caches
	Image    string
	Egress   []string
	Timeout  time.Duration
	// ImageUser keeps the image's own user and home: an agent image built by ynh keeps its
	// harness and vendor configuration under its user's home.
	ImageUser bool
	// Secrets reach the run by name only: docker reads each value from its own environment, so a
	// secret never appears in a command line or a process listing.
	Secrets map[string]string
}

// Output is how the command ended.
type Output struct {
	Exit   int
	Stdout []byte
	Stderr []byte
	Denied []string // hosts the egress proxy refused
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
		return Docker{Bin: "docker", ProxyBinary: os.Getenv("YNF_LINUX_BINARY")}, nil
	case "process":
		return Process{}, nil
	case "ecs", "k8s-job", "ci-inline":
		return nil, fmt.Errorf("executor %s is not built yet", name)
	}
	return nil, fmt.Errorf("unknown executor %q", name)
}

// Docker runs the command in a container: no network at all when the lane allows no hosts, and
// otherwise an internal network whose only way out is ynf's allow-list proxy (ADR-007).
type Docker struct {
	Bin         string
	ProxyBinary string // a static linux ynf, run as the egress proxy
	ProxyImage  string // the image the proxy runs in; default alpine:3.20
}

const proxyPort = "3128"

// Name implements Executor.
func (Docker) Name() string { return "docker" }

// Contained implements Executor.
func (Docker) Contained() bool { return true }

// Paths implements Executor.
func (Docker) Paths(Job) (string, string) { return WorkDir, RunDir }

// Args builds the job's docker command line for a container name and network; it is separate
// from Run so it can be tested.
func (d Docker) Args(j Job, name, network string) ([]string, error) {
	if j.Image == "" {
		return nil, errors.New("docker executor needs run.image in the lane")
	}
	args := []string{"run", "--rm", "--name", name, "--network", network,
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "1024"}
	if !j.ImageUser {
		args = append(args, "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()))
	}
	args = append(args, "-v", j.Worktree+":"+WorkDir, "-v", j.RunDir+":"+RunDir, "-w", WorkDir)
	env := map[string]string{
		// A fresh cache per run (ADR-007): shared caches make sensors lie.
		"XDG_CACHE_HOME":      RunDir + "/cache",
		"GOCACHE":             RunDir + "/cache/go-build",
		"GOMODCACHE":          RunDir + "/cache/go-mod",
		"GOLANGCI_LINT_CACHE": RunDir + "/cache/golangci-lint",
		// Go makes its module cache read-only by default, which leaves run folders nobody can delete.
		"GOFLAGS": "-modcacherw",
	}
	if !j.ImageUser {
		env["HOME"] = RunDir + "/home"
	}
	if network != "none" {
		proxy := "http://egress:" + proxyPort
		for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
			env[k] = proxy
		}
		env["NO_PROXY"], env["no_proxy"] = "localhost,127.0.0.1", "localhost,127.0.0.1"
	}
	for k, v := range j.Env {
		env[k] = v
	}
	for _, k := range sortedKeys(env) {
		args = append(args, "-e", k+"="+env[k])
	}
	for _, k := range sortedKeys(j.Secrets) {
		args = append(args, "-e", k)
	}
	args = append(args, j.Image)
	return append(args, j.Argv...), nil
}

// ProxyArgs builds the egress proxy container's command line.
func (d Docker) ProxyArgs(j Job, name string) []string {
	image := d.ProxyImage
	if image == "" {
		image = "alpine:3.20"
	}
	return []string{"run", "-d", "--rm", "--name", name, "--network", "bridge",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--read-only",
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"-v", d.ProxyBinary + ":/ynf:ro",
		"-v", j.RunDir + ":" + RunDir,
		image, "/ynf", "egress-proxy", "--listen", ":" + proxyPort,
		"--allow", strings.Join(j.Egress, ","), "--log", RunDir + "/egress.jsonl"}
}

// Run implements Executor.
func (d Docker) Run(ctx context.Context, j Job) (Output, error) {
	name := "ynf-" + randHex(6)
	if len(j.Egress) == 0 {
		args, err := d.Args(j, name, "none")
		if err != nil {
			return Output{}, err
		}
		return d.runContainer(ctx, j, name, args)
	}
	if d.ProxyBinary == "" {
		return Output{}, ErrEgress
	}
	if _, err := os.Stat(d.ProxyBinary); err != nil {
		return Output{}, fmt.Errorf("%w: %w", ErrEgress, err)
	}
	args, err := d.Args(j, name, name+"-net")
	if err != nil {
		return Output{}, err
	}
	bg := context.WithoutCancel(ctx)
	network, proxy := name+"-net", name+"-egress"
	if _, err := d.docker(ctx, "network", "create", "--internal", network); err != nil {
		return Output{}, err
	}
	defer func() { _, _ = d.docker(bg, "network", "rm", network) }()
	if _, err := d.docker(ctx, d.ProxyArgs(j, proxy)...); err != nil {
		return Output{}, fmt.Errorf("start egress proxy: %w", err)
	}
	defer func() { _, _ = d.docker(bg, "rm", "-f", proxy) }()
	if _, err := d.docker(ctx, "network", "connect", "--alias", "egress", network, proxy); err != nil {
		return Output{}, err
	}
	if err := d.waitReady(ctx, proxy); err != nil {
		return Output{}, err
	}
	out, err := d.runContainer(ctx, j, name, args)
	if err == nil {
		out.Denied, err = egress.Denied(filepath.Join(j.RunDir, "egress.jsonl"))
	}
	return out, err
}

// runContainer runs the job and, if ctx ends first, removes the container: killing the docker
// client alone would leave it running.
func (d Docker) runContainer(ctx context.Context, j Job, name string, args []string) (Output, error) {
	var env []string
	if len(j.Secrets) > 0 {
		env = os.Environ()
		for _, k := range sortedKeys(j.Secrets) {
			env = append(env, k+"="+j.Secrets[k])
		}
	}
	out, err := run(ctx, j.Timeout, "", d.Bin, args, env)
	if err != nil && (ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded)) {
		_, _ = d.docker(context.WithoutCancel(ctx), "rm", "-f", name)
	}
	return out, err
}

func (d Docker) waitReady(ctx context.Context, proxy string) error {
	for range 100 {
		logs, _ := d.docker(ctx, "logs", proxy)
		if strings.Contains(logs, "listening") {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return errors.New("egress proxy did not start")
}

func (d Docker) docker(ctx context.Context, args ...string) (string, error) {
	c := exec.CommandContext(ctx, d.Bin, args...)
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		return stdout.String() + stderr.String(), fmt.Errorf("docker %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String() + stderr.String(), nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
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
		"GOFLAGS=-modcacherw",
	)
	for _, m := range []map[string]string{j.Env, j.Secrets} {
		for _, k := range sortedKeys(m) {
			env = append(env, k+"="+m[k])
		}
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
