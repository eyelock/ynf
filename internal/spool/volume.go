package spool

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ErrNoVolume means the host does not let ynf make a size-limited volume of its own: the quota
// watcher bounds the run's folder instead. It is expected, not a fault, in a job container without
// CAP_SYS_ADMIN, for an unprivileged user on Linux, and on a host with no implementation.
var ErrNoVolume = errors.New("no size-limited volume here")

// Volumes make a folder a filesystem of its own with a hard size limit, which ynr serve reads as
// a run folder on a device of its own (ynr ADR-003). An implementation is the host's: a tmpfs on
// Linux, a disk image on macOS.
type Volumes interface {
	// Name says what kind of volume it is, for the log.
	Name() string
	// Mount makes dir, an empty folder, a filesystem of at most size bytes and entries files and
	// folders, owned by the user ynf runs as. It returns how to take it away again, which must
	// work whether or not anything is left in it. It returns an error wrapping ErrNoVolume when
	// the host does not allow a volume.
	Mount(dir string, size int64, entries int) (unmount func() error, err error)
	// Backing is what, besides the mount point, identifies the volume Mount made at dir, for a
	// later ynf to find it by: the disk image's path on macOS, empty where the mount point is all
	// there is.
	Backing(dir string) string
	// Attached finds the volume that ynf made at dir (with that backing) and returns how to take
	// it away, which removes whatever Backing names too. It returns nil when there is none. It is
	// for a volume whose ynf was killed, and it must match only what ynf itself made: never a
	// mount or an image that merely is attached to the host.
	Attached(dir, backing string) (detach func() error, err error)
}

// DefaultDrain is how long a run's end waits for ynr serve to ship the run's closed files before
// they are captured and the run's volume is taken away.
const DefaultDrain = 10 * time.Second

// mounted is a run folder's volume, taken away once.
type mounted struct {
	once    sync.Once
	unmount func() error
	err     error
}

func (m *mounted) release() error {
	m.once.Do(func() { m.err = m.unmount() })
	return m.err
}

// mount makes the run's folder a volume of its own when the host allows, and says which path it
// took, once for the job. It returns nil when it did not: the quota watcher is the bound then.
func (s *Spool) mount(dir string) *mounted {
	if s.Volumes == nil {
		return nil
	}
	um, err := s.Volumes.Mount(dir, s.Quota, maxEntries)
	if err != nil {
		s.volumeNote.Do(func() {
			if errors.Is(err, ErrNoVolume) {
				s.Log.Info("a run's spool folder cannot be a volume of its own here: the quota watcher bounds it instead", "volumes", s.Volumes.Name(), "why", err.Error())
				return
			}
			s.Log.Warn("a run's spool folder could not be made a volume of its own: the quota watcher bounds it instead", "volumes", s.Volumes.Name(), "err", err)
		})
		return nil
	}
	s.volumeNote.Do(func() {
		s.Log.Info("each run's spool folder is a volume of its own, with a hard size limit", "volumes", s.Volumes.Name(), "quota", s.Quota)
	})
	return &mounted{unmount: um}
}

// drain waits for ynr serve to ship the closed files in the run's folder, which it deletes once it
// has, for at most the drain time. A file still there after that is captured with the rest.
func (r *Run) drain() {
	wait := r.s.Drain
	if wait <= 0 {
		wait = DefaultDrain
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) && r.hasClosed() {
		time.Sleep(50 * time.Millisecond)
	}
}

// hasClosed reports whether the folder holds a closed spool file (*.jsonl that is not
// *.open.jsonl), which ynr serve has yet to ship.
func (r *Run) hasClosed() bool {
	ents, err := os.ReadDir(r.Dir)
	if err != nil {
		return false
	}
	for _, e := range ents {
		n := e.Name()
		if e.Type().IsRegular() && strings.HasSuffix(n, ".jsonl") && !strings.HasSuffix(n, ".open.jsonl") {
			return true
		}
	}
	return false
}

// finishVolume is a run's end when its folder is a volume. What is in it is shipped or captured
// first, since nothing in it survives the volume: ynr serve is given the drain time to ship the
// closed files, and whatever is left goes into the run's capture.
func (r *Run) finishVolume(capture string) {
	if r.s.isShipping() {
		r.drain()
	}
	if n := copyFiles(r.s, filepath.Join(RunsDir, r.ID), capture, &budget{left: RunCaptureLimit}); n > 0 {
		r.s.Log.Info("spool files kept in the run capture", "run", r.ID, "files", n, "dir", capture)
	}
	r.unmountVolume()
}

// unmountVolume takes the run's volume away, and its now empty folder with it. A volume that will
// not unmount is logged, and the folder is left.
func (r *Run) unmountVolume() {
	if r.vol == nil {
		return
	}
	r.s.mu.Lock()
	delete(r.s.active, r.ID)
	r.s.mu.Unlock()
	if err := r.vol.release(); err != nil {
		r.s.Log.Warn("a run's spool volume could not be unmounted", "run", r.ID, "dir", r.Dir, "err", err)
		return
	}
	_ = os.Remove(r.s.ownerPath(r.ID)) // the lease goes with the volume
	_ = os.Remove(r.Dir)
}

// Close takes away any run volume still mounted, such as one whose run never reached its end,
// after keeping what is in it in the run capture: the run's own when it has one, else under dir.
// It is safe to call more than once.
func (s *Spool) Close(dir string) {
	s.mu.Lock()
	runs := make([]*Run, 0, len(s.active))
	for _, r := range s.active {
		runs = append(runs, r)
	}
	s.mu.Unlock()
	for _, r := range runs {
		s.mu.Lock()
		to, ok := s.captured[r.ID]
		s.mu.Unlock()
		if !ok {
			to = filepath.Join(dir, RunsDir, r.ID)
		}
		copyFiles(s, filepath.Join(RunsDir, r.ID), to, &budget{left: RunCaptureLimit})
		r.unmountVolume()
	}
}
