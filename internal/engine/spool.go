package engine

import (
	"path/filepath"

	"github.com/eyelock/ynf/internal/executor"
	"github.com/eyelock/ynf/internal/spool"
)

// beginSpool gives a run its own spool folder and its manifest, before the run starts, and points
// the run's job at the folder (ynr ADR-003, ADR-004): YNR_SPOOL for the run, the folder mounted
// alone in docker mode, and without the operator's OTEL_EXPORTER_OTLP_* when the collector is on.
//
// It never fails the step. A spool that cannot be written costs the run its telemetry, and is
// logged; the run starts as it would have without one. It returns nil then, and when there is no
// spool configured, which leaves the run's environment as it was.
func (s *step) beginSpool(job *executor.Job, m spool.Manifest) *spool.Run {
	e := s.e
	if e.Spool == nil {
		return nil
	}
	if e.SpoolCollector {
		job.NoOTLP = true // ynr serve ships to the operator's endpoint; the run does not
	}
	run, err := e.Spool.Begin(m, job.ImageUser)
	if err != nil {
		e.log().Warn("this run has no spool folder: its telemetry is not collected, and the run goes on", "run", m.Run, "err", err)
		return nil
	}
	job.Spool = run.Dir
	return run
}

// endSpool stops holding the run's folder to its quota, and keeps what is left in it in the run's
// capture when nothing will ship it (ADR-010). The capture is the step folder's spool/, which the
// run cannot reach.
func (s *step) endSpool(run *spool.Run, stepDir string) {
	if run == nil {
		return
	}
	run.End(filepath.Join(stepDir, "spool"))
}
