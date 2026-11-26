package policy_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/policy"
)

// pinRepo makes a bare repository with a branch main and a tag v1, and returns its file:// URL and
// the commit both name.
func pinRepo(t *testing.T) (url, commit string) {
	t.Helper()
	root := t.TempDir()
	src, bare := filepath.Join(root, "src"), filepath.Join(root, "h.git")
	sh := func(dir string, args ...string) string {
		c := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		c.Dir = dir
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	sh(root, "init", "-q", "-b", "main", src)
	sh(src, "commit", "-q", "--allow-empty", "-m", "h")
	sh(src, "tag", "v1")
	sh(src, "tag", "-a", "-m", "v2", "v2")
	commit = sh(src, "rev-parse", "HEAD")
	sh(root, "clone", "-q", "--bare", src, bare)
	return "file://" + bare, commit
}

// TestPinCheckRef: a full commit SHA is accepted. A tag, a short SHA and a branch are refused, and
// each refusal prints the full 40-character SHA the ref points to now, in a form to copy into the
// lane file (ynf #241). A repository that cannot be read is an error and not a pass.
func TestPinCheckRef(t *testing.T) {
	url, commit := pinRepo(t)
	ctx := context.Background()
	if err := (policy.Pin{Repo: url, Ref: commit}).CheckRef(ctx); err != nil {
		t.Errorf("a full SHA: %v", err)
	}
	for _, c := range []struct{ name, ref, what string }{
		{"a tag", "v1", "v1 is a tag"},
		{"an annotated tag", "v2", "v2 is a tag"},
		{"a branch", "main", "main is a branch"},
		{"a short SHA", commit[:7], commit[:7] + " is a short SHA"},
		{"an old commit's short SHA", pinOld(t, url), "is a short SHA"},
	} {
		err := policy.Pin{Repo: url, Ref: c.ref}.CheckRef(ctx)
		if err == nil || !strings.Contains(err.Error(), c.what) {
			t.Errorf("%s: %v, want %q", c.name, err, c.what)
			continue
		}
		want := commit
		if c.name == "an old commit's short SHA" {
			want = oldCommit
		}
		if !strings.Contains(err.Error(), "points to "+want+" now") || !strings.Contains(err.Error(), "write the pin as "+url+"@"+want) {
			t.Errorf("%s: the refusal should print the full SHA %s: %v", c.name, want, err)
		}
	}
	err := policy.Pin{Repo: url, Ref: "nope"}.CheckRef(ctx)
	if err == nil || !strings.Contains(err.Error(), "not a full commit SHA") {
		t.Errorf("an unknown ref: %v", err)
	}
	missing := "file://" + filepath.Join(t.TempDir(), "nowhere.git")
	err = policy.Pin{Repo: missing, Ref: "v1"}.CheckRef(ctx)
	if err == nil || !strings.Contains(err.Error(), "read its refs") {
		t.Errorf("an unreachable repository: %v", err)
	}
}

var oldCommit string

// pinOld adds an older commit to the repository behind url that no ref points at, as a short SHA
// usually names, and returns its short SHA; oldCommit is its full SHA.
func pinOld(t *testing.T, url string) string {
	t.Helper()
	bare := strings.TrimPrefix(url, "file://")
	sh := func(args ...string) string {
		c := exec.Command("git", append([]string{"-C", bare, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	tree := sh("rev-parse", "HEAD^{tree}")
	oldCommit = sh("commit-tree", "-m", "old", tree)
	// A newer commit on main, so the old one is no ref's tip.
	newer := sh("commit-tree", "-m", "newer", "-p", oldCommit, tree)
	sh("update-ref", "refs/heads/other", newer)
	return oldCommit[:8]
}
