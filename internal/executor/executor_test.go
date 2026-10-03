package executor_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eyelock/ynf/internal/executor"
)

func TestFor(t *testing.T) {
	for name, contained := range map[string]bool{"docker": true, "process": false} {
		e, err := executor.For(name)
		if err != nil || e.Name() != name || e.Contained() != contained {
			t.Errorf("%s: %v %v", name, e, err)
		}
	}
	for _, name := range []string{"ecs", "nope"} {
		if _, err := executor.For(name); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestDockerArgs(t *testing.T) {
	d := executor.Docker{Bin: "docker"}
	j := executor.Job{Argv: []string{"gofmt", "-w", "./x"}, Worktree: "/h/wt", RunDir: "/h/run", Image: "golang:1.26-alpine", Env: map[string]string{"A": "1"}}
	args, err := d.Args(j)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Join(args, " ")
	for _, want := range []string{"--network none", "-v /h/wt:/work", "-v /h/run:/run/ynf", "-w /work", "-e A=1", "-e GOCACHE=/run/ynf/cache/go-build", "golang:1.26-alpine gofmt -w ./x"} {
		if !strings.Contains(s, want) {
			t.Errorf("args lack %q: %s", want, s)
		}
	}
	if wt, rd := d.Paths(j); wt != executor.WorkDir || rd != executor.RunDir {
		t.Fatal("container paths")
	}
	if _, err := d.Args(executor.Job{Image: "x", Egress: []string{"proxy.golang.org"}}); !errors.Is(err, executor.ErrEgress) {
		t.Fatalf("egress allow list should be refused until the proxy exists: %v", err)
	}
	if _, err := d.Args(executor.Job{}); err == nil {
		t.Fatal("no image accepted")
	}
	if _, err := d.Run(context.Background(), executor.Job{}); err == nil {
		t.Fatal("run without image accepted")
	}
}

func TestProcess(t *testing.T) {
	wt, rd := t.TempDir(), t.TempDir()
	p := executor.Process{}
	if a, b := p.Paths(executor.Job{Worktree: wt, RunDir: rd}); a != wt || b != rd {
		t.Fatal("host paths")
	}
	out, err := p.Run(context.Background(), executor.Job{
		Argv: []string{"sh", "-c", `printf "$PWD|$XDG_CACHE_HOME|$X"; echo oops >&2; exit 3`}, Worktree: wt, RunDir: rd, Env: map[string]string{"X": "y"},
	})
	if err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(wt)
	got := strings.Split(string(out.Stdout), "|")
	if out.Exit != 3 || (got[0] != wt && got[0] != real) || got[1] != filepath.Join(rd, "cache") || got[2] != "y" || !strings.Contains(string(out.Stderr), "oops") {
		t.Fatalf("%+v %q", out, got)
	}
	if _, err := p.Run(context.Background(), executor.Job{}); err == nil {
		t.Fatal("empty argv accepted")
	}
	if _, err := p.Run(context.Background(), executor.Job{Argv: []string{"sleep", "5"}, Worktree: wt, RunDir: rd, Timeout: 50 * time.Millisecond}); err == nil {
		t.Fatal("timeout not reported")
	}
	if _, err := p.Run(context.Background(), executor.Job{Argv: []string{"/no/such/binary"}, Worktree: wt, RunDir: rd}); err == nil {
		t.Fatal("missing binary not reported")
	}
}

// TestDockerRun runs a real container when docker and the image are available locally.
func TestDockerRun(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil || os.Getenv("YNF_DOCKER_TESTS") == "" {
		t.Skip("set YNF_DOCKER_TESTS=1 with docker available")
	}
	wt, rd := t.TempDir(), t.TempDir()
	wt, _ = filepath.EvalSymlinks(wt)
	rd, _ = filepath.EvalSymlinks(rd)
	out, err := executor.Docker{Bin: "docker"}.Run(context.Background(), executor.Job{
		Argv:     []string{"sh", "-c", "echo hi > /work/f && (wget -q -T 2 http://example.com -O- >/dev/null 2>&1 && echo net || echo nonet)"},
		Worktree: wt, RunDir: rd, Image: "golang:1.26-alpine",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out.Stdout)) != "nonet" {
		t.Fatalf("container reached the network: %s", out.Stdout)
	}
	if b, _ := os.ReadFile(filepath.Join(wt, "f")); !slices.Equal(b, []byte("hi\n")) {
		t.Fatal("worktree not mounted")
	}
}
