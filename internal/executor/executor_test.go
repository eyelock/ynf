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
	"github.com/eyelock/ynf/internal/workspace"
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
	args, err := d.Args(j, "ynf-abc", "none")
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Join(args, " ")
	for _, want := range []string{"--name ynf-abc", "--network none", "--cap-drop ALL", "--security-opt no-new-privileges", "--pids-limit 1024",
		"-v /h/wt:/work", "-v /h/run:/run/ynf", "-w /work", "-e A=1", "-e GOCACHE=/run/ynf/cache/go-build", "golang:1.26-alpine gofmt -w ./x"} {
		if !strings.Contains(s, want) {
			t.Errorf("args lack %q: %s", want, s)
		}
	}
	if strings.Contains(s, "HTTPS_PROXY") {
		t.Error("no-network run should not be told about a proxy")
	}
	args, _ = d.Args(j, "ynf-abc", "ynf-abc-net")
	if s := strings.Join(args, " "); !strings.Contains(s, "--network ynf-abc-net") || !strings.Contains(s, "-e HTTPS_PROXY=http://egress:3128") {
		t.Errorf("egress run args: %s", s)
	}
	if wt, rd := d.Paths(j); wt != executor.WorkDir || rd != executor.RunDir {
		t.Fatal("container paths")
	}
	if _, err := d.Args(executor.Job{}, "n", "none"); err == nil {
		t.Fatal("no image accepted")
	}
	if _, err := d.Run(context.Background(), executor.Job{}); err == nil {
		t.Fatal("run without image accepted")
	}
}

func TestDockerProxyArgs(t *testing.T) {
	d := executor.Docker{Bin: "docker", ProxyBinary: "/bin/ynf-linux"}
	s := strings.Join(d.ProxyArgs(executor.Job{RunDir: "/h/run", Egress: []string{"a.example", "*.b.example"}}, "ynf-x-egress"), " ")
	for _, want := range []string{"--name ynf-x-egress", "--network bridge", "--read-only", "--cap-drop ALL", "-v /bin/ynf-linux:/ynf:ro", "alpine:3.20 /ynf egress-proxy", "--allow a.example,*.b.example", "--log /run/ynf/egress.jsonl"} {
		if !strings.Contains(s, want) {
			t.Errorf("proxy args lack %q: %s", want, s)
		}
	}
}

func TestEgressNeedsTheProxyBinary(t *testing.T) {
	j := executor.Job{Image: "x", Egress: []string{"proxy.golang.org"}}
	if _, err := (executor.Docker{Bin: "docker"}).Run(context.Background(), j); !errors.Is(err, executor.ErrEgress) {
		t.Fatalf("no proxy binary: %v", err)
	}
	if _, err := (executor.Docker{Bin: "docker", ProxyBinary: "/no/such/ynf"}).Run(context.Background(), j); !errors.Is(err, executor.ErrEgress) {
		t.Fatalf("missing proxy binary: %v", err)
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

func dockerTests(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil || os.Getenv("YNF_DOCKER_TESTS") == "" {
		t.Skip("set YNF_DOCKER_TESTS=1 with docker available")
	}
}

func dirs(t *testing.T) (string, string) {
	t.Helper()
	wt, _ := filepath.EvalSymlinks(t.TempDir())
	rd, _ := filepath.EvalSymlinks(t.TempDir())
	return wt, rd
}

// TestDockerRun runs a real container: the worktree is mounted and there is no network.
func TestDockerRun(t *testing.T) {
	dockerTests(t)
	wt, rd := dirs(t)
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

// TestDockerEgress runs a real container behind the egress proxy: an allowed host is reached
// through it, a denied host is refused and logged, and nothing gets out any other way.
func TestDockerEgress(t *testing.T) {
	dockerTests(t)
	arch, err := exec.Command("docker", "version", "--format", "{{.Server.Arch}}").Output()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "ynf-linux")
	build := exec.Command("go", "build", "-o", bin, "../../cmd/ynf")
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+strings.TrimSpace(string(arch)), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build linux ynf: %v\n%s", err, out)
	}
	wt, rd := dirs(t)
	// Go's module client honours HTTPS_PROXY the way real tools do (busybox wget does not).
	script := `
export GOSUMDB=off
go mod download golang.org/x/text@v0.20.0 >/dev/null 2>&1 && echo allowed-ok || echo allowed-failed
GOPROXY=https://www.iana.org go mod download golang.org/x/mod@v0.20.0 >/dev/null 2>&1 && echo denied-reached || echo denied-blocked
unset HTTPS_PROXY https_proxy HTTP_PROXY http_proxy
GOPROXY=https://proxy.golang.org go mod download golang.org/x/sync@v0.10.0 >/dev/null 2>&1 && echo bypass-reached || echo bypass-blocked`
	out, err := executor.Docker{Bin: "docker", ProxyBinary: bin}.Run(context.Background(), executor.Job{
		Argv: []string{"sh", "-c", script}, Worktree: wt, RunDir: rd, Image: "golang:1.26-alpine",
		Egress: []string{"proxy.golang.org"}, Timeout: 2 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Fields(string(out.Stdout))
	if !slices.Equal(got, []string{"allowed-ok", "denied-blocked", "bypass-blocked"}) {
		t.Fatalf("got %v\nstderr: %s", got, out.Stderr)
	}
	if !slices.Equal(out.Denied, []string{"www.iana.org"}) {
		t.Fatalf("denied %v", out.Denied)
	}
}

// fakeDocker writes a docker stand-in that records every call and plays the part of the proxy
// (printing "listening") and the job (writing a denial to the egress log, or sleeping).
func fakeDocker(t *testing.T) (bin, calls string) {
	t.Helper()
	dir := t.TempDir()
	calls = filepath.Join(dir, "calls")
	bin = filepath.Join(dir, "docker")
	script := `#!/bin/sh
echo "$*" >> "` + calls + `"
case "$1" in
  logs) echo "ynf egress proxy listening on :3128" ;;
  run)
    case "$*" in
      *" -d "*) echo container-id ;;
      *sleepy*) sleep 5 ;;
      *)
        for a in "$@"; do case "$a" in *:/run/ynf) rd="${a%%:/run/ynf}" ;; esac; done
        [ -n "$rd" ] && echo '{"host":"www.iana.org","allowed":false}' >> "$rd/egress.jsonl"
        echo job-ran; exit 3 ;;
    esac ;;
esac
exit 0
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, calls
}

func TestDockerEgressFlowWithFakeDocker(t *testing.T) {
	bin, calls := fakeDocker(t)
	proxy := filepath.Join(t.TempDir(), "ynf-linux")
	_ = os.WriteFile(proxy, []byte("x"), 0o755)
	wt, rd := dirs(t)
	out, err := executor.Docker{Bin: bin, ProxyBinary: proxy}.Run(context.Background(), executor.Job{
		Argv: []string{"go", "build"}, Worktree: wt, RunDir: rd, Image: "img", Egress: []string{"proxy.golang.org"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Exit != 3 || strings.TrimSpace(string(out.Stdout)) != "job-ran" || !slices.Equal(out.Denied, []string{"www.iana.org"}) {
		t.Fatalf("%+v", out)
	}
	b, _ := os.ReadFile(calls)
	log := string(b)
	for _, want := range []string{"network create --internal ynf-", "run -d --rm --name ynf-", "network connect --alias egress ynf-", "logs ynf-", "rm -f ynf-", "network rm ynf-"} {
		if !strings.Contains(log, want) {
			t.Errorf("docker was not asked to %q:\n%s", want, log)
		}
	}
}

func TestDockerNoEgressAndCancel(t *testing.T) {
	bin, calls := fakeDocker(t)
	wt, rd := dirs(t)
	out, err := executor.Docker{Bin: bin}.Run(context.Background(), executor.Job{Argv: []string{"true"}, Worktree: wt, RunDir: rd, Image: "img"})
	if err != nil || out.Exit != 3 {
		t.Fatalf("%+v %v", out, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := (executor.Docker{Bin: bin}).Run(ctx, executor.Job{Argv: []string{"sleepy"}, Worktree: wt, RunDir: rd, Image: "img"}); err == nil {
		t.Fatal("cancelled run reported success")
	}
	b, _ := os.ReadFile(calls)
	if !strings.Contains(string(b), "rm -f ynf-") {
		t.Fatalf("a cancelled run's container was not removed:\n%s", b)
	}
}

func TestDockerProxyFailures(t *testing.T) {
	dir := t.TempDir()
	failing := filepath.Join(dir, "docker")
	_ = os.WriteFile(failing, []byte("#!/bin/sh\necho nope >&2\nexit 1\n"), 0o755)
	proxy := filepath.Join(dir, "ynf-linux")
	_ = os.WriteFile(proxy, []byte("x"), 0o755)
	wt, rd := dirs(t)
	j := executor.Job{Argv: []string{"x"}, Worktree: wt, RunDir: rd, Image: "img", Egress: []string{"a.example"}}
	if _, err := (executor.Docker{Bin: failing, ProxyBinary: proxy}).Run(context.Background(), j); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("network create failure: %v", err)
	}
	quiet := filepath.Join(dir, "quiet-docker")
	_ = os.WriteFile(quiet, []byte("#!/bin/sh\nexit 0\n"), 0o755)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := (executor.Docker{Bin: quiet, ProxyBinary: proxy}).Run(ctx, j); err == nil {
		t.Fatal("a proxy that never says it is listening was accepted")
	}
}

func TestDockerImageUser(t *testing.T) {
	args, _ := executor.Docker{Bin: "docker"}.Args(executor.Job{Image: "agent", Worktree: "/w", RunDir: "/r", ImageUser: true}, "n", "none")
	s := strings.Join(args, " ")
	if strings.Contains(s, "--user") || strings.Contains(s, "-e HOME=") || !strings.Contains(s, "-e GOCACHE=") {
		t.Fatalf("an agent image keeps its own user and home, but still gets fresh caches: %s", s)
	}
}

// gitImage returns a local image with git, or skips: the test must not pull. The test overrides
// the image's entrypoint with a shell, as an agent image's is its own runner.
func gitImage(t *testing.T) string {
	t.Helper()
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker is not available")
	}
	for _, image := range []string{"alpine/git", "ynf-sandbox-agent:latest"} {
		if exec.Command("docker", "image", "inspect", image).Run() != nil {
			continue
		}
		if exec.Command("docker", "run", "--rm", "--entrypoint", "sh", image, "-c", "git --version").Run() == nil {
			return image
		}
	}
	t.Skip("no local image with git")
	return ""
}

// TestDockerGitInsideTheCheckout: with only the checkout mounted, as a run has it, git sees a whole
// repository (#59), though the files belong to a user the container does not know.
func TestDockerGitInsideTheCheckout(t *testing.T) {
	image := gitImage(t)
	root := t.TempDir()
	src, bare := filepath.Join(root, "src"), filepath.Join(root, "remote.git")
	host := func(dir string, args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	host("", "init", "-q", "--bare", "-b", "main", bare)
	host("", "init", "-q", "-b", "main", src)
	if err := os.WriteFile(filepath.Join(src, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	host(src, "add", "-A")
	host(src, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "seed")
	host(src, "push", "-q", bare, "main")

	ctx := context.Background()
	w := workspace.Workspace{Root: filepath.Join(root, "ws"), RemoteURL: func(string) string { return bare }}
	mirror, err := w.Mirror(ctx, "o/r")
	if err != nil {
		t.Fatal(err)
	}
	wt, err := w.Checkout(ctx, mirror, "main", filepath.Join(root, "step", "wt"))
	if err != nil {
		t.Fatal(err)
	}
	rd, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.WriteFile(filepath.Join(wt, "b.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The executor's own command line (mounts, user, environment), with a shell as the entrypoint.
	args, err := executor.Docker{Bin: "docker"}.Args(executor.Job{
		Argv:     []string{"-c", "git -C /work status --porcelain && git -C /work rev-parse origin/main"},
		Worktree: wt, RunDir: rd, Image: image,
	}, "ynf-test-"+filepath.Base(root), "none")
	if err != nil {
		t.Fatal(err)
	}
	args = slices.Insert(args, slices.Index(args, image), "--entrypoint", "sh")
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git in the container: %v\n%s", err, out)
	}
	head, _ := w.Head(ctx, wt)
	if got := string(out); !strings.Contains(got, "?? b.go") || !strings.Contains(got, head) {
		t.Fatalf("git in the container saw:\n%s", got)
	}
}
