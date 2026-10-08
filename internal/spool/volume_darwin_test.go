package spool

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func hdiutilInfo(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("hdiutil", "info").Output()
	if err != nil {
		t.Fatalf("hdiutil info: %v", err)
	}
	return string(out)
}

func imageAttached(t *testing.T, image string) bool {
	t.Helper()
	want := resolved(image)
	for _, a := range parseHdiutilInfo(hdiutilInfo(t)) {
		if resolved(a.image) == want {
			return true
		}
	}
	return false
}

// TestAKilledYnfsDiskImageIsDetachedByTheNextJob is the macOS path for real: a run's volume is
// attached the way ynf does it, its ynf is made dead, and a new job's start-up captures its files
// and detaches it, leaving alone a disk image that is not ynf's own, attached at the same time.
func TestAKilledYnfsDiskImageIsDetachedByTheNextJob(t *testing.T) {
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
	image := s.Volumes.Backing(r.Dir)
	t.Cleanup(func() { // whatever the test fails on, nothing of it stays attached
		if imageAttached(t, image) {
			_ = exec.Command("hdiutil", "detach", "-force", r.Dir).Run()
		}
		removeBacking(image)
	})
	if !imageAttached(t, image) {
		t.Fatalf("the run's image %s is not attached", image)
	}
	if err := os.WriteFile(filepath.Join(r.Dir, "a.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// An image that is not ynf's, attached under the same root, as the machine has others.
	decoyDir := filepath.Join(root, "decoy")
	if err := os.Mkdir(decoyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	decoyImg := filepath.Join(root, "decoy-image")
	if out, err := exec.Command("hdiutil", "create", "-quiet", "-size", "2m", "-type", "SPARSE", "-fs", "HFS+", "-volname", "not-ynf", decoyImg).CombinedOutput(); err != nil {
		t.Fatalf("hdiutil create: %v: %s", err, out)
	}
	if out, err := exec.Command("hdiutil", "attach", "-quiet", "-mountpoint", decoyDir, "-nobrowse", "-noverify", "-noautoopen", decoyImg+".sparseimage").CombinedOutput(); err != nil {
		t.Fatalf("hdiutil attach: %v: %s", err, out)
	}
	t.Cleanup(func() {
		if imageAttached(t, decoyImg+".sparseimage") {
			_ = exec.Command("hdiutil", "detach", "-force", decoyDir).Run()
		}
	})

	// The ynf that made the run is dead: its lease names a PID that is not running.
	host, _ := os.Hostname()
	b, _ := json.Marshal(owner{Host: host, PID: deadPID, Start: "t0", Backing: image})
	if err := os.WriteFile(s.ownerPath("r1"), b, 0o600); err != nil {
		t.Fatal(err)
	}

	// The next job: a new process's spool, with no memory of the run, and the real process table.
	next, err := New(s.Root, 4<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	next.Volumes = HostVolumes()
	capture := filepath.Join(root, "capture")
	next.CleanStale(capture)

	if imageAttached(t, image) {
		t.Errorf("the dead ynf's image is still attached")
	}
	if got, err := os.ReadFile(filepath.Join(capture, "runs", "r1", "a.jsonl")); err != nil || string(got) != "{}\n" {
		t.Errorf("the run's file was not captured: %q, %v", got, err)
	}
	if _, err := os.Stat(next.RunDir("r1")); !os.IsNotExist(err) {
		t.Errorf("the run's folder is left: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(image)); !os.IsNotExist(err) {
		t.Errorf("the image's folder is left: %v", err)
	}
	if !imageAttached(t, decoyImg+".sparseimage") {
		t.Errorf("an image that is not ynf's was detached")
	}
	if strings.Contains(hdiutilInfo(t), image) {
		t.Errorf("hdiutil still lists the image")
	}
}
