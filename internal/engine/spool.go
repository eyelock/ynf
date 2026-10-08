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
func (s *step) beginSpool(job *executor.Job, ex executor.Executor, m spool.Manifest) *spool.Run {
	e := s.e
	if e.Spool == nil {
		return nil
	}
	if e.SpoolCollector {
		job.NoOTLP = true // ynr serve ships to the operator's endpoint; the run does not
	}
	m.UID = s.runUser(*job, ex, m.Run)
	volume := true
	if v, ok := ex.(executor.SpoolVolumes); ok {
		volume = v.SpoolVolume()
	}
	run, err := e.Spool.BeginWith(m, job.ImageUser, volume)
	if err != nil {
		e.log().Warn("this run has no spool folder: its telemetry is not collected, and the run goes on", "run", m.Run, "err", err)
		return nil
	}
	job.Spool = run.Dir
	return run
}

// runUser is the user the run writes as when that is not the owner of its spool folder, for its
// manifest: the executor says, from the image's configuration or its own setting, never the run.
// When it cannot be found out the manifest omits it, and ynr then refuses what that user writes,
// which stays in the folder and is kept in the run capture instead; that is logged.
func (s *step) runUser(job executor.Job, ex executor.Executor, run string) *uint32 {
	ru, ok := ex.(executor.RunUser)
	if !ok {
		return nil
	}
	uid, other, err := ru.RunUID(s.ctx, job)
	if err != nil {
		s.e.log().Warn("the user this run writes as could not be found out, so its manifest does not name one and ynr may refuse its spool files", "run", run, "err", err)
		return nil
	}
	if !other {
		return nil
	}
	return &uid
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
