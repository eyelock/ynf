package executor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
)

// Inline runs the runner as a process inside the container ynf itself runs in: the factory image,
// as a job (ADR-007, ADR-009). The job runner provides the containment (the network policy, the
// dropped capabilities); ynf keeps its credentials from the run by running it as another user.
//
// ynf runs as root in the container with only SETUID, SETGID and CHOWN. Before a run it hands the
// run's folders to the run user; it starts the run
// as that user with only the run's own environment; afterwards it takes the folders back. The run
// cannot read ynf's environment or memory (another user, no SYS_PTRACE) or its token.
type Inline struct {
	// User is the user runs run as; default ynh, the user an agent image is built for.
	User string
	// Chown changes a path's owner; nil is os.Lchown. For tests.
	Chown func(path string, uid, gid int) error
	// Credential sets the user a command runs as; nil runs it as that user. For tests.
	Credential func(c *exec.Cmd, uid, gid uint32)
}

// Name implements Executor.
func (Inline) Name() string { return "inline" }

// Contained implements Executor: the operator declared the containment (ADR-007).
func (Inline) Contained() bool { return true }

// Paths implements Executor.
func (Inline) Paths(j Job) (string, string) { return j.Worktree, j.RunDir }

// Run implements Executor.
func (in Inline) Run(ctx context.Context, j Job) (Output, error) {
	if len(j.Argv) == 0 {
		return Output{}, errors.New("empty command")
	}
	name := in.User
	if name == "" {
		name = "ynh"
	}
	u, err := user.Lookup(name)
	if err != nil {
		return Output{}, fmt.Errorf("inline: the run user %q: %w", name, err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	if uid == os.Getuid() {
		return Output{}, fmt.Errorf("inline: ynf runs as %s, the run user: run ynf as another user, or the run could read its credentials", name)
	}
	owned := append([]string{j.Worktree, j.RunDir}, j.Share...)
	if j.Spool != "" {
		owned = append(owned, j.Spool) // the run's own folder, and only that one
	}
	if err := in.chownAll(owned, uid, gid); err != nil {
		return Output{}, fmt.Errorf("inline: hand the run's folders to %s: %w", name, err)
	}
	// Whatever happens, take them back: ynf commits, pushes and cleans up as itself.
	defer func() { _ = in.chownAll(owned, os.Getuid(), os.Getgid()) }()

	cache := filepath.Join(j.RunDir, "cache")
	env := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + u.HomeDir, "USER=" + name, "LOGNAME=" + name,
		"XDG_CACHE_HOME=" + cache,
		"GOCACHE=" + filepath.Join(cache, "go-build"),
		"GOLANGCI_LINT_CACHE=" + filepath.Join(cache, "golangci-lint"),
		"GOFLAGS=-modcacherw",
	}
	// The job runner's network may route through a proxy; the run needs to know it.
	for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "no_proxy"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	env = appendJobEnv(env, j)
	set := in.Credential
	if set == nil {
		set = func(c *exec.Cmd, uid, gid uint32) {
			c.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: gid}}
		}
	}
	return runAs(ctx, j.Timeout, j.Worktree, j.Argv[0], j.Argv[1:], env, func(c *exec.Cmd) { set(c, uint32(uid), uint32(gid)) })
}

func (in Inline) chownAll(paths []string, uid, gid int) error {
	chown := in.Chown
	if chown == nil {
		chown = os.Lchown
	}
	for _, p := range paths {
		err := filepath.WalkDir(p, func(path string, _ fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			return chown(path, uid, gid)
		})
		if err != nil {
			return err
		}
	}
	return nil
}
