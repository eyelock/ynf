package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/eyelock/ynf/internal/executor"
)

func TestLinuxBinary(t *testing.T) {
	t.Setenv("YNF_LINUX_BINARY", "/opt/ynf-linux")
	if got := linuxBinary(); got != "/opt/ynf-linux" {
		t.Fatalf("env: %s", got)
	}
	t.Setenv("YNF_LINUX_BINARY", "")
	got := linuxBinary()
	self, _ := os.Executable()
	switch {
	case runtime.GOOS == "linux" && got == self:
	case got == "" || filepath.Dir(got) == filepath.Dir(self):
	default:
		t.Fatalf("unexpected %s", got)
	}
}

func TestExecutorGivesDockerItsProxy(t *testing.T) {
	t.Setenv("YNF_LINUX_BINARY", "/opt/ynf-linux")
	linuxBin = ""
	linuxOnce = sync.Once{}
	a := &app{}
	e, err := a.executor("docker")
	if err != nil {
		t.Fatal(err)
	}
	if d := e.(executor.Docker); d.ProxyBinary != "/opt/ynf-linux" {
		t.Fatalf("%+v", d)
	}
	if e, err := a.executor("process"); err != nil || e.Name() != "process" {
		t.Fatalf("%v %v", e, err)
	}
	if _, err := a.executor("nope"); err == nil {
		t.Fatal("unknown executor accepted")
	}
}
