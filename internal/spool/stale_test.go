package spool

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Backing and Attached stand in for the host's way of finding a volume again: a volume is
// attached from Mount until its unmount, and survives the ynf that made it, as a real one does.
func (f *fakeVolumes) Backing(dir string) string { return "fake-image:" + dir }

func (f *fakeVolumes) Attached(dir, backing string) (func() error, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.attachErr != nil {
		return nil, f.attachErr
	}
	if backing != "fake-image:"+dir || !slices.Contains(f.mounted, dir) || slices.Contains(f.unmounted, dir) {
		return nil, nil // not a volume ynf made, or not there any more
	}
	return func() error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.detached = append(f.detached, dir)
		if f.failUn != nil {
			return f.failUn
		}
		f.unmounted = append(f.unmounted, dir)
		ents, _ := os.ReadDir(dir) // what was in the volume goes with it
		for _, e := range ents {
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
		return nil
	}, nil
}

const deadPID, livePID = 4000001, 4000002

// leftBy makes a run's volume as a ynf with the given PID would leave it: the run began with a
// file in its volume, and the ynf is gone. It returns a second spool on the same root, the next
// job's, which finds a given PID alive or not as the test says.
func leftBy(t *testing.T, s *Spool, fv *fakeVolumes, id string, pid int) *Spool {
	t.Helper()
	s.Volumes = fv
	r, err := s.Begin(manifest(id), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.Dir, "a.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	host, _ := os.Hostname()
	b, _ := json.Marshal(owner{Host: host, PID: pid, Start: "t0", Backing: fv.Backing(r.Dir)})
	if err := os.WriteFile(s.ownerPath(id), b, 0o600); err != nil {
		t.Fatal(err)
	}
	next, _ := newSpool(t)
	next.Root = s.Root
	next.Log = s.Log
	next.Volumes = fv
	next.Alive = func(p int, start string) bool { return p == livePID && start == "t0" }
	return next
}

// TestAStaleRunVolumeIsCleanedAtStart: a volume whose ynf is gone has its spool files captured,
// then it is unmounted and its folder and lease removed; and the job logs it once.
func TestAStaleRunVolumeIsCleanedAtStart(t *testing.T) {
	s, logs := newSpool(t)
	fv := &fakeVolumes{}
	next := leftBy(t, s, fv, "r1", deadPID)
	leftBy(t, s, fv, "r2", deadPID)
	capture := filepath.Join(t.TempDir(), "capture")
	next.CleanStale(capture)
	for _, id := range []string{"r1", "r2"} {
		if b, err := os.ReadFile(filepath.Join(capture, "runs", id, "a.jsonl")); err != nil || string(b) != "{}\n" {
			t.Errorf("%s: the stale run's file was not captured: %v", id, err)
		}
		if _, err := os.Stat(next.RunDir(id)); !os.IsNotExist(err) {
			t.Errorf("%s: the folder is left: %v", id, err)
		}
		if _, err := os.Stat(next.ownerPath(id)); !os.IsNotExist(err) {
			t.Errorf("%s: the lease is left: %v", id, err)
		}
	}
	if len(fv.detached) != 2 {
		t.Errorf("detached %v, want both", fv.detached)
	}
	if n := strings.Count(logs.String(), "were cleaned up"); n != 1 || !strings.Contains(logs.String(), "volumes=2") {
		t.Errorf("logged %d times, want once for both:\n%s", n, logs)
	}
	next.CleanStale(capture) // nothing is left to do
	if n := strings.Count(logs.String(), "were cleaned up"); n != 1 {
		t.Errorf("a second start logged again:\n%s", logs)
	}
}

// TestACaptureOverTheLimitStaysAndTheVolumeStillGoes: the capture limits hold at start-up as they do
// at a run's end: a file past the limit is not copied, and the volume is still taken away.
func TestACaptureOverTheLimitStaysAndTheVolumeStillGoes(t *testing.T) {
	s, logs := newSpool(t)
	fv := &fakeVolumes{}
	next := leftBy(t, s, fv, "r1", deadPID)
	if err := os.WriteFile(filepath.Join(next.RunDir("r1"), "big.jsonl"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(t.TempDir(), "capture")
	job := &budget{left: 50}
	n, err := next.cleanOne("r1", owner{Backing: fv.Backing(next.RunDir("r1"))}, capture, job)
	if err != nil || n != 1 || job.left != 47 {
		t.Errorf("kept %d files with %d of the job's limit left, %v", n, job.left, err)
	}
	if _, err := os.Stat(filepath.Join(capture, "runs", "r1", "big.jsonl")); !os.IsNotExist(err) {
		t.Errorf("a file over the limit was captured: %v", err)
	}
	if len(fv.detached) != 1 || !strings.Contains(logs.String(), "over the capture limit") {
		t.Errorf("detached %v:\n%s", fv.detached, logs)
	}
}

// TestALiveRunsVolumeIsLeftAlone: a lease whose process is running, this process's own runs, and
// a lease from another host are never touched, however the volume looks.
func TestALiveRunsVolumeIsLeftAlone(t *testing.T) {
	s, logs := newSpool(t)
	fv := &fakeVolumes{}
	next := leftBy(t, s, fv, "other-ynf", livePID) // another ynf on the same host, running
	leftBy(t, s, fv, "mine", deadPID)              // a lease that looks dead, but this process holds it
	leftBy(t, s, fv, "elsewhere", deadPID)
	host, _ := os.Hostname()
	b, _ := json.Marshal(owner{Host: host + "-2", PID: deadPID, Start: "t0", Backing: fv.Backing(next.RunDir("elsewhere"))})
	if err := os.WriteFile(next.ownerPath("elsewhere"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	next.active["mine"] = &Run{ID: "mine"}
	// A start time that differs is another process on a reused PID: the original is gone.
	next.Alive = func(p int, start string) bool { return p == livePID && start == "t0" }
	capture := filepath.Join(t.TempDir(), "capture")
	next.CleanStale(capture)
	if len(fv.detached) != 0 {
		t.Errorf("detached %v: a live run's volume was touched", fv.detached)
	}
	for _, id := range []string{"other-ynf", "mine", "elsewhere"} {
		if _, err := os.Stat(filepath.Join(next.RunDir(id), "a.jsonl")); err != nil {
			t.Errorf("%s: its file is gone: %v", id, err)
		}
		if _, err := os.Stat(next.ownerPath(id)); err != nil {
			t.Errorf("%s: its lease is gone: %v", id, err)
		}
	}
	if _, err := os.Stat(capture); !os.IsNotExist(err) {
		t.Errorf("something was captured from a live run: %v", err)
	}
	if strings.Contains(logs.String(), "cleaned up") {
		t.Errorf("logged a clean-up:\n%s", logs)
	}
}

// TestAVolumeNotMadeByYnfIsNeverTouched: a run folder with no lease, a lease whose volume is not
// one ynf made (another backing, or none attached), and a name ynr would not read are left, and
// the host is never asked to detach.
func TestAVolumeNotMadeByYnfIsNeverTouched(t *testing.T) {
	s, _ := newSpool(t)
	fv := &fakeVolumes{}
	next := leftBy(t, s, fv, "r-lease-only", deadPID)
	host, _ := os.Hostname()
	// A folder some other process mounted at a run's path, whose lease names an image of another's.
	other := next.RunDir("r-foreign")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "keep.txt"), []byte("not ours"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(owner{Host: host, PID: deadPID, Start: "t0", Backing: "/Volumes/some-other.dmg"})
	if err := os.WriteFile(next.ownerPath("r-foreign"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"no-lease", ".hidden"} {
		if err := os.Mkdir(next.RunDir(d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(next.Root, "manifests", "garbled.owner"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(next.RunDir("garbled"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Remove the one volume that is ynf's, so what is left is all someone else's.
	fv.unmounted = append(fv.unmounted, next.RunDir("r-lease-only"))
	capture := filepath.Join(t.TempDir(), "capture")
	next.CleanStale(capture)
	if len(fv.detached) != 0 {
		t.Errorf("detached %v: something that was not ynf's own volume", fv.detached)
	}
	for _, d := range []string{"no-lease", ".hidden", "garbled", "r-foreign"} {
		if _, err := os.Stat(next.RunDir(d)); err != nil {
			t.Errorf("%s: left alone, but gone: %v", d, err)
		}
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("the foreign folder was removed: %v", err)
	}
}

// TestACleanUpFailureIsLoggedAndTheJobCarriesOn: a volume that will not detach, one the host
// cannot be asked about, and a good one after them: each failure is logged on its own, the lease
// of a failed one stays for the next job, and the good one is cleaned.
func TestACleanUpFailureIsLoggedAndTheJobCarriesOn(t *testing.T) {
	s, logs := newSpool(t)
	fv := &fakeVolumes{}
	next := leftBy(t, s, fv, "r1", deadPID)
	capture := filepath.Join(t.TempDir(), "capture")

	fv.failUn = errors.New("resource busy")
	next.CleanStale(capture)
	if !strings.Contains(logs.String(), "could not be cleaned up") || !strings.Contains(logs.String(), "resource busy") || strings.Contains(logs.String(), "were cleaned up") {
		t.Errorf("the failure is not logged:\n%s", logs)
	}
	if _, err := os.Stat(next.ownerPath("r1")); err != nil {
		t.Errorf("the lease is gone, so no later job retries: %v", err)
	}
	if _, err := os.Stat(next.RunDir("r1")); err != nil {
		t.Errorf("the folder is gone: %v", err)
	}

	fv.failUn = nil
	fv.attachErr = errors.New("hdiutil info: not available")
	next.CleanStale(capture)
	if !strings.Contains(logs.String(), "not available") {
		t.Errorf("an attach failure is not logged:\n%s", logs)
	}

	fv.attachErr = nil
	next.CleanStale(capture)
	if _, err := os.Stat(next.RunDir("r1")); !os.IsNotExist(err) {
		t.Errorf("the retry did not clean it: %v", err)
	}
	if !strings.Contains(logs.String(), "were cleaned up") {
		t.Errorf("the clean-up is not logged:\n%s", logs)
	}
}

// TestACleanedRunHasNoLeaseOnceItsVolumeIsGone: the lease is written when a run's volume is made,
// and removed with the volume at the run's end; a run without a volume has none.
func TestACleanedRunHasNoLeaseOnceItsVolumeIsGone(t *testing.T) {
	s, _ := newSpool(t)
	fv := &fakeVolumes{}
	s.Volumes = fv
	r, err := s.Begin(manifest("r1"), false)
	if err != nil {
		t.Fatal(err)
	}
	o, ok := s.readOwner("r1")
	host, _ := os.Hostname()
	if !ok || o.PID != os.Getpid() || o.Host != host || o.Backing != fv.Backing(r.Dir) {
		t.Fatalf("the lease is %+v, %v", o, ok)
	}
	if !processAlive(o.PID, o.Start) {
		t.Errorf("this process is not alive by its own lease")
	}
	r.End(filepath.Join(t.TempDir(), "spool"))
	if _, ok := s.readOwner("r1"); ok {
		t.Errorf("the lease outlived the volume")
	}
	s.Volumes = nil
	if _, err := s.Begin(manifest("r2"), false); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.readOwner("r2"); ok {
		t.Errorf("a run with no volume has a lease")
	}
}

func TestProcessAliveTellsAReusedPIDApart(t *testing.T) {
	start, err := procStart(os.Getpid())
	if err != nil || start == "" {
		t.Fatalf("own start time: %q, %v", start, err)
	}
	for name, tc := range map[string]struct {
		pid   int
		start string
		want  bool
	}{
		"this process":        {os.Getpid(), start, true},
		"a reused pid":        {os.Getpid(), start + "x", false},
		"no start recorded":   {os.Getpid(), "", true},
		"a pid with no owner": {deadPID, "t0", false},
		"no pid":              {0, "", false},
	} {
		if got := processAlive(tc.pid, tc.start); got != tc.want {
			t.Errorf("%s: alive %v, want %v", name, got, tc.want)
		}
	}
	if _, err := procStart(deadPID); !errors.Is(err, errNoProcess) {
		t.Errorf("a pid that is not running: %v", err)
	}
}

func TestParseMountinfoFindsOnlyYnfsTmpfs(t *testing.T) {
	info := `22 28 0:21 / /proc rw,nosuid - proc proc rw
401 380 0:55 / /spool/runs/r1 rw,nosuid,nodev,noexec - tmpfs ynf-run rw,size=65536k
402 380 0:56 / /spool/runs/my\040run rw - tmpfs ynf-run rw
403 380 0:57 / /spool/runs/other rw - tmpfs tmpfs rw
404 380 0:58 / /spool/runs/disk rw - ext4 ynf-run rw
garbage line
`
	got := parseMountinfo(info, "ynf-run")
	want := []string{"/spool/runs/r1", "/spool/runs/my run"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestParseHdiutilInfoListsImagesAndTheirDevices(t *testing.T) {
	out := `framework       : 704
images          : 2
================================================
image-path      : /System/other.dmg
image-type      : <unknown>
/dev/disk4	GUID_partition_scheme
/dev/disk4s1	Apple_APFS	/Volumes/x
================================================
image-path      : /private/var/folders/ab/T/ynf-run-volume-1/run.sparseimage
image-type      : sparse
/dev/disk5	Apple_partition_scheme
/dev/disk5s2	Apple_HFS	/spool/runs/r1
`
	got := parseHdiutilInfo(out)
	want := []attachedImage{{"/System/other.dmg", "/dev/disk4"}, {"/private/var/folders/ab/T/ynf-run-volume-1/run.sparseimage", "/dev/disk5"}}
	if !slices.Equal(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestRemoveBackingRemovesOnlyAFolderYnfMade(t *testing.T) {
	tmp := t.TempDir()
	mine := filepath.Join(tmp, backingPrefix+"123")
	theirs := filepath.Join(tmp, "documents")
	for _, d := range []string{mine, theirs} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	removeBacking(filepath.Join(theirs, "run.sparseimage"))
	removeBacking(filepath.Join(mine, "run.dmg"))
	removeBacking("")
	if _, err := os.Stat(theirs); err != nil {
		t.Errorf("a folder that is not ynf's was removed: %v", err)
	}
	if _, err := os.Stat(mine); err != nil {
		t.Errorf("a folder with something other than a run image was removed: %v", err)
	}
	removeBacking(filepath.Join(mine, "run.sparseimage"))
	if _, err := os.Stat(mine); !os.IsNotExist(err) {
		t.Errorf("ynf's own image folder is left: %v", err)
	}
}
