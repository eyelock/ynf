package telemetry

import (
	"os"
	"path/filepath"
	"strings"
)

// Mode is where telemetry goes.
type Mode string

// The modes, in the order Choose tries them (ynr ADR-004).
const (
	ModeOTLP  Mode = "otlp"  // the operator's OTEL_EXPORTER_OTLP_*, over the network
	ModeSpool Mode = "spool" // OTLP JSON lines in a spool folder, which ynr serve reads
	ModeNone  Mode = "none"  // the SDK's no-op providers: nothing is written
)

// Choice is where telemetry goes, and for a spool, which folder.
type Choice struct {
	Mode Mode
	Dir  string
}

// Choose picks where to write, in the contract's order (ynr ADR-006, rule 1): the operator's
// OTEL_EXPORTER_OTLP_* if any is set; otherwise the spool, if YNR_SPOOL names a folder or the
// laptop default, $XDG_STATE_HOME/ynr/spool/local (XDG's default is ~/.local/state), exists;
// otherwise nothing. When ynf writes to the spool itself it writes into the folder named, not a
// subfolder of it. environ is the environment, as os.Environ gives it.
func Choose(environ []string) Choice {
	env := map[string]string{}
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	if strings.EqualFold(env["OTEL_SDK_DISABLED"], "true") {
		return Choice{Mode: ModeNone}
	}
	for k, v := range env {
		if strings.HasPrefix(k, "OTEL_EXPORTER_OTLP_") && v != "" {
			return Choice{Mode: ModeOTLP}
		}
	}
	if dir := env["YNR_SPOOL"]; dir != "" {
		return Choice{Mode: ModeSpool, Dir: dir}
	}
	if dir := LaptopSpool(env); dir != "" {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return Choice{Mode: ModeSpool, Dir: dir}
		}
	}
	return Choice{Mode: ModeNone}
}

// LaptopSpool is the laptop's default spool folder: $XDG_STATE_HOME/ynr/spool/local, with
// ~/.local/state when XDG_STATE_HOME is not set to an absolute path. It is "" when there is no
// home to resolve.
func LaptopSpool(env map[string]string) string {
	state := env["XDG_STATE_HOME"]
	if !filepath.IsAbs(state) {
		home := env["HOME"]
		if home == "" {
			var err error
			if home, err = os.UserHomeDir(); err != nil {
				return ""
			}
		}
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "ynr", "spool", "local")
}
