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

// TestFindBesideFollowsHomebrewLayout: Homebrew links bin/ynf into the Cellar and the formula puts
// the linux proxies in the Cellar's libexec.
func TestFindBesideFollowsHomebrewLayout(t *testing.T) {
	root := t.TempDir()
	cellar := filepath.Join(root, "Cellar", "ynf", "0.1.0")
	_ = os.MkdirAll(filepath.Join(cellar, "bin"), 0o755)
	_ = os.MkdirAll(filepath.Join(cellar, "libexec"), 0o755)
	_ = os.WriteFile(filepath.Join(cellar, "bin", "ynf"), []byte("x"), 0o755)
	_ = os.WriteFile(filepath.Join(cellar, "libexec", "ynf-linux-arm64"), []byte("x"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "bin"), 0o755)
	link := filepath.Join(root, "bin", "ynf")
	if err := os.Symlink(filepath.Join(cellar, "bin", "ynf"), link); err != nil {
		t.Fatal(err)
	}
	got := findBeside(link, "ynf-linux-arm64")
	want, _ := filepath.EvalSymlinks(filepath.Join(cellar, "libexec", "ynf-linux-arm64"))
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	_ = os.WriteFile(filepath.Join(root, "bin", "ynf-linux-amd64"), []byte("x"), 0o755)
	if got := findBeside(link, "ynf-linux-amd64"); got != filepath.Join(root, "bin", "ynf-linux-amd64") {
		t.Fatalf("beside the link first: %q", got)
	}
	if findBeside(link, "nope") != "" {
		t.Fatal("missing binary found")
	}
}
