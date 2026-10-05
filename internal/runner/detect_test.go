package runner_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/policy"
	"github.com/eyelock/ynf/internal/runner"
)

func TestResolve(t *testing.T) {
	both := policy.Lane{Name: "both", Run: policy.Run{Ynh: &policy.Ynh{Harness: "."}, Command: &policy.Command{Argv: []string{"x"}}}}
	ynhOnly := policy.Lane{Name: "ynh", Run: policy.Run{Ynh: &policy.Ynh{Harness: "."}}}
	cmdOnly := policy.Lane{Name: "cmd", Run: policy.Run{Command: &policy.Command{Argv: []string{"x"}}}}
	named := policy.Lane{Name: "named", Run: policy.Run{Runner: "ynh", Ynh: &policy.Ynh{Harness: "."}, Command: &policy.Command{Argv: []string{"x"}}}}
	found := runner.Detection{Found: true, Version: "0.10.0"}
	missing := runner.Detection{}
	for _, c := range []struct {
		lane     policy.Lane
		det      runner.Detection
		runner   string
		detected bool
		note     string
		err      string
	}{
		{both, found, "ynh", true, "ynh (detected 0.10.0)", ""},
		{both, missing, "command", true, "command (ynh was not found)", ""},
		{both, runner.Detection{Detail: "capabilities 0.8.0"}, "command", true, "ynh was not detected (capabilities 0.8.0)", ""},
		{cmdOnly, found, "command", true, "no ynh block", ""},
		{ynhOnly, found, "ynh", true, "detected", ""},
		{ynhOnly, missing, "", false, "", "no command block to fall back to"},
		{named, missing, "ynh", false, "", ""}, // named: Resolve never second-guesses it
	} {
		got, err := runner.Resolve(c.lane, c.det)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: %v", c.lane.Name, err)
			}
			continue
		}
		if err != nil || got.Runner.Name() != c.runner || got.Detected != c.detected || !strings.Contains(got.Note, c.note) {
			t.Errorf("%s: %+v %v", c.lane.Name, got, err)
		}
	}
	if _, err := runner.Resolve(policy.Lane{Name: "bad", Run: policy.Run{Runner: "make"}}, found); err == nil {
		t.Error("an unknown runner resolved")
	}
}

func fakeYnhBin(t *testing.T, out string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ynh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\necho '"+out+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDetectYnh(t *testing.T) {
	ctx := context.Background()
	t.Setenv("YNF_YNH_BIN", fakeYnhBin(t, `{"version":"0.10.0","capabilities":"0.10.0"}`))
	if d := runner.DetectYnh(ctx); !d.Found || d.Version != "0.10.0" || d.String() != "ynh 0.10.0" {
		t.Fatalf("%+v", d)
	}
	t.Setenv("YNF_YNH_BIN", fakeYnhBin(t, `{"capabilities":"0.9.0"}`))
	if d := runner.DetectYnh(ctx); !d.Found || d.Version != "0.9.0" {
		t.Fatalf("a ynh that reports only capabilities: %+v", d)
	}
	t.Setenv("YNF_YNH_BIN", fakeYnhBin(t, `{"version":"0.8.0","capabilities":"0.8.0"}`))
	if d := runner.DetectYnh(ctx); d.Found || !strings.Contains(d.Detail, "needs 0.9.0") {
		t.Fatalf("too old: %+v", d)
	}
	t.Setenv("YNF_YNH_BIN", fakeYnhBin(t, `not json`))
	if d := runner.DetectYnh(ctx); d.Found || d.Detail == "" {
		t.Fatalf("garbage: %+v", d)
	}
	t.Setenv("YNF_YNH_BIN", filepath.Join(t.TempDir(), "missing"))
	if d := runner.DetectYnh(ctx); d.Found || d.String() != "ynh not found" || d.Detail == "" {
		t.Fatalf("missing: %+v", d)
	}
}

func TestDetectedYnhAsksOnce(t *testing.T) {
	t.Setenv("YNF_YNH_BIN", fakeYnhBin(t, `{"version":"1.0.0","capabilities":"1.0.0"}`))
	first := runner.DetectedYnh(context.Background())
	t.Setenv("YNF_YNH_BIN", filepath.Join(t.TempDir(), "missing"))
	if again := runner.DetectedYnh(context.Background()); again != first {
		t.Fatalf("asked twice: %+v then %+v", first, again)
	}
}

func TestYnhBin(t *testing.T) {
	t.Setenv("YNF_YNH_BIN", "")
	if runner.YnhBin() != "ynh" {
		t.Fatal(runner.YnhBin())
	}
	t.Setenv("YNF_YNH_BIN", "/x/ynh")
	if runner.YnhBin() != "/x/ynh" {
		t.Fatal(runner.YnhBin())
	}
}

func TestAtLeast(t *testing.T) {
	for _, c := range []struct {
		have, want string
		ok         bool
	}{{"0.9.0", "0.9.0", true}, {"0.10.0", "0.9.0", true}, {"0.8.9", "0.9.0", false}, {"0", "0.9.0", false}, {"dev", "0.9.0", false}, {"1.0.0", "0.9", true}} {
		if runner.AtLeast(c.have, c.want) != c.ok {
			t.Errorf("%s >= %s: want %v", c.have, c.want, c.ok)
		}
	}
}
