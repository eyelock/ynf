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
		{both, missing, "command", true, "command (ynh not found)", ""},
		{both, runner.Detection{Version: "0.8.0", Capabilities: "0.8.0", Detail: "ynh has capabilities 0.8.0; ynf needs 0.9.0"}, "command", true, "command (ynh 0.8.0 is too old: capabilities 0.8.0, needs 0.9.0)", ""},
		{both, runner.Detection{Detail: "ynh version --format json gave no capabilities"}, "command", true, "command (ynh gave no capabilities)", ""},
		{both, runner.Detection{Detail: "ynh: exec: \"ynh\": executable file not found in $PATH"}, "command", true, "command (ynh not found)", ""},
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

// fakeYnh puts a ynh that prints out first on PATH, the only place ynf looks for it.
func fakeYnh(t *testing.T, out string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ynh"), []byte("#!/bin/sh\necho '"+out+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// noYnh takes every folder holding a ynh off PATH.
func noYnh(t *testing.T) {
	t.Helper()
	var keep []string
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if _, err := os.Stat(filepath.Join(d, "ynh")); err != nil {
			keep = append(keep, d)
		}
	}
	t.Setenv("PATH", strings.Join(keep, string(os.PathListSeparator)))
}

func TestDetectYnh(t *testing.T) {
	ctx := context.Background()
	fakeYnh(t, `{"version":"0.10.0","capabilities":"0.10.0"}`)
	if d := runner.DetectYnh(ctx); !d.Found || d.Version != "0.10.0" || d.String() != "ynh 0.10.0" {
		t.Fatalf("%+v", d)
	}
	fakeYnh(t, `{"capabilities":"0.9.0"}`)
	if d := runner.DetectYnh(ctx); !d.Found || d.Version != "0.9.0" {
		t.Fatalf("a ynh that reports only capabilities: %+v", d)
	}
	fakeYnh(t, `{"version":"0.8.0","capabilities":"0.8.0"}`)
	if d := runner.DetectYnh(ctx); d.Found || !strings.Contains(d.Detail, "needs 0.9.0") {
		t.Fatalf("too old: %+v", d)
	}
	fakeYnh(t, `not json`)
	if d := runner.DetectYnh(ctx); d.Found || d.Detail == "" {
		t.Fatalf("garbage: %+v", d)
	}
	noYnh(t)
	if d := runner.DetectYnh(ctx); d.Found || d.String() != "ynh not found" || d.Detail == "" {
		t.Fatalf("missing: %+v", d)
	}
}

func TestDetectedYnhAsksOnce(t *testing.T) {
	fakeYnh(t, `{"version":"1.0.0","capabilities":"1.0.0"}`)
	first := runner.DetectedYnh(context.Background())
	noYnh(t)
	if again := runner.DetectedYnh(context.Background()); again != first {
		t.Fatalf("asked twice: %+v then %+v", first, again)
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
