package telemetry

import (
	"os"
	"path/filepath"
	"testing"
)

func TestChooseOrder(t *testing.T) {
	state := t.TempDir()
	laptop := filepath.Join(state, "ynr", "spool", "local")
	spool := t.TempDir()

	// Neither the operator's endpoint nor a spool: the no-op providers.
	if c := Choose([]string{"XDG_STATE_HOME=" + state}); c.Mode != ModeNone {
		t.Errorf("nothing set: %+v", c)
	}
	// The laptop default counts only once its folder exists.
	if err := os.MkdirAll(laptop, 0o755); err != nil {
		t.Fatal(err)
	}
	if c := Choose([]string{"XDG_STATE_HOME=" + state}); c.Mode != ModeSpool || c.Dir != laptop {
		t.Errorf("laptop default: %+v, want the spool at %s itself, not a subfolder", c, laptop)
	}
	// YNR_SPOOL names the folder, which need not exist yet: it is written into as named.
	named := filepath.Join(spool, "factory")
	if c := Choose([]string{"XDG_STATE_HOME=" + state, "YNR_SPOOL=" + named}); c.Mode != ModeSpool || c.Dir != named {
		t.Errorf("YNR_SPOOL: %+v", c)
	}
	// The operator's OTEL_EXPORTER_OTLP_* wins over both.
	for _, v := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT=http://collector:4318", "OTEL_EXPORTER_OTLP_TRACES_HEADERS=a=b"} {
		if c := Choose([]string{"XDG_STATE_HOME=" + state, "YNR_SPOOL=" + named, v}); c.Mode != ModeOTLP {
			t.Errorf("%s: %+v", v, c)
		}
	}
	// An empty variable is not set.
	if c := Choose([]string{"OTEL_EXPORTER_OTLP_ENDPOINT=", "YNR_SPOOL=" + named}); c.Mode != ModeSpool {
		t.Errorf("empty endpoint: %+v", c)
	}
	// The SDK's own off switch.
	if c := Choose([]string{"OTEL_SDK_DISABLED=true", "YNR_SPOOL=" + named}); c.Mode != ModeNone {
		t.Errorf("OTEL_SDK_DISABLED: %+v", c)
	}
}

func TestLaptopSpoolFollowsXDG(t *testing.T) {
	if got := LaptopSpool(map[string]string{"XDG_STATE_HOME": "/state"}); got != "/state/ynr/spool/local" {
		t.Errorf("XDG_STATE_HOME: %s", got)
	}
	// A relative XDG_STATE_HOME is ignored, as the specification says.
	if got := LaptopSpool(map[string]string{"XDG_STATE_HOME": "state", "HOME": "/home/me"}); got != "/home/me/.local/state/ynr/spool/local" {
		t.Errorf("relative: %s", got)
	}
	if got := LaptopSpool(map[string]string{"HOME": "/home/me"}); got != "/home/me/.local/state/ynr/spool/local" {
		t.Errorf("default: %s", got)
	}
}
