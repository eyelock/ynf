package spool

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"
)

// maxEntries bounds how many entries a run's folder may hold, so a folder of millions of empty
// files counts against the quota too.
const maxEntries = 10000

type trimmed struct {
	mu sync.Mutex
	n  int
}

func (t *trimmed) add(n int) { t.mu.Lock(); t.n += n; t.mu.Unlock() }
func (t *trimmed) get() int  { t.mu.Lock(); defer t.mu.Unlock(); return t.n }

// watch holds the run's folder to its quota while the run lasts.
//
// This is the fallback, for a run folder that is not a volume of its own (volume.go), and it is a
// bound, not a hard limit: the folder is measured every interval, and the largest files are removed
// until it is back under, so an agent that floods it gets, at most, an interval's worth of writes
// over the quota before they are taken away. ADR-007 says which executor and host gets which.
func (r *Run) watch() {
	defer close(r.done)
	every := r.s.Interval
	if every <= 0 {
		every = 250 * time.Millisecond
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-r.stop:
			r.enforce() // once more: the last interval's writes
			return
		case <-tick.C:
			r.enforce()
		}
	}
}

type entry struct {
	path string
	size int64
}

// enforce removes files from the run's folder, largest first, until it is within the quota. It
// never follows a link and never fails: a file it cannot remove stays.
func (r *Run) enforce() {
	var files []entry
	var total int64
	entries := 0
	_ = filepath.WalkDir(r.Dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == r.Dir {
			return nil
		}
		entries++
		if d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				files = append(files, entry{p, fi.Size()})
				total += fi.Size()
			}
		} else if !d.IsDir() {
			// A link, device or socket holds nothing the quota is for, and ynr ignores it.
			files = append(files, entry{p, 0})
		}
		return nil
	})
	if total <= r.s.Quota && entries <= maxEntries {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].size > files[j].size })
	n := 0
	for _, f := range files {
		if total <= r.s.Quota && entries <= maxEntries {
			break
		}
		if os.Remove(f.path) == nil {
			total -= f.size
			entries--
			n++
		}
	}
	if n > 0 {
		r.tr.add(n)
		r.s.Log.Warn("a run filled its spool folder: files over the quota were removed", "run", r.ID, "removed", n, "quota", r.s.Quota)
	}
}

// budget is how many bytes of spool files may still be copied into a capture.
type budget struct {
	left    int64
	dropped int
}

// copyFiles copies the spool files (*.jsonl, which includes *.open.jsonl) of the writer folder
// rel, under the root, into dst, as ADR-010 keeps them: regular files only, opened without
// following a link, within the budget. It returns how many it copied; the originals stay, since a
// persistent spool is read by the next ynr serve.
func copyFiles(s *Spool, rel, dst string, b *budget) int {
	src := filepath.Join(s.Root, rel)
	ents, err := os.ReadDir(src)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range ents {
		if !e.Type().IsRegular() || filepath.Ext(e.Name()) != ".jsonl" {
			continue
		}
		key := filepath.Join(rel, e.Name())
		s.mu.Lock()
		seen := s.done[key]
		s.mu.Unlock()
		fi, err := e.Info()
		if err != nil || seen {
			continue
		}
		if fi.Size() > b.left {
			b.dropped++
			s.Log.Warn("a spool file is over the capture limit and stays in the spool", "file", key, "bytes", fi.Size())
			continue
		}
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return n
		}
		if copyOne(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())) == nil {
			b.left -= fi.Size()
			n++
			s.mu.Lock()
			s.done[key] = true
			s.mu.Unlock()
		}
	}
	return n
}

// copyOne copies a regular file, refusing a link.
func copyOne(from, to string) error {
	fd, err := syscall.Open(from, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	in := os.NewFile(uintptr(fd), from)
	defer func() { _ = in.Close() }()
	if fi, err := in.Stat(); err != nil || !fi.Mode().IsRegular() {
		return os.ErrInvalid
	}
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// Sweep puts any spool file still in the spool, in runs/ and factory/, into the run capture at a
// job's end (ADR-010): a run's into its own capture when it has one, the rest under dir. It is
// called after ynr serve has had its archive time, so what is left is what was not shipped.
func (s *Spool) Sweep(dir string) int {
	s.Close(dir) // a run volume still mounted is kept, then taken away
	b := &budget{left: JobCaptureLimit}
	n := copyFiles(s, FactoryDir, filepath.Join(dir, FactoryDir), b)
	runs, err := os.ReadDir(filepath.Join(s.Root, RunsDir))
	if err != nil {
		return n
	}
	for _, e := range runs {
		if !e.IsDir() {
			continue
		}
		s.mu.Lock()
		to, ok := s.captured[e.Name()]
		s.mu.Unlock()
		if !ok {
			to = filepath.Join(dir, RunsDir, e.Name())
		}
		n += copyFiles(s, filepath.Join(RunsDir, e.Name()), to, b)
	}
	if n > 0 || b.dropped > 0 {
		s.Log.Info("spool files not shipped were kept in the run capture", "files", n, "over_limit", b.dropped, "dir", dir)
	}
	return n
}
