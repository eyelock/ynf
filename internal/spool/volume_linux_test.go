package spool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func tmpfsAt(t *testing.T, dir string) bool {
	t.Helper()
	info, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(dir)
	for _, mp := range parseMountinfo(string(info), tmpfsSource) {
		if mp == real {
			return true
		}
	}
	return false
}

// TestAKilledYnfsTmpfsIsUnmountedByTheNextJob is the Linux path for real, where ynf may mount: a
// run's tmpfs is made, its ynf is made dead, and a new job's start-up captures its files and
// unmounts it, leaving alone a tmpfs that is not ynf's, mounted under runs/ at the same time.
// Without CAP_SYS_ADMIN it skips, as CI's unprivileged runners do.
func TestAKilledYnfsTmpfsIsUnmountedByTheNextJob(t *testing.T) {
	root := t.TempDir()
	s, err := New(filepath.Join(root, "spool"), 4<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Volumes = HostVolumes()
	r, err := s.Begin(manifest("r1"), false)
	if err != nil {
		t.Fatal(err)
	}
	if r.vol == nil {
		t.Skip("this host did not give the run a volume")
	}
	t.Cleanup(func() { _ = syscall.Unmount(r.Dir, syscall.MNT_DETACH) })
	if !tmpfsAt(t, r.Dir) {
		t.Fatalf("%s is not a tmpfs of ynf's", r.Dir)
	}
	if err := os.WriteFile(filepath.Join(r.Dir, "a.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A tmpfs under runs/ that is not ynf's: another source.
	other := s.RunDir("r-not-ynf")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mount("somebody-else", other, "tmpfs", 0, "size=1m"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Unmount(other, syscall.MNT_DETACH) })
	host, _ := os.Hostname()
	b, _ := json.Marshal(owner{Host: host, PID: deadPID, Start: "t0"})
	for _, id := range []string{"r1", "r-not-ynf"} {
		if err := os.WriteFile(s.ownerPath(id), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	next, err := New(s.Root, 4<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	next.Volumes = HostVolumes()
	capture := filepath.Join(root, "capture")
	next.CleanStale(capture)

	if tmpfsAt(t, r.Dir) {
		t.Errorf("the dead ynf's tmpfs is still mounted")
	}
	if got, err := os.ReadFile(filepath.Join(capture, "runs", "r1", "a.jsonl")); err != nil || string(got) != "{}\n" {
		t.Errorf("the run's file was not captured: %q, %v", got, err)
	}
	if _, err := os.Stat(r.Dir); !os.IsNotExist(err) {
		t.Errorf("the run's folder is left: %v", err)
	}
	info, _ := os.ReadFile("/proc/self/mountinfo")
	if !strings.Contains(string(info), other) {
		t.Errorf("a tmpfs that is not ynf's was unmounted")
	}
}
