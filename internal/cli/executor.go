package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/eyelock/ynf/internal/executor"
)

var (
	linuxOnce sync.Once
	linuxBin  string
)

// executor returns the named executor; docker gets a linux ynf to run as its egress proxy.
func (a *app) executor(name string) (executor.Executor, error) {
	// An instance inside containment the operator provides runs everything inline (ADR-007).
	if a.cfg != nil && a.cfg.Executor == "inline" {
		return executor.Inline{User: a.cfg.InlineUser}, nil
	}
	e, err := executor.For(name)
	if err != nil {
		return nil, err
	}
	if d, ok := e.(executor.Docker); ok {
		linuxOnce.Do(func() { linuxBin = linuxBinary() })
		d.ProxyBinary = linuxBin
		return d, nil
	}
	return e, nil
}

// linuxBinary finds a static linux ynf for docker's architecture: YNF_LINUX_BINARY, this binary
// when it already is one, or ynf-linux-<arch> beside it or in ../libexec (where the Homebrew
// formula installs it, off PATH).
func linuxBinary() string {
	if p := os.Getenv("YNF_LINUX_BINARY"); p != "" {
		return p
	}
	arch := runtime.GOARCH
	if out, err := exec.Command("docker", "version", "--format", "{{.Server.Arch}}").Output(); err == nil {
		arch = strings.TrimSpace(string(out))
	}
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	if runtime.GOOS == "linux" && runtime.GOARCH == arch {
		return self
	}
	return findBeside(self, "ynf-linux-"+arch)
}

// findBeside looks for name next to exe, then in ../libexec, following exe's symlink first (a
// Homebrew bin entry is a link into the Cellar).
func findBeside(exe, name string) string {
	dirs := []string{filepath.Dir(exe)}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		dirs = append(dirs, filepath.Dir(real), filepath.Join(filepath.Dir(real), "..", "libexec"))
	}
	for _, d := range dirs {
		p := filepath.Join(d, name)
		if _, err := os.Stat(p); err == nil {
			return filepath.Clean(p)
		}
	}
	return ""
}
