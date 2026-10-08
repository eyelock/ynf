package policy

import (
	"fmt"
	"regexp"
	"strings"
)

// Pin is a harness pinned from a repository: the repository and the tag or commit to take from it.
// A lane writes it as run.ynh.harness: <repository>@<tag-or-commit>, such as
// github.com/org/harness@v1.2.0 or file:///srv/harness.git@3f9c2e1. ynh takes the same two things
// as `ynh install <repository> --ref <tag-or-commit>`.
type Pin struct {
	Repo string `json:"repo"`
	Ref  string `json:"ref"`
}

func (p Pin) String() string { return p.Repo + "@" + p.Ref }

var (
	// pinRef is a tag or a commit: no spaces, nothing a flag could be mistaken for.
	pinRef = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._/+-]*$`)
	// scpRepo is the scp-like form of a repository address, such as user@github.com:org/repo.
	scpRepo = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[^\s]+$`)
	// hostRepo is a host and a path, such as github.com/org/repo.
	hostRepo = regexp.MustCompile(`^[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+/[^\s]+$`)
)

// isRepo reports whether s names a repository ynh can clone: a URL with a scheme, an scp-like
// address, or a host and a path. An installed harness's id, such as local/name@1.0, or a folder,
// is not one.
func isRepo(s string) bool {
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
	if !isRepo(repo) || strings.Contains(ref, ":") {
		return p, false, nil
	}
	if strings.HasPrefix(repo, "-") {
		return p, false, fmt.Errorf("harness %q: the repository %q looks like a flag", harness, repo)
	}
	if !pinRef.MatchString(ref) {
		return p, false, fmt.Errorf("harness %q: pin it to a tag or commit, <repository>@<tag-or-commit>", harness)
	}
	return Pin{Repo: repo, Ref: ref}, true, nil
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
