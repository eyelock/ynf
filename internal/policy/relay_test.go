package policy_test

import (
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/policy"
)

func relayLane(run string) []byte {
	return []byte("version: 1\nlanes:\n  x:\n    kind: originate\n    intake: [{github.search: q, every: 5m}]\n    run: " + run + "\n    when: {converged: open_pr}\n")
}

// TestTheVendorRelayIsALaneSetting: run.ynh.telemetry_relay turns on ynh's relay for the lane's
// runs, off by default; a lane that runs the command runner refuses it at load.
func TestTheVendorRelayIsALaneSetting(t *testing.T) {
	f, err := policy.Load(relayLane("{runner: ynh, ynh: {harness: ., telemetry_relay: true}}"))
	if err != nil || !f.Lanes["x"].Run.Ynh.TelemetryRelay {
		t.Fatalf("on: %v", err)
	}
	f, err = policy.Load(relayLane("{runner: ynh, ynh: {harness: .}}"))
	if err != nil || f.Lanes["x"].Run.Ynh.TelemetryRelay {
		t.Fatalf("off by default: %v", err)
	}
	// Setting it changes the lane's policy hash, which every decision records; leaving it out does not.
	a, _ := policy.Load(relayLane("{runner: ynh, ynh: {harness: .}}"))
	b, _ := policy.Load(relayLane("{runner: ynh, ynh: {harness: ., telemetry_relay: false}}"))
	c, _ := policy.Load(relayLane("{runner: ynh, ynh: {harness: ., telemetry_relay: true}}"))
	if a.Lanes["x"].Hash() != b.Lanes["x"].Hash() || a.Lanes["x"].Hash() == c.Lanes["x"].Hash() {
		t.Error("the relay setting and the policy hash")
	}
	// A lane that may fall back to its command keeps the setting for when ynh runs.
	if _, err := policy.Load(relayLane("{ynh: {harness: ., telemetry_relay: true}, command: {argv: [echo]}}")); err != nil {
		t.Errorf("no runner named: %v", err)
	}
}

func TestACommandLaneRefusesTheRelay(t *testing.T) {
	for name, run := range map[string]string{
		"the command runner":     "{runner: command, command: {argv: [echo]}, ynh: {harness: ., telemetry_relay: true}}",
		"not a boolean":          "{runner: ynh, ynh: {harness: ., telemetry_relay: 1}}",
		"a command lane's block": "{runner: command, command: {argv: [echo], telemetry_relay: true}}",
	} {
		_, err := policy.Load(relayLane(run))
		if err == nil {
			t.Errorf("%s: loaded", name)
			continue
		}
		if name == "the command runner" && !strings.Contains(err.Error(), "telemetry_relay") {
			t.Errorf("%s: the error does not name the setting: %v", name, err)
		}
	}
}
