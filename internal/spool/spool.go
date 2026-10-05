// Package spool is ynf's side of the spool layout in ynr ADR-003 and ADR-004: the root's three
// folders (factory/ for ynf's own writer, runs/<run id>/ one per run, manifests/<run id>.json one
// per run), the manifest, the quota on a run's folder, ynr serve for the length of a factory job,
// and what is swept into the run capture (ADR-007, ADR-009, ADR-010, ADR-011).
//
// Nothing here ever fails a step: telemetry is the factory's side effect, and a full or broken
// spool costs telemetry, never work (ynr ADR-006, rule 12).
package spool

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The folders under the spool root.
const (
	FactoryDir   = "factory"
	RunsDir      = "runs"
	ManifestsDir = "manifests"
)

// ContainerPath is where a docker run sees its own folder, and nothing else of the spool.
const ContainerPath = "/run/ynr/spool"

// MaxManifest is the largest manifest ynr reads (64 KiB).
const MaxManifest = 64 << 10

// DefaultQuota is the size a run's folder is held to when the configuration names none.
const DefaultQuota = 64 << 20

// The most that is put into a run capture (ADR-010): per run, and per job for what is swept at
// its end. A spool file past the limit is left where it is and counted in the log.
const (
	RunCaptureLimit = 32 << 20
	JobCaptureLimit = 256 << 20
)

// name is what ynr accepts for a run folder (ynr's spool.ValidName).
var name = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// ValidName reports whether s can name a run folder and its manifest.
func ValidName(s string) bool { return name.MatchString(s) }

// Manifest is what ynf writes for each run, out of the run's reach (ynr ADR-003): ynr stamps the
// factory attributes on everything from the run's folder from it, so a run cannot claim another
// factory. It holds ids and names, never content.
type Manifest struct {
	Run     string `json:"run"`
	Lane    string `json:"lane"`
	Harness string `json:"harness"`
	Focus   string `json:"focus"`
	Item    string `json:"item"`
	Step    string `json:"step"`
	// UID is the user the run writes as, when that is not the owner of its folder: a ynh-built
	// image's own user, or the inline run user. ynr then also accepts the files that user owns in
	// the run's folder. It comes from the image or the executor's own setting, never from the run,
	// and is omitted when the run writes as the folder's owner.
	UID *uint32 `json:"uid,omitempty"`
}

// Spool is the spool root as ynf uses it.
type Spool struct {
	Root string
	// Quota is how large a run's folder may grow; default DefaultQuota.
	Quota int64
	// Interval is how often a run's folder is measured; default 250ms.
	Interval time.Duration
	Log      *slog.Logger

	// Shipping reports whether ynr serve is running on this spool, so it ships the files and a
	// run's end captures nothing. Nil means it is not.
	Shipping func() bool

	// Volumes make a run's folder a size-limited filesystem of its own, so the quota is hard. Nil,
	// or a host that does not allow one, keeps the quota watcher as the bound.
	Volumes Volumes
	// Drain is how long a run's end waits for ynr serve to ship the run's files before its volume
	// is taken away; default DefaultDrain.
	Drain time.Duration
	// Alive reports whether the ynf process that holds a run volume's lease is running, for the
	// volumes a killed ynf left (CleanStale). Nil checks the process table.
	Alive func(pid int, start string) bool

	volumeNote sync.Once

	mu       sync.Mutex
	captured map[string]string // run id to its capture folder
	done     map[string]bool   // files already captured, by path under the root
	active   map[string]*Run   // runs whose folder is a volume, until it is taken away
}

// New makes the layout under root: factory/ and manifests/ for ynf alone, runs/ that a run user
// can enter but not list. It never follows a link: a root's folder that is one is an error.
func New(root string, quota int64, log *slog.Logger) (*Spool, error) {
	if root == "" {
		return nil, errors.New("spool: no root")
	}
	if quota <= 0 {
		quota = DefaultQuota
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("spool: %w", err)
	}
	s := &Spool{Root: root, Quota: quota, Log: log, captured: map[string]string{}, done: map[string]bool{}, active: map[string]*Run{}}
	for _, d := range []struct {
		name string
		mode os.FileMode
	}{{FactoryDir, 0o700}, {ManifestsDir, 0o700}, {RunsDir, 0o711}} {
		if err := ensureDir(filepath.Join(root, d.name), d.mode); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// ensureDir makes path a real directory with the mode, refusing a symbolic link or a file.
func ensureDir(path string, mode os.FileMode) error {
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.Mkdir(path, mode); err != nil {
			return fmt.Errorf("spool: %w", err)
		}
		return os.Chmod(path, mode) // the umask must not narrow it
	case err != nil:
		return fmt.Errorf("spool: %w", err)
	case !fi.IsDir():
		return fmt.Errorf("spool: %s is not a folder (a link or a file): ynf will not use it", path)
	}
	return nil
}

// FactoryDir is ynf's own writer folder.
func (s *Spool) FactoryDir() string { return filepath.Join(s.Root, FactoryDir) }

// RunDir is a run's folder.
func (s *Spool) RunDir(id string) string { return filepath.Join(s.Root, RunsDir, id) }

func (s *Spool) isShipping() bool { return s.Shipping != nil && s.Shipping() }

// WriteManifest writes manifests/<run id>.json as ynr reads it: the run id equal to the folder's,
// a non-empty lane, under 64 KiB, a regular file. It goes to a temporary name in manifests/ and
// is renamed into place, so ynr never reads half of it, and it refuses to replace anything that
// is not a regular file, such as a link.
func (s *Spool) WriteManifest(m Manifest) error {
	switch {
	case !ValidName(m.Run):
		return fmt.Errorf("manifest: %q is not a run id ynr reads", m.Run)
	case m.Lane == "":
		return errors.New("manifest: no lane id")
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if len(b) >= MaxManifest {
		return fmt.Errorf("manifest: %d bytes, over the %d ynr reads", len(b), MaxManifest)
	}
	dir := filepath.Join(s.Root, ManifestsDir)
	if err := ensureDir(dir, 0o700); err != nil {
		return err
	}
	target := filepath.Join(dir, m.Run+".json")
	if fi, err := os.Lstat(target); err == nil && !fi.Mode().IsRegular() {
		return fmt.Errorf("manifest: %s exists and is not a regular file", target)
	}
	tmp, err := os.CreateTemp(dir, "."+m.Run+".*.tmp")
	if err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	_, werr := tmp.Write(b)
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp.Name(), 0o644)
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), target)
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("manifest: %w", werr)
	}
	return nil
}

// Run is one run's folder, with its quota being held.
type Run struct {
	ID  string
	Dir string

	s    *Spool
	vol  *mounted // the run's own volume, when the host gave one
	stop chan struct{}
	done chan struct{}
	tr   trimmed
}

// Begin gives a run its folder and its manifest, before the run starts, and holds the folder to
// its quota: as a volume of its own with a hard limit where the host allows one, else with the
// quota watcher. It is all or nothing: without a manifest ynr could not say which factory
// the run belongs to, so no folder is left behind for it. imageUser is for a run whose container
// keeps the image's own user, who needs the folder open to them.
//
// The caller runs on without a spool when it returns an error: a spool that cannot be written
// never fails a step.
func (s *Spool) Begin(m Manifest, imageUser bool) (*Run, error) {
	return s.BeginWith(m, imageUser, true)
}

// BeginWith is Begin for a run whose executor may not be able to use a volume: with volume false
// the folder is a plain one, bound by the quota watcher, whatever the host allows.
func (s *Spool) BeginWith(m Manifest, imageUser, volume bool) (*Run, error) {
	if !ValidName(m.Run) {
		return nil, fmt.Errorf("spool: %q is not a run id ynr reads", m.Run)
	}
	dir := s.RunDir(m.Run)
	mode := os.FileMode(0o700)
	if imageUser {
		// Only that run's container has it mounted; the image's user is not ynf's.
		mode = 0o777
	}
	if err := os.Mkdir(dir, mode); err != nil {
		return nil, fmt.Errorf("spool: %w", err)
	}
	if err := os.Chmod(dir, mode); err != nil {
		_ = os.Remove(dir)
		return nil, fmt.Errorf("spool: %w", err)
	}
	r := &Run{ID: m.Run, Dir: dir, s: s, stop: make(chan struct{}), done: make(chan struct{})}
	if volume {
		r.vol = s.mount(dir)
	}
	if r.vol != nil {
		// The volume's own root is the folder now, so it takes the mode again.
		if err := os.Chmod(dir, mode); err != nil {
			_ = r.vol.release()
			_ = os.Remove(dir)
			return nil, fmt.Errorf("spool: %w", err)
		}
	}
	if err := s.WriteManifest(m); err != nil {
		if r.vol != nil {
			_ = r.vol.release()
		}
		_ = os.Remove(dir)
		return nil, err
	}
	if r.vol != nil {
		// A volume without a lease is never cleaned up after: failing to write one costs only that.
		if err := s.writeOwner(m.Run, s.Volumes.Backing(dir)); err != nil {
			s.Log.Warn("a run's spool volume has no lease: a later job cannot clean it up if ynf is killed", "run", m.Run, "err", err)
		}
		s.mu.Lock()
		s.active[m.Run] = r
		s.mu.Unlock()
		close(r.done) // the volume is the bound: there is nothing to watch
		return r, nil
	}
	go r.watch()
	return r, nil
}

// Capture records where a run's leftovers go, so the sweep at a job's end puts them beside the
// run's own capture.
func (s *Spool) Capture(runID, dir string) {
	s.mu.Lock()
	s.captured[runID] = dir
	s.mu.Unlock()
}

// End stops holding the folder to its quota and, when nothing will ship what is in it, copies
// those files into dir, the run's capture (ADR-010). A folder that is a volume is shipped or
// captured, then taken away. It is safe to call on a nil Run.
func (r *Run) End(dir string) {
	if r == nil {
		return
	}
	if r.vol == nil {
		close(r.stop)
	}
	<-r.done
	r.s.Capture(r.ID, dir)
	if r.vol != nil {
		r.finishVolume(dir)
		return
	}
	if r.s.isShipping() {
		return // ynr serve ships them, and the sweep at the job's end takes what it did not
	}
	n := copyFiles(r.s, filepath.Join(RunsDir, r.ID), dir, &budget{left: RunCaptureLimit})
	if n > 0 {
		r.s.Log.Info("spool files kept in the run capture", "run", r.ID, "files", n, "dir", dir)
	}
}

// Trimmed is how many files the quota removed from the run's folder.
func (r *Run) Trimmed() int {
	if r == nil {
		return 0
	}
	return r.tr.get()
}

// Env is the environment that points a run at its folder: where the run sees it, which is its
// own path except in a container.
func Env(host string, inContainer bool) map[string]string {
	if inContainer {
		return map[string]string{"YNR_SPOOL": ContainerPath}
	}
	return map[string]string{"YNR_SPOOL": host}
}

// StripOTLP removes the operator's OTEL_EXPORTER_OTLP_* from an environment, for a run when the
// collector is on: its records go to its own folder, and the operator's endpoint is served by
// ynr serve's upstream instead (ynr ADR-004).
func StripOTLP(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		if strings.HasPrefix(kv, "OTEL_EXPORTER_OTLP_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}
