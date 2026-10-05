package spool

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// backingPrefix names the folder holding the disk image ynf makes for a run (macOS). A leftover
// image is only ever removed from a folder of this name.
const backingPrefix = "ynf-run-volume-"

// owner is the lease ynf records for a run whose folder is a volume, in manifests/<run id>.owner:
// beside the manifest, in the one folder a run cannot reach, and not in the run's folder, which is
// the run's to write. It names the ynf process that made the volume, so a later job can tell a
// volume whose ynf was killed from one whose run is still going.
//
// A PID alone is not enough: the kernel reuses them, and a dead ynf's PID may belong to anything
// by the time the next job starts. So the lease holds the process's start time too, and the owner
// is alive only while a process with that PID has that start time. Host is there because a spool
// root may be shared: a mount is the host's, so another host's volume is never ours to touch.
// Backing is the disk image ynf made, where there is one: the macOS volume is found by it, and by
// nothing else attached to the machine.
type owner struct {
	Host    string `json:"host"`
	PID     int    `json:"pid"`
	Start   string `json:"start"`
	Backing string `json:"backing,omitempty"`
}

// ownerPath is where a run's lease is kept.
func (s *Spool) ownerPath(id string) string {
	return filepath.Join(s.Root, ManifestsDir, id+".owner")
}

// writeOwner records this process as the owner of the run's volume.
func (s *Spool) writeOwner(id, backing string) error {
	host, _ := os.Hostname()
	start, _ := procStart(os.Getpid()) // without it the PID alone is the test
	b, err := json.Marshal(owner{Host: host, PID: os.Getpid(), Start: start, Backing: backing})
	if err != nil {
		return err
	}
	return os.WriteFile(s.ownerPath(id), b, 0o600)
}

func (s *Spool) readOwner(id string) (owner, bool) {
	var o owner
	b, err := os.ReadFile(s.ownerPath(id))
	if err != nil || json.Unmarshal(b, &o) != nil || o.Host == "" || o.PID <= 0 {
		return owner{}, false
	}
	return o, true
}

// errNoProcess is procStart's answer for a PID that is not running.
var errNoProcess = errors.New("no such process")

// processAlive reports whether the process that holds a lease is still running: a process with
// that PID, and with the start time the lease recorded. When it cannot tell, it says alive, since
// the cost of a wrong answer is a live run's volume taken away.
func processAlive(pid int, start string) bool {
	if pid <= 0 {
		return false
	}
	got, err := procStart(pid)
	switch {
	case errors.Is(err, errNoProcess):
		return false
	case err != nil:
		return true
	}
	return start == "" || got == start
}

// CleanStale takes away the run volumes a ynf that is no longer running left behind, at the start
// of a job. A volume is stale only when it has a lease (owner) in manifests/ written on this host
// by a process that is gone, and its run is not one of this process's own. A run folder without a
// lease, a lease whose process is running (this ynf or another one sharing the root), and anything
// that is not ynf's own volume are left exactly as they are. For a stale one it copies the spool
// files into the run's capture as the run's end would, within the capture limits, unmounts the
// volume, and removes the folder. capture is the job's capture folder, for a run that has no
// capture of its own.
//
// It never fails the job: each volume that could not be cleaned is logged and left for the next
// job, and what was cleaned is logged once.
func (s *Spool) CleanStale(capture string) {
	if s.Volumes == nil {
		return
	}
	ents, err := os.ReadDir(filepath.Join(s.Root, RunsDir))
	if err != nil {
		return
	}
	alive := s.Alive
	if alive == nil {
		alive = processAlive
	}
	host, _ := os.Hostname()
	job := &budget{left: JobCaptureLimit}
	var cleaned, files, failed int
	for _, e := range ents {
		id := e.Name()
		if !e.IsDir() || !ValidName(id) {
			continue
		}
		o, ok := s.readOwner(id)
		if !ok || o.Host != host {
			continue
		}
		s.mu.Lock()
		_, mine := s.active[id]
		s.mu.Unlock()
		if mine || alive(o.PID, o.Start) {
			continue
		}
		// Claim the lease, so two jobs starting together do not both clean this volume. A lease
		// that is gone was taken by the other.
		claim := s.ownerPath(id) + ".cleaning"
		if os.Rename(s.ownerPath(id), claim) != nil {
			continue
		}
		n, err := s.cleanOne(id, o, capture, job)
		files += n
		if err != nil {
			failed++
			s.Log.Warn("a run volume left by a ynf that was killed could not be cleaned up", "run", id, "dir", s.RunDir(id), "err", err)
			_ = os.Rename(claim, s.ownerPath(id)) // the next job tries again
			continue
		}
		_ = os.Remove(claim)
		cleaned++
	}
	if cleaned > 0 {
		s.Log.Info("run volumes left by a ynf that was killed were cleaned up", "volumes", cleaned, "files", files, "failed", failed)
	}
}

// cleanOne cleans one stale run's volume: its spool files are kept first, since nothing in the
// volume survives its removal, then it is detached and its folder removed. job is what is left of
// the capture limit for the job.
func (s *Spool) cleanOne(id string, o owner, capture string, job *budget) (int, error) {
	dir := s.RunDir(id)
	detach, err := s.Volumes.Attached(dir, o.Backing)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	to, ok := s.captured[id]
	s.mu.Unlock()
	if !ok {
		to = filepath.Join(capture, RunsDir, id)
	}
	b := &budget{left: min(RunCaptureLimit, job.left)}
	before := b.left
	n := copyFiles(s, filepath.Join(RunsDir, id), to, b)
	job.left -= before - b.left
	if n > 0 {
		s.Log.Info("spool files kept in the run capture", "run", id, "files", n, "dir", to)
	}
	if detach != nil {
		if err := detach(); err != nil {
			return n, err
		}
	} else {
		removeBacking(o.Backing) // the image is there, and the volume is not
	}
	_ = os.Remove(dir) // not empty only when a file was over the capture limit: it stays
	return n, nil
}

// removeBacking removes the folder of a run's disk image, and only a folder ynf made for one.
func removeBacking(image string) {
	d := filepath.Dir(image)
	if image == "" || !strings.HasPrefix(filepath.Base(d), backingPrefix) || !strings.HasSuffix(image, ".sparseimage") {
		return
	}
	_ = os.RemoveAll(d)
}

// parseMountinfo returns the mount points of the tmpfs mounts whose source is source, from
// /proc/self/mountinfo (proc(5)): the mount point is the fifth field, escaped, and the file system
// type and source follow the " - " separator.
func parseMountinfo(info, source string) []string {
	var out []string
	for _, line := range strings.Split(info, "\n") {
		pre, post, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		f, g := strings.Fields(pre), strings.Fields(post)
		if len(f) < 5 || len(g) < 2 || g[0] != "tmpfs" || g[1] != source {
			continue
		}
		out = append(out, unescapeMount(f[4]))
	}
	return out
}

// unescapeMount undoes the octal escapes (\040 for a space) of a mountinfo path.
func unescapeMount(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if c, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(c))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// attachedImage is one image in hdiutil info's text: the image's path and its first device node.
type attachedImage struct{ image, dev string }

// parseHdiutilInfo reads `hdiutil info`: images are separated by a line of equals signs, each
// with an "image-path" line and its devices after it.
func parseHdiutilInfo(out string) []attachedImage {
	var res []attachedImage
	for _, block := range strings.Split(out, "\n================================================") {
		var d attachedImage
		for _, line := range strings.Split(block, "\n") {
			if p, ok := strings.CutPrefix(line, "image-path"); ok {
				if _, v, ok := strings.Cut(p, ":"); ok {
					d.image = strings.TrimSpace(v)
				}
			} else if d.dev == "" && strings.HasPrefix(line, "/dev/") {
				d.dev = strings.Fields(line)[0]
			}
		}
		if d.image != "" {
			res = append(res, d)
		}
	}
	return res
}
