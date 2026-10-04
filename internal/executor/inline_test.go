package executor_test

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/executor"
)

// TestInline: the run's folders, and the folders it shares, go to the run user for the run and
// come back after; the run starts as that user with only its own environment (ADR-007).
func TestInline(t *testing.T) {
	nobody, err := user.Lookup("nobody")
	if err != nil {
		t.Skip("no nobody user here")
	}
	dir := t.TempDir()
	wt, rd, mirror := filepath.Join(dir, "wt"), filepath.Join(dir, "run"), filepath.Join(dir, "mirror")
	for _, d := range []string{wt, rd, filepath.Join(mirror, ".git")} {
		_ = os.MkdirAll(d, 0o755)
	}
	t.Setenv("YNF_FORGE_TOKEN", "must-not-leak")
	t.Setenv("HTTPS_PROXY", "http://proxy:3128")
	var chowned []string
	var credential string
	in := executor.Inline{
		User: "nobody",
		Chown: func(p string, uid, gid int) error {
			chowned = append(chowned, filepath.Base(p)+"="+itoa(uid))
			return nil
		},
		Credential: func(_ *exec.Cmd, uid, gid uint32) { credential = itoa(int(uid)) },
	}
	if in.Name() != "inline" || !in.Contained() {
		t.Fatal("inline is contained by the operator's declaration")
	}
	if w, r := in.Paths(executor.Job{Worktree: wt, RunDir: rd}); w != wt || r != rd {
		t.Fatal("paths are the host's")
	}
	out, err := in.Run(context.Background(), executor.Job{
		Argv: []string{"sh", "-c", "env; pwd"}, Worktree: wt, RunDir: rd, Share: []string{mirror},
		Env: map[string]string{"YNH_VENDOR": "claude"}, Secrets: map[string]string{"ANTHROPIC_API_KEY": "k"},
	})
	if err != nil || out.Exit != 0 {
		t.Fatalf("%v %+v", err, out)
	}
	env := string(out.Stdout)
	for _, want := range []string{"HOME=" + nobody.HomeDir, "USER=nobody", "HTTPS_PROXY=http://proxy:3128", "YNH_VENDOR=claude", "ANTHROPIC_API_KEY=k", "GOCACHE=" + filepath.Join(rd, "cache")} {
		if !strings.Contains(env, want) {
			t.Errorf("the run's environment lacks %s", want)
		}
	}
	if strings.Contains(env, "must-not-leak") {
		t.Fatal("ynf's own environment reached the run")
	}
	if credential != nobody.Uid {
		t.Fatalf("ran as %s, want nobody (%s)", credential, nobody.Uid)
	}
	handed := strings.Join(chowned, " ")
	for _, d := range []string{"wt=" + nobody.Uid, "run=" + nobody.Uid, "mirror=" + nobody.Uid, ".git=" + nobody.Uid, "wt=" + itoa(os.Getuid()), "mirror=" + itoa(os.Getuid())} {
		if !strings.Contains(handed, d) {
			t.Errorf("chown lacks %s: %s", d, handed)
		}
	}
}

func TestInlineRefuses(t *testing.T) {
	me, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	j := executor.Job{Argv: []string{"true"}, Worktree: dir, RunDir: dir}
	if _, err := (executor.Inline{User: me.Username}).Run(context.Background(), j); err == nil || !strings.Contains(err.Error(), "run ynf as another user") {
		t.Fatalf("the run as ynf's own user: %v", err)
	}
	if _, err := (executor.Inline{User: "no-such-user-ynf"}).Run(context.Background(), j); err == nil {
		t.Fatal("an unknown run user")
	}
	if _, err := (executor.Inline{}).Run(context.Background(), executor.Job{}); err == nil {
		t.Fatal("an empty command")
	}
	failing := executor.Inline{User: "nobody", Chown: func(string, int, int) error { return os.ErrPermission }}
	if _, err := user.Lookup("nobody"); err == nil {
		if _, err := failing.Run(context.Background(), j); err == nil || !strings.Contains(err.Error(), "hand the run's folders") {
			t.Fatalf("a folder that cannot be handed over: %v", err)
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
