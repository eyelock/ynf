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
// when it already is one, or ynf-linux-<arch> beside it (make build puts it there).
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
	p := filepath.Join(filepath.Dir(self), "ynf-linux-"+arch)
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}
