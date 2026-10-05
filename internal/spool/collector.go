package spool

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// DefaultArchive is how long ynr serve is given, at a job's end, to ship what is left.
const DefaultArchive = 30 * time.Second

// Collector is the configuration's collector settings (ADR-009): whether ynf starts ynr serve for
// a factory job, and how. It is off by default, and nothing else turns it on: not finding ynr.
type Collector struct {
	Enabled bool
	// ID is the collector's identity: the runner pool or the host, never one job (ynr ADR-003).
	ID string
	// Instance is the job within the pool: data, not identity.
	Instance string
	// Upstream is the OTLP/HTTP endpoint ynr serve ships to. ynr needs one until it has an object
	// store of its own.
	Upstream string
	// Archive is how long ynr serve has at a job's end to ship what is left; default 30s.
	Archive time.Duration
}

// YnrBin is the ynr binary ynf starts and asks: YNF_YNR_BIN when set, else ynr on PATH.
func YnrBin() string {
	if b := os.Getenv("YNF_YNR_BIN"); b != "" {
		return b
	}
	return "ynr"
}

// UpstreamFromEnv is the endpoint the operator set for OpenTelemetry, which ynr serve ships to
// when the collector is on (ynr ADR-004): OTEL_EXPORTER_OTLP_ENDPOINT, else a signal's own
// endpoint with its /v1/<signal> path taken off, else YNR_UPSTREAM, which ynr reads itself. It is
// empty when there is none.
func UpstreamFromEnv(getenv func(string) string) string {
	if v := getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); v != "" {
		return v
	}
	for _, sig := range []string{"TRACES", "LOGS", "METRICS"} {
		if v := getenv("OTEL_EXPORTER_OTLP_" + sig + "_ENDPOINT"); v != "" {
			return strings.TrimSuffix(strings.TrimRight(v, "/"), "/v1/"+strings.ToLower(sig))
		}
	}
	return getenv("YNR_UPSTREAM")
}

// Args is ynr serve's command line for the spool root.
func (c Collector) Args(root string) []string {
	args := []string{"serve", "--spool", root, "--collector-id", c.ID}
	if c.Instance != "" {
		args = append(args, "--collector-instance", c.Instance)
	}
	if c.Upstream != "" {
		args = append(args, "--upstream", c.Upstream)
	}
	return args
}

// Serve is a ynr serve that ynf started for a job.
type Serve struct {
	cfg  Collector
	log  *slog.Logger
	cmd  *exec.Cmd
	done chan struct{}
	mu   sync.Mutex
	err  error // how it ended, once done
	// stopped is set when Stop is called, so a planned end is not reported as an early one.
	stopped atomic.Bool
	early   sync.Once
}

// StartServe starts ynr serve on the spool root for the length of a job. It never fails the job:
// when ynr is missing or does not start, it says so in the log and returns nil, and the job runs
// on without a collector. bin is the ynr to start (YnrBin); environ is the environment it gets.
func StartServe(c Collector, bin, root string, environ []string, log *slog.Logger) *Serve {
	path, err := exec.LookPath(bin)
	if err != nil {
		log.Warn("the collector is enabled but ynr was not found: this job runs without it, and its telemetry stays in the spool", "ynr", bin, "err", err)
		return nil
	}
	cmd := exec.Command(path, c.Args(root)...)
	cmd.Env = environ
	// ynr is stopped by the signal ynf sends at the job's end, not by the job's own Ctrl-C going
	// to its process group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		log.Warn("the collector did not start: this job runs without it", "err", err)
		return nil
	}
	cmd.Stdout = io.Discard
	if err := cmd.Start(); err != nil {
		log.Warn("the collector did not start: this job runs without it", "ynr", path, "err", err)
		return nil
	}
	s := &Serve{cfg: c, log: log, cmd: cmd, done: make(chan struct{})}
	lines := make(chan struct{})
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			// ynr serve's own log is verbose at info (every component says it is starting): that
			// is debug here, and anything else, such as its refusing a setting, is the job's to see.
			line := sc.Text()
			level := slog.LevelInfo
			if strings.Contains(line, "\tinfo\t") {
				level = slog.LevelDebug
			}
			log.Log(context.Background(), level, "ynr serve", "line", line)
		}
	}()
	go func() {
		<-lines // the pipe's reader must finish before Wait closes it
		err := cmd.Wait()
		s.mu.Lock()
		s.err = err
		s.mu.Unlock()
		close(s.done)
	}()
	log.Info("collector started", "ynr", path, "collector", c.ID, "instance", c.Instance, "spool", root)
	go s.watchEarly()
	return s
}

// watchEarly says so when ynr serve ends while the job is still running, such as when it refuses
// its settings: the job does not stop for it.
func (s *Serve) watchEarly() {
	<-s.done
	s.reportEarly()
}

// reportEarly says once that ynr serve ended before it was stopped.
func (s *Serve) reportEarly() {
	s.early.Do(func() {
		if s.stopped.Load() {
			return
		}
		s.mu.Lock()
		err := s.err
		s.mu.Unlock()
		s.log.Warn("ynr serve ended before the job did: the rest of this job runs without a collector, and its telemetry stays in the spool", "err", errString(err))
	})
}

func errString(err error) string {
	if err == nil {
		return "exited"
	}
	return err.Error()
}

// Running reports whether ynr serve is still running. It is safe on a nil Serve.
func (s *Serve) Running() bool {
	if s == nil {
		return false
	}
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

// Stop ends ynr serve at a job's end: SIGTERM, then the archive time to ship what is left, then
// a kill. It reports whether ynr serve ended within the time. It is safe on a nil Serve.
func (s *Serve) Stop() bool {
	if s == nil {
		return true
	}
	if !s.Running() {
		s.reportEarly() // it ended on its own, before the job did
		s.stopped.Store(true)
		return true
	}
	s.stopped.Store(true)
	archive := s.cfg.Archive
	if archive <= 0 {
		archive = DefaultArchive
	}
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-s.done:
		s.mu.Lock()
		err := s.err
		s.mu.Unlock()
		var exit *exec.ExitError
		if err != nil && !errors.As(err, &exit) {
			s.log.Warn("ynr serve ended badly", "err", err)
		}
		s.log.Info("collector stopped")
		return true
	case <-time.After(archive):
		_ = s.cmd.Process.Kill()
		<-s.done
		s.log.Warn(fmt.Sprintf("ynr serve did not finish shipping in %s and was stopped: what it had not shipped is in the run capture", archive))
		return false
	}
}
