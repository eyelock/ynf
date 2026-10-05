package cli

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/eyelock/ynf/internal/config"
	"github.com/eyelock/ynf/internal/spool"
	"github.com/eyelock/ynf/internal/telemetry"
)

// isJob reports whether a command is a factory job: long-running work that runs steps, for which
// ynf starts ynr serve when the configuration enables it (ADR-009, ADR-011). That is sweep, serve,
// handle and shadow run. start is a person's own, attended command; it, and every command that only
// reads or records, leave the collector alone.
func isJob(cmd string, rest []string) bool {
	switch cmd {
	case "sweep", "serve", "handle":
		return true
	case "shadow":
		return len(rest) > 0 && rest[0] == "run"
	}
	return false
}

// readsConfigEarly reports whether a command's telemetry depends on the configuration, which is
// then read before telemetry is set up. lanes validate takes a file, not the configuration.
func readsConfigEarly(cmd string, rest []string) bool {
	return cmd != "lanes" || len(rest) == 0 || rest[0] != "validate"
}

// telemetrySettings reads the configuration's telemetry block before telemetry is set up, since
// where ynf writes depends on it. A configuration that cannot be read or is invalid changes
// nothing here: the command reports it as it always did.
func (a *app) telemetrySettings(cmd string, rest []string) telemetry.Settings {
	a.job = isJob(cmd, rest)
	if !readsConfigEarly(cmd, rest) || a.loadConfig() != nil {
		return telemetry.Settings{}
	}
	ts, err := a.cfg.TelemetrySettings(os.Getenv)
	if err != nil || ts.Root == "" {
		return telemetry.Settings{}
	}
	a.tsettings = ts
	// The layout exists before ynf writes into factory/. A root that cannot be made is reported
	// once there is a log to report it in, and costs telemetry, nothing else.
	a.spool, a.spoolErr = spool.New(ts.Root, ts.Quota, nil)
	return telemetry.Settings{Root: ts.Root, Collector: a.job && ts.Collector.Enabled}
}

// hostVolumes is the host's way of making a run's spool folder a size-limited volume of its own;
// tests replace it, since mounting is the host's, not the test's.
var hostVolumes = spool.HostVolumes

// startJob is the start of a factory job, once the engine has its logger: it hands the engine the
// spool, and starts ynr serve when the configuration enables the collector. It never fails the
// job: ynr missing or not starting is logged, and the job goes on.
func (a *app) startJob(log *slog.Logger) {
	if a.tsettings.Root == "" {
		return
	}
	if a.spoolErr != nil {
		log.Warn("the spool root cannot be used: telemetry written there is lost, and runs have no spool folder", "root", a.tsettings.Root, "err", a.spoolErr)
		return
	}
	a.spool.Log = log
	a.spool.Volumes = hostVolumes() // a hard quota per run where the host allows one
	a.eng.Spool = a.spool
	if !a.job || !a.tsettings.Collector.Enabled {
		return
	}
	a.eng.SpoolCollector = true
	a.serve = spool.StartServe(a.tsettings.Collector, spool.YnrBin(), a.tsettings.Root, os.Environ(), log)
	a.spool.Shipping = a.serve.Running
}

// endJob is the end of a factory job, after ynf's own telemetry is flushed and closed: ynr serve is
// stopped with SIGTERM and given its archive time to ship what is left, then any spool file still
// in the spool goes into the run capture (ADR-010).
func (a *app) endJob() {
	if a.spool == nil || a.eng == nil {
		return
	}
	dir := filepath.Join(a.cfg.WorkPath(), "spool-capture", time.Now().UTC().Format("20060102T150405Z"))
	if !a.job || !a.tsettings.Collector.Enabled {
		a.spool.Close(dir) // no run volume outlives the command
		return
	}
	a.serve.Stop()
	a.spool.Sweep(dir)
}

// spoolCheck is doctor's line for the spool root: empty when none is configured.
func (a *app) spoolCheck() (ok bool, detail string) {
	ts := a.tsettings
	if ts.Root == "" {
		return true, ""
	}
	if a.spoolErr != nil {
		return false, a.spoolErr.Error()
	}
	detail = ts.Root + ": factory/, runs/<run id>/ and manifests/"
	if ts.Collector.Enabled {
		detail += "; the collector " + ts.Collector.ID + " ships upstream for a factory job"
	} else {
		detail += "; the collector is off"
	}
	return true, detail
}

// ynrCheck is doctor's line for ynr: an optional tool, which presence starts nothing for. With the
// collector enabled and ynr missing, it says what will happen: the job runs on without it.
func ynrCheck(ctx context.Context, cfg *config.Config) (ok bool, detail string) {
	d := spool.Detect(ctx, spool.YnrBin())
	enabled := false
	if cfg != nil {
		if ts, err := cfg.TelemetrySettings(os.Getenv); err == nil {
			enabled = ts.Collector.Enabled
		}
	}
	switch {
	case d.Found && enabled:
		return true, d.String() + ", capabilities " + d.Capabilities + ": started as ynr serve for a factory job, as telemetry.collector says"
	case d.Found:
		return true, d.String() + ", capabilities " + d.Capabilities + ": detected, and nothing starts it: telemetry.collector is off"
	case enabled:
		return false, "not found or not working (" + d.Detail + "): telemetry.collector is on, so a factory job logs this and runs on without a collector"
	}
	return false, "not found or not working (" + d.Detail + ")"
}
