package executor_test

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/executor"
)

func TestResolveUser(t *testing.T) {
	lookup := func(name string) (uint32, error) {
		if name == "ynh" {
			return 1000, nil
		}
		return 0, errors.New("no user " + name)
	}
	for spec, want := range map[string]uint32{"": 0, "1000": 1000, "1000:1000": 1000, "10001:0": 10001, "ynh": 1000, "ynh:staff": 1000} {
		got, err := executor.ResolveUser(spec, lookup)
		if err != nil || got != want {
			t.Errorf("%q = %d, %v; want %d", spec, got, err, want)
		}
	}
	for _, bad := range []string{"nobody", "99999999999", "1x"} {
		if _, err := executor.ResolveUser(bad, lookup); err == nil {
			t.Errorf("%q was resolved", bad)
		}
	}
}

// imageDocker answers the three things the user lookup asks: the image's configured user, a
// container created and never started, and its /etc/passwd as a tar stream.
func imageDocker(t *testing.T, user, passwd string) executor.Docker {
	t.Helper()
	dir := t.TempDir()
	var tarball bytes.Buffer
	tw := tar.NewWriter(&tarball)
	_ = tw.WriteHeader(&tar.Header{Name: "passwd", Mode: 0o644, Size: int64(len(passwd))})
	_, _ = tw.Write([]byte(passwd))
	_ = tw.Close()
	tarFile := filepath.Join(dir, "passwd.tar")
	if err := os.WriteFile(tarFile, tarball.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "calls")
	script := `#!/bin/sh
echo "$@" >> ` + log + `
case "$1" in
image) echo "WARNING: something" >&2; echo "` + user + `" ;;
create) echo "cid123" ;;
cp) cat ` + tarFile + ` ;;
esac
`
	bin := filepath.Join(dir, "docker")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return executor.Docker{Bin: bin}
}

func calls(t *testing.T, d executor.Docker) string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(filepath.Dir(d.Bin), "calls"))
	return string(b)
}

// TestADockerRunsUserComesFromItsImage: an image user named in the image's own /etc/passwd, a
// number in its configuration, or none (root); never a docker run that keeps ynf's own user.
func TestADockerRunsUserComesFromItsImage(t *testing.T) {
	passwd := "root:x:0:0:root:/root:/bin/sh\nynh:x:10042:10042::/home/ynh:/bin/sh\n"
	me := uint32(os.Getuid())
	tests := []struct {
		name  string
		user  string
		job   executor.Job
		uid   uint32
		other bool
		ran   bool // the image was asked
	}{
		{"a name, through the image's passwd", "ynh", executor.Job{Image: "agent", ImageUser: true}, 10042, true, true},
		{"a name with a group", "ynh:ynh", executor.Job{Image: "agent", ImageUser: true}, 10042, true, true},
		{"a number needs no lookup", "10001:10001", executor.Job{Image: "agent", ImageUser: true}, 10001, true, true},
		{"no user is root", "", executor.Job{Image: "agent", ImageUser: true}, 0, me != 0, true},
		{"the run keeps ynf's own user", "ynh", executor.Job{Image: "agent"}, 0, false, false},
		{"an image user who is ynf's own", itoa(int(me)), executor.Job{Image: "agent", ImageUser: true}, me, false, true},
	}
	for _, tc := range tests {
		d := imageDocker(t, tc.user, passwd)
		uid, other, err := d.RunUID(context.Background(), tc.job)
		if err != nil || (tc.ran && (uid != tc.uid || other != tc.other)) || (!tc.ran && other) {
			t.Errorf("%s: %d, %v, %v; want %d, %v", tc.name, uid, other, err, tc.uid, tc.other)
		}
		if got := calls(t, d); (got != "") != tc.ran {
			t.Errorf("%s: docker was called %q", tc.name, got)
		}
		if strings.Contains(calls(t, d), "run ") || strings.Contains(calls(t, d), "start") {
			t.Errorf("%s: the image was run: %s", tc.name, calls(t, d))
		}
	}
}

func TestADockerRunsUserThatCannotBeFoundOutIsAnError(t *testing.T) {
	// A name the image's passwd does not hold.
	d := imageDocker(t, "ghost", "root:x:0:0::/root:/bin/sh\n")
	if _, _, err := d.RunUID(context.Background(), executor.Job{Image: "agent", ImageUser: true}); err == nil || !strings.Contains(err.Error(), `no user "ghost"`) {
		t.Errorf("an unknown user: %v", err)
	}
	// An image docker does not know, and cannot pull.
	failing := filepath.Join(t.TempDir(), "docker")
	_ = os.WriteFile(failing, []byte("#!/bin/sh\necho no such image >&2\nexit 1\n"), 0o755)
	if _, _, err := (executor.Docker{Bin: failing}).RunUID(context.Background(), executor.Job{Image: "agent", ImageUser: true}); err == nil {
		t.Error("an image that is not there")
	}
}

func TestAnInlineRunsUserIsTheRunUser(t *testing.T) {
	uid, other, err := (executor.Inline{User: "nobody"}).RunUID(context.Background(), executor.Job{})
	if err != nil {
		t.Skip("no nobody user here")
	}
	if !other && os.Getuid() != int(uid) {
		t.Errorf("%d is not ynf's own (%d), and was said to be", uid, os.Getuid())
	}
	if _, _, err := (executor.Inline{User: "no-such-user-ynf"}).RunUID(context.Background(), executor.Job{}); err == nil {
		t.Error("an unknown run user")
	}
}
