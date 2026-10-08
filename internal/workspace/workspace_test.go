package workspace_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/workspace"
)

// remote makes a bare repository with one commit on main, standing in for GitHub.
func remote(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bare := filepath.Join(dir, "remote.git")
	src := filepath.Join(dir, "src")
	run(t, "", "git", "init", "-q", "--bare", "-b", "main", bare)
	run(t, "", "git", "init", "-q", "-b", "main", src)
	if err := os.WriteFile(filepath.Join(src, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, src, "git", "add", "-A")
	run(t, src, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "seed")
	run(t, src, "git", "push", "-q", bare, "main")
	return bare
}

func run(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	c := exec.Command(name, args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return string(out)
}

func TestMirrorCheckoutCommitPush(t *testing.T) {
	ctx := context.Background()
	bare := remote(t)
	w := workspace.Workspace{Root: t.TempDir(), Token: "secret", Author: workspace.Author{Name: "ynf", Email: "ynf@x"},
		RemoteURL: func(string) string { return bare }}

	mirror, err := w.Mirror(ctx, "o/r")
	if err != nil {
		t.Fatal(err)
	}
	if again, err := w.Mirror(ctx, "o/r"); err != nil || again != mirror {
		t.Fatalf("second mirror: %s %v", again, err)
	}
	wt, err := w.Checkout(ctx, mirror, "main", filepath.Join(t.TempDir(), "step", "wt"))
	if err != nil {
		t.Fatal(err)
	}
	if real, _ := filepath.EvalSymlinks(wt); real != wt {
		t.Fatalf("checkout path %s is not real (%s)", wt, real)
	}

	if changed, err := w.Changed(ctx, wt); err != nil || len(changed) != 0 {
		t.Fatalf("clean checkout changed: %v %v", changed, err)
	}
	if err := os.WriteFile(filepath.Join(wt, "a.go"), []byte("package a\n\nvar X = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "b.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := w.Changed(ctx, wt)
	if err != nil || strings.Join(changed, ",") != "a.go,b.go" {
		t.Fatalf("changed: %v %v", changed, err)
	}
	sha, err := w.Commit(ctx, wt, "fix things\n\nYNF-Item: item/x\nYNF-Step: 01ABC\n")
	if err != nil || len(sha) != 40 {
		t.Fatalf("commit: %q %v", sha, err)
	}
	if err := w.Push(ctx, wt, "o/r", "ynf/issue-1"); err != nil {
		t.Fatal(err)
	}
	msg := run(t, bare, "git", "log", "-1", "--format=%an <%ae>%n%B", "ynf/issue-1")
	if !strings.Contains(msg, "ynf <ynf@x>") || !strings.Contains(msg, "YNF-Step: 01ABC") {
		t.Fatalf("pushed commit: %s", msg)
	}

	if _, err := w.Mirror(ctx, "o/r"); err != nil {
		t.Fatal(err)
	}
	if !w.RemoteHas(ctx, mirror, "ynf/issue-1") || w.RemoteHas(ctx, mirror, "nope") {
		t.Fatal("RemoteHas")
	}
	if err := w.RemoveCheckout(wt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatal("checkout not removed")
	}
}

// TestCheckoutIsSelfContained: the checkout alone is a repository (a container mounts nothing
// else): .git is a folder, nothing points into the mirror, and the base is checked out with its
// remote-tracking ref.
func TestCheckoutIsSelfContained(t *testing.T) {
	ctx := context.Background()
	bare := remote(t)
	w := workspace.Workspace{Root: t.TempDir(), RemoteURL: func(string) string { return bare }}
	mirror, err := w.Mirror(ctx, "o/r")
	if err != nil {
		t.Fatal(err)
	}
	wt, err := w.Checkout(ctx, mirror, "main", filepath.Join(t.TempDir(), "wt"))
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(wt, ".git")); err != nil || !fi.IsDir() {
		t.Fatalf(".git should be a folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, ".git", "objects", "info", "alternates")); !os.IsNotExist(err) {
		t.Fatalf("objects should not be shared with the mirror: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, "a.go")); err != nil {
		t.Fatalf("base not checked out: %v", err)
	}
	head, _ := w.Head(ctx, wt)
	if tip := strings.TrimSpace(run(t, wt, "git", "rev-parse", "origin/main")); tip != head {
		t.Fatalf("origin/main %s, head %s", tip, head)
	}
	// Hide the mirror: the checkout must not need it.
	if err := os.Rename(mirror, mirror+".gone"); err != nil {
		t.Fatal(err)
	}
	if out := run(t, wt, "git", "status", "--porcelain"); out != "" {
		t.Fatalf("status without the mirror: %q", out)
	}
	if out := run(t, wt, "git", "diff", "origin/main", "--stat"); out != "" {
		t.Fatalf("diff without the mirror: %q", out)
	}
}

func TestErrors(t *testing.T) {
	ctx := context.Background()
	w := workspace.Workspace{Root: t.TempDir(), RemoteURL: func(string) string { return "/no/such/repo.git" }}
	if _, err := w.Mirror(ctx, "o/r"); err == nil || !strings.Contains(err.Error(), "git clone") {
		t.Fatalf("clone of a missing remote: %v", err)
	}
	bare := remote(t)
	w.RemoteURL = func(string) string { return bare }
	mirror, err := w.Mirror(ctx, "o/r")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Checkout(ctx, mirror, "no-such-branch", filepath.Join(t.TempDir(), "wt")); err == nil {
		t.Fatal("checkout at a missing ref")
	}
	wt, _ := w.Checkout(ctx, mirror, "main", filepath.Join(t.TempDir(), "wt"))
	if _, err := w.Commit(ctx, wt, "nothing staged"); err == nil {
		t.Fatal("empty commit accepted")
	}
	if (workspace.Workspace{}).RemoteURL != nil {
		t.Fatal("default remote should be GitHub")
	}
}

func TestFastForwardOnly(t *testing.T) {
	ctx := context.Background()
	bare := remote(t)
	w := workspace.Workspace{Root: t.TempDir(), RemoteURL: func(string) string { return bare }}
	mirror, _ := w.Mirror(ctx, "o/r")
	a, _ := w.Checkout(ctx, mirror, "main", filepath.Join(t.TempDir(), "a"))
	b, _ := w.Checkout(ctx, mirror, "main", filepath.Join(t.TempDir(), "b"))
	base, err := w.Head(ctx, a)
	if err != nil || len(base) != 40 {
		t.Fatalf("%q %v", base, err)
	}
	if tip, _ := w.RemoteSHA(ctx, mirror, "main"); tip != base {
		t.Fatalf("remote tip %s, head %s", tip, base)
	}
	for _, wt := range []string{a, b} {
		_ = os.WriteFile(filepath.Join(wt, "from_"+filepath.Base(wt)+".go"), []byte("package a\n"), 0o644)
		_, _ = w.Changed(ctx, wt)
		if _, err := w.Commit(ctx, wt, "from "+filepath.Base(wt)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.PushFastForward(ctx, a, "o/r", "main"); err != nil {
		t.Fatal(err)
	}
	if err := w.PushFastForward(ctx, b, "o/r", "main"); err == nil {
		t.Fatal("a push that would overwrite someone else's commit succeeded")
	}
	if _, err := w.RemoteSHA(ctx, mirror, "no-such-branch"); err == nil {
		t.Fatal("missing branch")
	}
}

// TestBaseOfAMerge: a fix's base is the first parent of its merge commit, whether the fix was
// squashed (one commit) or merged (two parents); the base checks out by commit, and the human
// patch and the agent's are both diffs against it.
func TestBaseOfAMerge(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	bare := filepath.Join(dir, "remote.git")
	src := filepath.Join(dir, "src")
	run(t, "", "git", "init", "-q", "--bare", "-b", "main", bare)
	run(t, "", "git", "init", "-q", "-b", "main", src)
	commit := func(msg string) string {
		run(t, src, "git", "add", "-A")
		run(t, src, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", msg)
		return strings.TrimSpace(run(t, src, "git", "rev-parse", "HEAD"))
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(src, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.go", "package a\n")
	base := commit("seed")
	write("a.go", "package a\n\nvar X = 1\n") // the squashed fix: one parent
	squash := commit("fix (#1)")
	run(t, src, "git", "checkout", "-q", "-b", "side", base)
	write("side.go", "package a\n")
	commit("side")
	run(t, src, "git", "checkout", "-q", "main")
	run(t, src, "git", "-c", "user.name=t", "-c", "user.email=t@t", "merge", "-q", "--no-ff", "-m", "Merge side", "side")
	merge := strings.TrimSpace(run(t, src, "git", "rev-parse", "HEAD"))
	run(t, src, "git", "push", "-q", bare, "main")

	w := workspace.Workspace{Root: t.TempDir(), RemoteURL: func(string) string { return bare }}
	mirror, err := w.Mirror(ctx, "o/r")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := w.FirstParent(ctx, mirror, squash); err != nil || got != base {
		t.Fatalf("squash base %s %v, want %s", got, err, base)
	}
	if got, err := w.FirstParent(ctx, mirror, merge); err != nil || got != squash {
		t.Fatalf("merge base %s %v, want %s", got, err, squash)
	}
	if _, err := w.FirstParent(ctx, mirror, strings.Repeat("0", 40)); err == nil {
		t.Fatal("an unknown commit has no parent")
	}
	human, err := w.RangeDiff(ctx, mirror, base, squash)
	if err != nil || !strings.Contains(human, "+var X = 1") {
		t.Fatalf("human patch: %q %v", human, err)
	}
	wt, err := w.Checkout(ctx, mirror, base, filepath.Join(t.TempDir(), "wt"))
	if err != nil {
		t.Fatal(err)
	}
	if head, _ := w.Head(ctx, wt); head != base {
		t.Fatalf("checked out %s, want the base %s", head, base)
	}
	if err := os.WriteFile(filepath.Join(wt, "new.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	agent, err := w.Diff(ctx, wt, base)
	if err != nil || !strings.Contains(agent, "b/new.go") {
		t.Fatalf("agent patch should include a new file: %q %v", agent, err)
	}
}
