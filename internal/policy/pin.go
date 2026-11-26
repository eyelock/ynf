package policy

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// Pin is a harness pinned from a repository: the repository and the tag or commit to take from it.
// A lane writes it as run.ynh.harness: <repository>@<full-sha>, such as
// github.com/org/harness@0123456789abcdef0123456789abcdef01234567. ynh takes the same two things
// as `ynh install <repository> --ref <full-sha>`. A tag or a short SHA parses, and CheckRef refuses it
// with the full SHA it points to.
type Pin struct {
	Repo string `json:"repo"`
	Ref  string `json:"ref"`
	// URL is the repository as the clone tools are given it, set only when it differs from Repo:
	// a bare host and path is cloned over https.
	URL string `json:"url,omitempty"`
}

func (p Pin) String() string { return p.Repo + "@" + p.Ref }

// CloneURL is the repository as the clone tools are given it. A URL with a scheme and an scp-like
// address are used as written; a bare host and path, such as github.com/org/repo, is
// https://github.com/org/repo, because the clone tools read the bare form as a folder.
func (p Pin) CloneURL() string {
	if strings.Contains(p.Repo, "://") || scpRepo.MatchString(p.Repo) {
		return p.Repo
	}
	return "https://" + p.Repo
}

// Resolved is CloneURL and the ref: the pin as it is used, which differs from String only for a
// bare host and path.
func (p Pin) Resolved() string { return p.CloneURL() + "@" + p.Ref }

var (
	// pinRef is a tag or a commit: no spaces, nothing a flag could be mistaken for.
	pinRef = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._/+-]*$`)
	// scpRepo is the scp-like form of a repository address, such as user@github.com:org/repo.
	scpRepo = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[^\s]+$`)
	// hostRepo is a host and a path, such as github.com/org/repo.
	hostRepo = regexp.MustCompile(`^[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+/[^\s]+$`)
	// fullSHA is a commit SHA written out in full; shortSHA is an abbreviation of one.
	fullSHA  = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
	shortSHA = regexp.MustCompile(`^[0-9a-fA-F]{4,39}$`)
)

// IsRepo reports whether s names a repository ynh can clone: a URL with a scheme, an scp-like
// address, or a host and a path. An installed harness's id, such as local/name@1.0, or a folder,
// is not one.
func IsRepo(s string) bool {
	return strings.Contains(s, "://") && !strings.ContainsAny(s, " \t\n") || scpRepo.MatchString(s) || hostRepo.MatchString(s)
}

// ParsePin reads a lane's harness value. ok is false when it is not a pin: a folder in the
// repository, or the id of an installed harness. A value shaped like a pin that cannot be used as
// one (no tag or commit, or one a flag could be mistaken for) is an error, so a typo is refused
// rather than run as an installed id.
func ParsePin(harness string) (p Pin, ok bool, err error) {
	i := strings.LastIndex(harness, "@")
	if i < 0 {
		return p, false, nil
	}
	repo, ref := harness[:i], harness[i+1:]
	if !IsRepo(repo) || strings.Contains(ref, ":") {
		return p, false, nil
	}
	if strings.HasPrefix(repo, "-") {
		return p, false, fmt.Errorf("harness %q: the repository %q looks like a flag", harness, repo)
	}
	if !pinRef.MatchString(ref) {
		return p, false, fmt.Errorf("harness %q: pin it to a tag or commit, <repository>@<tag-or-commit>", harness)
	}
	p = Pin{Repo: repo, Ref: ref}
	if u := p.CloneURL(); u != repo {
		p.URL = u
	}
	return p, true, nil
}

// IsFullSHA reports whether s is a full commit SHA: 40 hexadecimal digits. A tag, a branch and a
// short SHA are not, because each can name another commit tomorrow (ynh's trust gate takes only
// the full form as a pin, ynf #241).
func IsFullSHA(s string) bool { return fullSHA.MatchString(s) }

// CheckRef refuses a pin that is not a full commit SHA, and says which commit the ref it was given
// points to now, so the lane file can be corrected by copying the SHA from the refusal. A tag is
// resolved (an annotated tag to the commit it tags), a branch to the commit at its tip, a short SHA
// to the commit it abbreviates. It reads the pin repository's refs with git ls-remote against
// CloneURL, so it needs the repository; one it cannot reach is an error, not a pass. `ynf harness`,
// a run and `ynf lanes validate` all use this one check.
func (p Pin) CheckRef(ctx context.Context) error {
	refs, err := lsRemote(ctx, p.CloneURL(), "refs/heads/"+p.Ref, "refs/tags/"+p.Ref, "refs/tags/"+p.Ref+"^{}")
	if err != nil {
		return fmt.Errorf("harness %s: read its refs: %w", p, err)
	}
	if IsFullSHA(p.Ref) {
		return nil
	}
	var branch, tag, peeled string
	for _, r := range refs {
		switch r.name {
		case "refs/heads/" + p.Ref:
			branch = r.sha
		case "refs/tags/" + p.Ref:
			tag = r.sha
		case "refs/tags/" + p.Ref + "^{}":
			peeled = r.sha
		}
	}
	switch {
	case tag != "":
		if peeled != "" {
			tag = peeled
		}
		return p.notFullSHA("a tag, which can be moved to another commit", tag)
	case branch != "":
		return p.notFullSHA("a branch, which moves", branch)
	case shortSHA.MatchString(p.Ref):
		sha, err := p.resolveShort(ctx)
		if err != nil {
			return fmt.Errorf("harness %s: %s is a short SHA, which does not pin a commit, and ynf could not find the commit it abbreviates: %w; pin the full 40-character SHA", p, p.Ref, err)
		}
		return p.notFullSHA("a short SHA, which does not pin a commit", sha)
	}
	return fmt.Errorf("harness %s: %s is not a full commit SHA, and the repository has no tag or branch of that name; pin the full 40-character SHA of the commit to run", p, p.Ref)
}

// notFullSHA is the refusal of a ref that is not a full SHA: it names the 40-character SHA the ref
// resolved to, in the form a lane file takes.
func (p Pin) notFullSHA(what, sha string) error {
	return fmt.Errorf("harness %s: %s is %s; a lane pins its harness to a full 40-character commit SHA. %s points to %s now: write the pin as %s@%s",
		p, p.Ref, what, p.Ref, sha, p.Repo, sha)
}

type remoteRef struct{ sha, name string }

// lsRemote lists the refs of url that match patterns (all of them with none), as git ls-remote does.
func lsRemote(ctx context.Context, url string, patterns ...string) ([]remoteRef, error) {
	var out, errb bytes.Buffer
	c := exec.CommandContext(ctx, "git", append([]string{"ls-remote", "--", url}, patterns...)...)
	c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(errb.String()))
	}
	var refs []remoteRef
	for _, l := range strings.Split(out.String(), "\n") {
		if sha, name, ok := strings.Cut(strings.TrimSpace(l), "\t"); ok {
			refs = append(refs, remoteRef{sha: sha, name: name})
		}
	}
	return refs, nil
}

// resolveShort finds the one commit a short SHA abbreviates: among the tips of the repository's
// refs first, then, as a short SHA is usually an older commit, in a bare copy of its history
// fetched without file contents.
func (p Pin) resolveShort(ctx context.Context) (string, error) {
	refs, err := lsRemote(ctx, p.CloneURL())
	if err != nil {
		return "", err
	}
	found := map[string]bool{}
	for _, r := range refs {
		if strings.HasPrefix(strings.ToLower(r.sha), strings.ToLower(p.Ref)) {
			found[r.sha] = true
		}
	}
	if len(found) == 1 {
		for sha := range found {
			return sha, nil
		}
	}
	if len(found) > 1 {
		return "", fmt.Errorf("it is ambiguous: more than one ref tip starts with it")
	}
	dir, err := os.MkdirTemp("", "ynf-pin-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	git := func(args ...string) (string, error) {
		var out, errb bytes.Buffer
		c := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		c.Stdout, c.Stderr = &out, &errb
		if err := c.Run(); err != nil {
			return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(errb.String()))
		}
		return strings.TrimSpace(out.String()), nil
	}
	if _, err := git("init", "-q", "--bare"); err != nil {
		return "", err
	}
	if _, err := git("fetch", "-q", "--no-tags", "--filter=blob:none", p.CloneURL(), "+refs/heads/*:refs/remotes/o/*", "+refs/tags/*:refs/tags/*"); err != nil {
		return "", err
	}
	sha, err := git("rev-parse", "--verify", "--quiet", p.Ref+"^{commit}")
	if err != nil || !IsFullSHA(sha) {
		return "", fmt.Errorf("no commit of the repository starts with %s", p.Ref)
	}
	return sha, nil
}

// Pins returns the harness each lane pins, by lane name.
func (f *File) Pins() map[string]Pin {
	out := map[string]Pin{}
	for name, l := range f.Lanes {
		if l.Run.Ynh == nil {
			continue
		}
		if p, ok, _ := ParsePin(l.Run.Ynh.Harness); ok {
			out[name] = p
		}
	}
	return out
}

// checkHarnesses refuses a lane whose harness looks like a pin and is not a usable one.
func (f *File) checkHarnesses() error {
	for _, name := range f.Names() {
		if y := f.Lanes[name].Run.Ynh; y != nil {
			if _, _, err := ParsePin(y.Harness); err != nil {
				return fmt.Errorf("lane %s: run.ynh.%w", name, err)
			}
		}
	}
	return nil
}
