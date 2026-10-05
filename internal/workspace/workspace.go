// Package workspace is ynf's git: a mirror per repository, a checkout per step, and the commit and
// push that only ynf (never the agent) performs (ADR-007). The token reaches git through
// environment configuration, so it never appears in a process listing.
package workspace

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Author is the git identity of ynf's commits.
type Author struct{ Name, Email string }

// Workspace manages mirrors and checkouts under Root.
type Workspace struct {
	Root   string
	Token  string
	Author Author
	// RemoteURL maps owner/name to a clone URL. Default https://github.com/<repo>.git.
	RemoteURL func(repo string) string
}

// Mirror clones the repository, or fetches it if already cloned, and returns its real path.
func (w Workspace) Mirror(ctx context.Context, repo string) (string, error) {
	dir := filepath.Join(w.Root, "repos", filepath.FromSlash(repo))
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		if _, err := w.git(ctx, dir, "fetch", "-q", "--prune", "origin", "+refs/heads/*:refs/remotes/origin/*"); err != nil {
			return "", err
		}
		return filepath.EvalSymlinks(dir)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	if _, err := w.git(ctx, "", "clone", "-q", "--no-checkout", w.url(repo), dir); err != nil {
		return "", err
	}
	if _, err := w.git(ctx, dir, "fetch", "-q", "origin", "+refs/heads/*:refs/remotes/origin/*"); err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(dir)
}

// Checkout makes a self-contained clone of the mirror at origin/<ref>, detached, and returns its
// real path. Real, because a symlinked path makes git and linters disagree about which files
// changed (ADR-007).
//
// A clone rather than a git worktree: a worktree's .git is a file pointing into the mirror by
// absolute host path, so a container that mounts only the checkout has no repository. Here .git is
// a real folder inside dir, and the one mount is enough. --local hardlinks the mirror's objects
// (no copy on the same filesystem); --shared would not do, as its alternates file points back
// into the mirror. The clone keeps the mirror's remote-tracking branch as origin/<ref>, so
// `--new-from-merge-base=origin/main` works in the run.
func (w Workspace) Checkout(ctx context.Context, mirror, ref, dir string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(dir))
	if err != nil {
		return "", err
	}
	dir = filepath.Join(parent, filepath.Base(dir))
	if _, err := w.git(ctx, "", "clone", "-q", "--local", "--no-checkout", "--no-tags", mirror, dir); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	if isSHA(ref) {
		// A commit, as shadow mode checks out a fix's base: the clone has the mirror's objects.
		if _, err := w.git(ctx, dir, "checkout", "-q", "--detach", ref); err != nil {
			_ = os.RemoveAll(dir)
			return "", err
		}
		return dir, nil
	}
	// A clone copies the mirror's branches; the mirror keeps the forge's as remote-tracking refs.
	remote := "refs/remotes/origin/" + ref
	for _, args := range [][]string{
		{"fetch", "-q", "--no-tags", "origin", "+" + remote + ":" + remote},
		{"checkout", "-q", "--detach", "origin/" + ref},
	} {
		if _, err := w.git(ctx, dir, args...); err != nil {
			_ = os.RemoveAll(dir)
			return "", err
		}
	}
	return dir, nil
}

// isSHA reports whether ref is a full commit id rather than a branch name.
func isSHA(ref string) bool {
	if len(ref) != 40 {
		return false
	}
	for _, c := range ref {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// FirstParent is the commit before a merge: the first parent of sha in the mirror. For a squash
// merge it is the base branch as it was; for a merge commit, the same, with the branch merged in.
func (w Workspace) FirstParent(ctx context.Context, mirror, sha string) (string, error) {
	out, err := w.git(ctx, mirror, "rev-parse", "--verify", "-q", sha+"^1")
	if err != nil {
		return "", fmt.Errorf("%s has no parent in the mirror: %w", sha, err)
	}
	return strings.TrimSpace(out), nil
}

// RangeDiff is the change from one commit to another in the mirror: the human patch.
func (w Workspace) RangeDiff(ctx context.Context, mirror, from, to string) (string, error) {
	return w.git(ctx, mirror, "diff", "--no-color", "--no-ext-diff", from, to)
}

// Diff is everything the checkout differs from base by, new files included: the agent's patch.
// It stages the change, as Changed does.
func (w Workspace) Diff(ctx context.Context, wt, base string) (string, error) {
	if _, err := w.git(ctx, wt, "add", "-A"); err != nil {
		return "", err
	}
	return w.git(ctx, wt, "diff", "--cached", "--no-color", "--no-ext-diff", base)
}

// RemoveCheckout deletes a checkout. It is a whole repository, so there is no registration to undo.
func (w Workspace) RemoveCheckout(dir string) error {
	return os.RemoveAll(dir)
}

// RemoteHas reports whether origin has the branch (after the last fetch).
func (w Workspace) RemoteHas(ctx context.Context, mirror, branch string) bool {
	_, err := w.git(ctx, mirror, "rev-parse", "-q", "--verify", "refs/remotes/origin/"+branch)
	return err == nil
}

// Changed stages everything and lists the changed paths.
func (w Workspace) Changed(ctx context.Context, wt string) ([]string, error) {
	if _, err := w.git(ctx, wt, "add", "-A"); err != nil {
		return nil, err
	}
	out, err := w.git(ctx, wt, "diff", "--cached", "--name-only", "--no-renames")
	if err != nil {
		return nil, err
	}
	return lines(out), nil
}

// Commit commits the staged change with message and returns the commit SHA.
func (w Workspace) Commit(ctx context.Context, wt, message string) (string, error) {
	name, email := w.Author.Name, w.Author.Email
	if name == "" {
		name, email = "ynf", "ynf@users.noreply.github.com"
	}
	_, err := w.gitIn(ctx, wt, strings.NewReader(message),
		"-c", "user.name="+name, "-c", "user.email="+email, "-c", "commit.gpgsign=false",
		"commit", "-q", "-F", "-")
	if err != nil {
		return "", err
	}
	out, err := w.git(ctx, wt, "rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

// Head returns the checkout's HEAD commit.
func (w Workspace) Head(ctx context.Context, wt string) (string, error) {
	out, err := w.git(ctx, wt, "rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

// RemoteSHA returns origin's branch tip as of the last fetch.
func (w Workspace) RemoteSHA(ctx context.Context, mirror, branch string) (string, error) {
	out, err := w.git(ctx, mirror, "rev-parse", "refs/remotes/origin/"+branch)
	return strings.TrimSpace(out), err
}

// PushFastForward pushes HEAD to branch only if that is a fast-forward: the push to someone else's
// branch, which must never overwrite their work (ADR-007).
func (w Workspace) PushFastForward(ctx context.Context, wt, repo, branch string) error {
	_, err := w.git(ctx, wt, "push", "-q", w.url(repo), "HEAD:refs/heads/"+branch)
	return err
}

// Push force-pushes HEAD to branch. ynf only force-pushes branches it originated; adopted
// branches are never force-pushed (ADR-007).
func (w Workspace) Push(ctx context.Context, wt, repo, branch string) error {
	_, err := w.git(ctx, wt, "push", "-q", "--force", w.url(repo), "HEAD:refs/heads/"+branch)
	return err
}

func (w Workspace) url(repo string) string {
	if w.RemoteURL != nil {
		return w.RemoteURL(repo)
	}
	return "https://github.com/" + repo + ".git"
}

func (w Workspace) git(ctx context.Context, dir string, args ...string) (string, error) {
	return w.gitIn(ctx, dir, nil, args...)
}

func (w Workspace) gitIn(ctx context.Context, dir string, stdin *strings.Reader, args ...string) (string, error) {
	c := exec.CommandContext(ctx, "git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if w.Token != "" {
		auth := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + w.Token))
		c.Env = append(c.Env,
			"GIT_CONFIG_COUNT=2",
			"GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=",
			"GIT_CONFIG_KEY_1=http.https://github.com/.extraheader", "GIT_CONFIG_VALUE_1=AUTHORIZATION: basic "+auth,
		)
	}
	if stdin != nil {
		c.Stdin = stdin
	}
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		return stdout.String(), fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func lines(s string) []string {
	var out []string
	for l := range strings.SplitSeq(strings.TrimSpace(s), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}
