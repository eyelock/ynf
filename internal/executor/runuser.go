package executor

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strconv"
	"strings"
)

// RunUser is implemented by an executor whose run can write as a user other than ynf's own, so the
// run's spool manifest can name that user (ynr ADR-003). The answer comes from the image's own
// configuration or the executor's own setting, which the run cannot reach or change.
type RunUser interface {
	// RunUID returns the user the run writes as and whether it is another than ynf's own, the
	// owner of the run's spool folder. An error means it could not be found out.
	RunUID(ctx context.Context, j Job) (uid uint32, other bool, err error)
}

// SpoolVolumes is implemented by an executor that may not be able to use a run's spool folder
// when it is a volume of its own, in which case the folder stays a plain one.
type SpoolVolumes interface {
	SpoolVolume() bool
}

// SpoolVolume implements SpoolVolumes. Docker Desktop for Mac cannot bind-mount a host folder
// that is a mount of its own: the daemon fails with "error while creating mount source path ...
// file exists", so a run there keeps a plain folder and the quota watcher. A docker daemon on
// Linux binds one like any folder.
func (Docker) SpoolVolume() bool { return runtime.GOOS != "darwin" }

// RunUID implements RunUser: a run that keeps its image's own user writes as the user the image
// names, found in the image's configuration, and in the image's /etc/passwd when it names the user
// rather than numbering it. Any other docker run is ynf's own user (--user).
func (d Docker) RunUID(ctx context.Context, j Job) (uint32, bool, error) {
	if !j.ImageUser {
		return 0, false, nil
	}
	spec, err := d.imageUser(ctx, j.Image)
	if err != nil {
		return 0, false, err
	}
	uid, err := ResolveUser(spec, func(name string) (uint32, error) { return d.passwdUID(ctx, j.Image, name) })
	if err != nil {
		return 0, false, fmt.Errorf("the user of image %s: %w", j.Image, err)
	}
	return uid, uid != uint32(os.Getuid()), nil
}

// imageUser is the image's configured user, as docker writes it: empty for root, or a name or
// number, with an optional :group. An image not yet on the host is pulled first, as running it
// would.
func (d Docker) imageUser(ctx context.Context, image string) (string, error) {
	out, err := d.stdout(ctx, "image", "inspect", "--format", "{{.Config.User}}", image)
	if err != nil {
		if _, perr := d.docker(ctx, "pull", "--quiet", image); perr != nil {
			return "", err
		}
		if out, err = d.stdout(ctx, "image", "inspect", "--format", "{{.Config.User}}", image); err != nil {
			return "", err
		}
	}
	return strings.TrimSpace(out), nil
}

// stdout runs docker and returns what it wrote to standard output alone: its warnings are on
// standard error, and an empty answer (an image with no user) must stay empty.
func (d Docker) stdout(ctx context.Context, args ...string) (string, error) {
	c := exec.CommandContext(ctx, d.Bin, args...)
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("docker %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// passwdUID looks name up in the image's /etc/passwd, copied out of a container that is created
// and never started, so nothing in the image runs.
func (d Docker) passwdUID(ctx context.Context, image, name string) (uint32, error) {
	id, err := d.stdout(ctx, "create", image)
	if err != nil {
		return 0, err
	}
	id = strings.TrimSpace(id)
	defer func() { _, _ = d.docker(context.WithoutCancel(ctx), "rm", "-f", id) }()
	c := exec.CommandContext(ctx, d.Bin, "cp", id+":/etc/passwd", "-")
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		return 0, fmt.Errorf("read /etc/passwd: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	tr := tar.NewReader(&stdout)
	if _, err := tr.Next(); err != nil {
		return 0, fmt.Errorf("read /etc/passwd: %w", err)
	}
	return passwdLookup(io.LimitReader(tr, 1<<20), name)
}

// passwdLookup is the uid of name in a passwd file.
func passwdLookup(r io.Reader, name string) (uint32, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Split(sc.Text(), ":")
		if len(f) >= 3 && f[0] == name {
			return parseUID(f[2])
		}
	}
	return 0, fmt.Errorf("no user %q in /etc/passwd", name)
}

// ResolveUser is the uid an image's user setting names: empty is root, a number is itself, and a
// name is looked up. A group after a colon is not needed.
func ResolveUser(spec string, lookup func(name string) (uint32, error)) (uint32, error) {
	u, _, _ := strings.Cut(spec, ":")
	switch {
	case u == "":
		return 0, nil
	case u[0] >= '0' && u[0] <= '9':
		return parseUID(u)
	}
	return lookup(u)
}

func parseUID(s string) (uint32, error) {
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("%q is not a user id", s)
	}
	return uint32(n), nil
}

// RunUID implements RunUser: the run user, which is not ynf's own (Run refuses it when it is).
func (in Inline) RunUID(_ context.Context, _ Job) (uint32, bool, error) {
	name := in.User
	if name == "" {
		name = "ynh"
	}
	u, err := user.Lookup(name)
	if err != nil {
		return 0, false, fmt.Errorf("the run user %q: %w", name, err)
	}
	uid, err := parseUID(u.Uid)
	if err != nil {
		return 0, false, errors.New("the run user has no numeric id")
	}
	return uid, uid != uint32(os.Getuid()), nil
}
