package telemetry

import (
	"path/filepath"
	"testing"
)

// TestChooseWithTheSpoolRoot: a configured root sends ynf's own telemetry to its factory/ folder;
// with the collector on the spool wins over the operator's endpoint, and with it off the operator's
// endpoint still wins (ynr ADR-004, ADR-006).
func TestChooseWithTheSpoolRoot(t *testing.T) {
	root := t.TempDir()
	factory := filepath.Join(root, "factory")
	otlp := "OTEL_EXPORTER_OTLP_ENDPOINT=http://collector:4318"
	named := "YNR_SPOOL=" + t.TempDir()
	state := "XDG_STATE_HOME=" + t.TempDir()

	// A root alone: ynf's own folder, ahead of YNR_SPOOL and the laptop default.
	if c := ChooseWith([]string{state, named}, Settings{Root: root}); c.Mode != ModeSpool || c.Dir != factory {
		t.Errorf("root: %+v", c)
	}
	// The collector off: the operator's endpoint wins, as the contract orders.
	if c := ChooseWith([]string{state, otlp}, Settings{Root: root}); c.Mode != ModeOTLP {
		t.Errorf("root, operator's endpoint, collector off: %+v", c)
	}
	// The collector on: the spool wins, and ynr serve ships to the endpoint.
	if c := ChooseWith([]string{state, otlp}, Settings{Root: root, Collector: true}); c.Mode != ModeSpool || c.Dir != factory {
		t.Errorf("root, operator's endpoint, collector on: %+v", c)
	}
	// The SDK's off switch still wins.
	if c := ChooseWith([]string{"OTEL_SDK_DISABLED=true"}, Settings{Root: root, Collector: true}); c.Mode != ModeNone {
		t.Errorf("OTEL_SDK_DISABLED: %+v", c)
	}
	// No root: slice one's behaviour, whatever the collector says.
	if c := ChooseWith([]string{state, otlp}, Settings{Collector: true}); c.Mode != ModeOTLP {
		t.Errorf("no root: %+v", c)
	}
	if c := ChooseWith([]string{state}, Settings{}); c.Mode != ModeNone {
		t.Errorf("nothing: %+v", c)
	}
}
