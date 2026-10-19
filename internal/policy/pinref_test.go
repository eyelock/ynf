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
	commit = sh(src, "rev-parse", "HEAD")
	sh(root, "clone", "-q", "--bare", src, bare)
	return "file://" + bare, commit
}

// TestPinCheckRef: a tag and a full commit are accepted, a branch is refused with the message a
// run gives, and a repository that cannot be read is an error and not a pass.
func TestPinCheckRef(t *testing.T) {
	url, commit := pinRepo(t)
	ctx := context.Background()
	for _, ref := range []string{"v1", commit} {
		if err := (policy.Pin{Repo: url, Ref: ref}).CheckRef(ctx); err != nil {
			t.Errorf("%s: %v", ref, err)
		}
	}
	err := policy.Pin{Repo: url, Ref: "main"}.CheckRef(ctx)
	if err == nil || !strings.Contains(err.Error(), "main is a branch, which moves; pin a tag or a commit") {
		t.Errorf("a branch: %v", err)
	}
	missing := "file://" + filepath.Join(t.TempDir(), "nowhere.git")
	err = policy.Pin{Repo: missing, Ref: "v1"}.CheckRef(ctx)
	if err == nil || !strings.Contains(err.Error(), "read its refs") {
		t.Errorf("an unreachable repository: %v", err)
	}
}
