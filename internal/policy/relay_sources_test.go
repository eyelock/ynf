package policy_test

import (
	"testing"

	"github.com/eyelock/ynf/internal/policy"
)

// TestLanesShowSaysWhereTheRelaySettingCameFrom: the relay is set by a repository over a lane its
// configuration repository defines, and `lanes show` says which layer set it.
func TestLanesShowSaysWhereTheRelaySettingCameFrom(t *testing.T) {
	config := []byte("version: 1\nlanes:\n  x:\n    kind: originate\n    intake: [{github.search: q, every: 5m}]\n    run: {runner: ynh, ynh: {harness: .}}\n    when: {converged: open_pr}\n")
	repo := []byte("version: 1\nlanes:\n  x:\n    run: {ynh: {telemetry_relay: true}}\n")
	src, err := policy.LaneSources(config, repo, "x")
	if err != nil {
		t.Fatal(err)
	}
	if src["run.ynh.telemetry_relay"] != "repo" || src["run.ynh.harness"] != "config" {
		t.Errorf("sources: %v", src)
	}
	doc, err := policy.MergeLanes(config, repo)
	if err != nil {
		t.Fatal(err)
	}
	f, err := policy.Load(doc)
	if err != nil || !f.Lanes["x"].Run.Ynh.TelemetryRelay {
		t.Fatalf("merged: %v", err)
	}
}
