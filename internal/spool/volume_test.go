package spool

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeVolumes stands in for the host: it makes no filesystem, only records what was mounted and
// unmounted, so the lifecycle is tested without privilege.
type fakeVolumes struct {
	mu        sync.Mutex
	err       error
	mounted   []string
	unmounted []string
	failUn    error
	size      int64
	entries   int
}

func (f *fakeVolumes) Name() string { return "fake" }

func (f *fakeVolumes) Mount(dir string, size int64, entries int) (func() error, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	f.mounted = append(f.mounted, dir)
	f.size, f.entries = size, entries
	return func() error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.unmounted = append(f.unmounted, dir)
		if f.failUn != nil {
			return f.failUn
		}
		// What was in the volume goes with it, leaving the empty folder under the mount.
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
		return nil
	}, nil
}

func (f *fakeVolumes) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.mounted), len(f.unmounted)
}

func manifest(id string) Manifest { return Manifest{Run: id, Lane: "github.com/o/r#l"} }

// TestAManifestNamesTheRunUserOnlyWhenItIsNotTheFolderOwner: uid is in the manifest ynr reads when
// the run writes as another user, and absent when it writes as the folder's owner.
func TestAManifestNamesTheRunUserOnlyWhenItIsNotTheFolderOwner(t *testing.T) {
	s, _ := newSpool(t)
	uid := uint32(10001)
	for name, tc := range map[string]struct {
		uid  *uint32
		want string
	}{"another user": {&uid, `"uid":10001`}, "the owner": {nil, ""}} {
		m := manifest("r-" + strings.ReplaceAll(name, " ", "-"))
		m.UID = tc.uid
		if err := s.WriteManifest(m); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(s.Root, "manifests", m.Run+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if tc.want == "" && strings.Contains(string(b), "uid") {
			t.Errorf("%s: the manifest names a user: %s", name, b)
		}
		if tc.want != "" && !strings.Contains(string(b), tc.want) {
			t.Errorf("%s: %s lacks %s", name, b, tc.want)
		}
		var back Manifest
		if err := json.Unmarshal(b, &back); err != nil || (tc.uid == nil) != (back.UID == nil) || (tc.uid != nil && *back.UID != *tc.uid) {
			t.Errorf("%s: read back %+v, %v", name, back, err)
		}
	}
	// A run as root is a user to name: zero is not "none".
	zero := uint32(0)
	m := manifest("root")
	m.UID = &zero
	if err := s.WriteManifest(m); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(s.Root, "manifests", "root.json")); !strings.Contains(string(b), `"uid":0`) {
		t.Errorf("root was left out: %s", b)
	}
}

// TestARunFolderIsAVolumeOfItsOwnWhereTheHostAllows: the folder is mounted before the manifest and
// the run, sized by the quota, with no watcher; at the run's end its files are captured, then the
// volume and the folder go.
func TestARunFolderIsAVolumeOfItsOwnWhereTheHostAllows(t *testing.T) {
	s, logs := newSpool(t)
	fv := &fakeVolumes{}
	s.Volumes = fv
	r, err := s.Begin(manifest("r1"), true)
	if err != nil {
		t.Fatal(err)
	}
	if m, u := fv.counts(); m != 1 || u != 0 || fv.mounted[0] != r.Dir || fv.size != s.Quota || fv.entries != maxEntries {
		t.Fatalf("mounted %v, unmounted %d, size %d entries %d", fv.mounted, u, fv.size, fv.entries)
	}
	if fi, _ := os.Stat(r.Dir); fi.Mode().Perm() != 0o777 {
		t.Errorf("an image user's folder is %v", fi.Mode().Perm())
	}
	// No watcher: a file over the quota is the volume's to stop, not ynf's to remove.
	if err := os.WriteFile(filepath.Join(r.Dir, "big.bin"), make([]byte, 4<<10), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.Dir, "a.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(r.Dir, "big.bin")); err != nil {
		t.Errorf("the watcher ran on a volume: %v", err)
	}
	capture := filepath.Join(t.TempDir(), "spool")
	r.End(capture)
	if _, u := fv.counts(); u != 1 {
		t.Errorf("unmounted %d times, want 1", u)
	}
	if b, err := os.ReadFile(filepath.Join(capture, "a.jsonl")); err != nil || string(b) != "{}\n" {
		t.Errorf("the run's file was not captured before the volume went: %v", err)
	}
	if _, err := os.Stat(r.Dir); !os.IsNotExist(err) {
		t.Errorf("the run's folder is left: %v", err)
	}
	if !strings.Contains(logs.String(), "volume of its own, with a hard size limit") {
		t.Errorf("the path taken is not logged:\n%s", logs)
	}
	r.End(capture) // a second end is harmless
	if _, u := fv.counts(); u != 1 {
		t.Errorf("unmounted %d times after a second end", u)
	}
}

// TestWithoutAVolumeTheWatcherStaysTheBound: a host that does not allow a volume, or no volume
// implementation at all, leaves the run as it was, and says so once, not on every run.
func TestWithoutAVolumeTheWatcherStaysTheBound(t *testing.T) {
	for name, tc := range map[string]struct {
		volumes Volumes
		note    string
	}{
		"not allowed":    {&fakeVolumes{err: fmt.Errorf("%w: needs CAP_SYS_ADMIN", ErrNoVolume)}, "cannot be a volume of its own here"},
		"a fault":        {&fakeVolumes{err: errors.New("disk image: no space")}, "could not be made a volume"},
		"no implementer": {nil, ""},
	} {
		s, logs := newSpool(t)
		s.Volumes = tc.volumes
		for _, id := range []string{"r1", "r2"} {
			r, err := s.Begin(manifest(id), false)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if err := os.WriteFile(filepath.Join(r.Dir, "flood.bin"), make([]byte, 5<<10), 0o644); err != nil {
				t.Fatal(err)
			}
			time.Sleep(100 * time.Millisecond)
			r.End(filepath.Join(t.TempDir(), "spool"))
			if _, err := os.Stat(filepath.Join(r.Dir, "flood.bin")); !os.IsNotExist(err) {
				t.Errorf("%s: the watcher did not trim: %v", name, err)
			}
			if _, err := os.Stat(r.Dir); err != nil {
				t.Errorf("%s: a run's folder with no volume stays for ynr: %v", name, err)
			}
		}
		if n := strings.Count(logs.String(), "volume of its own"); tc.note != "" && (n != 1 || !strings.Contains(logs.String(), tc.note)) {
			t.Errorf("%s: the fallback was logged %d times, want once saying %q:\n%s", name, n, tc.note, logs)
		}
		if tc.note == "" && strings.Contains(logs.String(), "volume of its own") {
			t.Errorf("%s: logged a volume with none to make:\n%s", name, logs)
		}
	}
}

// TestAFailedBeginLeavesNoMount: a manifest that cannot be written takes the volume it mounted
// away again, and the folder with it.
func TestAFailedBeginLeavesNoMount(t *testing.T) {
	s, _ := newSpool(t)
	fv := &fakeVolumes{}
	s.Volumes = fv
	if _, err := s.Begin(Manifest{Run: "r1"}, false); err == nil {
		t.Fatal("a manifest with no lane was written")
	}
	if m, u := fv.counts(); m != 1 || u != 1 {
		t.Errorf("mounted %d, unmounted %d", m, u)
	}
	if _, err := os.Stat(s.RunDir("r1")); !os.IsNotExist(err) {
		t.Errorf("the folder is left: %v", err)
	}
}

// TestTheRunsFilesAreShippedBeforeItsVolumeGoes: with ynr serve shipping, a run's end waits for it
// to take the closed files, which it deletes, then takes the volume away; a file ynr never takes is
// captured once the drain time is up, and an open file, which ynr does not delete, is captured.
func TestTheRunsFilesAreShippedBeforeItsVolumeGoes(t *testing.T) {
	s, _ := newSpool(t)
	fv := &fakeVolumes{}
	s.Volumes, s.Drain = fv, 300*time.Millisecond
	s.Shipping = func() bool { return true }

	r, err := s.Begin(manifest("shipped"), false)
	if err != nil {
		t.Fatal(err)
	}
	closed := filepath.Join(r.Dir, "ynh-1-0.jsonl")
	open := filepath.Join(r.Dir, "vendor-1-0.open.jsonl")
	_ = os.WriteFile(closed, []byte("{}\n"), 0o644)
	_ = os.WriteFile(open, []byte("{}\n"), 0o644)
	go func() { // ynr serve ships the closed file and deletes it
		time.Sleep(80 * time.Millisecond)
		_ = os.Remove(closed)
	}()
	capture := filepath.Join(t.TempDir(), "spool")
	start := time.Now()
	r.End(capture)
	if time.Since(start) < 80*time.Millisecond || time.Since(start) > 250*time.Millisecond {
		t.Errorf("the end took %v: it waits for the shipping, and no longer", time.Since(start))
	}
	if _, err := os.Stat(filepath.Join(capture, "ynh-1-0.jsonl")); err == nil {
		t.Error("a shipped file was captured as well")
	}
	if _, err := os.Stat(filepath.Join(capture, "vendor-1-0.open.jsonl")); err != nil {
		t.Errorf("an open file, which ynr does not delete, was not captured: %v", err)
	}

	r, err = s.Begin(manifest("stuck"), false)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(r.Dir, "ynh-2-0.jsonl"), []byte("{}\n"), 0o644)
	capture = filepath.Join(t.TempDir(), "spool")
	r.End(capture)
	if _, err := os.Stat(filepath.Join(capture, "ynh-2-0.jsonl")); err != nil {
		t.Errorf("a file ynr did not ship was lost with the volume: %v", err)
	}
	if m, u := fv.counts(); m != 2 || u != 2 {
		t.Errorf("mounted %d, unmounted %d", m, u)
	}
}

// TestCloseTakesAwayAVolumeWhoseRunNeverEnded: the job's end keeps what is in a volume nobody
// ended, such as a run that panicked, then unmounts it, once.
func TestCloseTakesAwayAVolumeWhoseRunNeverEnded(t *testing.T) {
	s, _ := newSpool(t)
	fv := &fakeVolumes{}
	s.Volumes = fv
	r, err := s.Begin(manifest("lost"), false)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(r.Dir, "a.jsonl"), []byte("{}\n"), 0o644)
	job := t.TempDir()
	s.Close(job)
	s.Close(job)
	if _, u := fv.counts(); u != 1 {
		t.Errorf("unmounted %d times, want 1", u)
	}
	if _, err := os.Stat(filepath.Join(job, "runs", "lost", "a.jsonl")); err != nil {
		t.Errorf("what was in the volume was not kept: %v", err)
	}
	// The run's own capture wins when it has one.
	r, _ = s.Begin(manifest("known"), false)
	_ = os.WriteFile(filepath.Join(r.Dir, "b.jsonl"), []byte("{}\n"), 0o644)
	own := filepath.Join(t.TempDir(), "spool")
	s.Capture("known", own)
	s.Sweep(job)
	if _, err := os.Stat(filepath.Join(own, "b.jsonl")); err != nil {
		t.Errorf("the job's sweep did not keep it with the run's capture: %v", err)
	}
	if _, u := fv.counts(); u != 2 {
		t.Errorf("unmounted %d times, want 2", u)
	}
}

// TestAVolumeThatWillNotUnmountIsLoggedAndTheStepGoesOn: the folder stays, and nothing fails.
func TestAVolumeThatWillNotUnmountIsLoggedAndTheStepGoesOn(t *testing.T) {
	s, logs := newSpool(t)
	s.Volumes = &fakeVolumes{failUn: errors.New("busy")}
	r, err := s.Begin(manifest("busy"), false)
	if err != nil {
		t.Fatal(err)
	}
	r.End(filepath.Join(t.TempDir(), "spool"))
	if !strings.Contains(logs.String(), "could not be unmounted") {
		t.Errorf("not logged:\n%s", logs)
	}
	if _, err := os.Stat(r.Dir); err != nil {
		t.Errorf("the folder went with a volume that is still mounted: %v", err)
	}
}
