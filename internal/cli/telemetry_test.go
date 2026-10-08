package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/cli"
	"github.com/eyelock/ynf/internal/telemetry"
	"github.com/eyelock/ynf/internal/telemetry/spooltest"
)

var timeField = regexp.MustCompile(`"time":"[^"]*"`)

// TestTelemetryDoesNotChangeWhatPeopleSee: stderr, --log-file and stdout, and the exit code, are the
// same with a spool as without one; the spool holds the step's trace, its intake mirrored once and
// the bridged logs.
func TestTelemetryDoesNotChangeWhatPeopleSee(t *testing.T) {
	run := func(spool string) (code int, stdout, stderr, logfile string) {
		e := setup(t)
		if spool != "" {
			t.Setenv("YNR_SPOOL", spool)
		}
		logPath := filepath.Join(e.dir, "ynf.log")
		code, stdout, stderr = e.run("--log-file", logPath, "--log-format", "json", "sweep")
		b, _ := os.ReadFile(logPath)
		strip := func(s string) string {
			s = timeField.ReplaceAllString(s, "")
			return strings.ReplaceAll(s, e.dir, "<dir>")
		}
		return code, strip(stdout), strip(stderr), strip(string(b))
	}
	spool := t.TempDir()
	c0, o0, e0, l0 := run("")
	c1, o1, e1, l1 := run(spool)
	if c0 != 0 || c1 != c0 || o0 != o1 || e0 != e1 || l0 != l1 {
		t.Fatalf("output changed with telemetry on:\nexit %d/%d\nstdout %q\n  vs %q\nstderr %q\n  vs %q", c0, c1, o0, o1, e0, e1)
	}
	if !strings.Contains(e0, `"msg":"decided"`) {
		t.Fatalf("expected the usual log lines: %s", e0)
	}
	d := spooltest.Read(t, spool)
	if len(d.Named(telemetry.SpanStep)) != 1 || len(d.Events(telemetry.EventIntakeReceived)) != 1 || len(d.Named(telemetry.SpanIntake)) != 1 {
		t.Fatalf("spans %d, intake events %d", len(d.Named(telemetry.SpanStep)), len(d.Events(telemetry.EventIntakeReceived)))
	}
	var bridged bool
	for _, l := range d.Logs {
		if l.Event == "" && l.Body == "decided" {
			bridged = true
		}
	}
	if !bridged {
		t.Errorf("slog was not bridged: %+v", d.Logs)
	}
	if got := d.Spans[0].Resource.Str("service.name"); got != "ynf" {
		t.Errorf("service.name %q", got)
	}
}

// TestAFailingCommandKeepsItsExitCodeWithTelemetryOn: telemetry never changes an exit code.
func TestAFailingCommandKeepsItsExitCodeWithTelemetryOn(t *testing.T) {
	e := setup(t)
	off, _, _ := e.run("replay", "no/such#1")
	t.Setenv("YNR_SPOOL", t.TempDir())
	if code, _, _ := e.run("--log-format", "yaml", "items", "ls"); code != cli.ExitUsage {
		t.Fatalf("bad format: %d", code)
	}
	if code, _, _ := e.run("replay", "no/such#1"); code != off {
		t.Fatalf("exit %d with telemetry on, %d off", code, off)
	}
}

func TestTelemetryRegistryCommand(t *testing.T) {
	e := setup(t)
	code, out, stderr := e.run("telemetry", "registry", "--format", "json")
	if code != 0 {
		t.Fatalf("%d %s", code, stderr)
	}
	var r struct {
		Tool    string
		Version string
		Spans   []struct{ Name string }
		Events  []struct{ Name string }
		Metrics []struct{ Name string }
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if r.Tool != "ynf" || r.Version == "" || len(r.Spans) == 0 || len(r.Events) == 0 || len(r.Metrics) == 0 {
		t.Fatalf("%+v", r)
	}
	if !strings.Contains(out, `"ynf.lane.harness"`) || !strings.Contains(out, `"ynf.lane.focus"`) || !strings.Contains(out, `"ynf.intake.received"`) {
		t.Error("the factory attributes ynr stamps on ynf's behalf, and the mirror, are in the registry")
	}
	if code, text, _ := e.run("telemetry", "registry"); code != 0 || !strings.Contains(text, "ynf.step") {
		t.Errorf("text: %d %s", code, text)
	}
	if code, _, _ := e.run("--format", "json", "telemetry", "registry"); code != 0 {
		t.Errorf("global --format: %d", code)
	}
	for _, bad := range [][]string{{"telemetry"}, {"telemetry", "nope"}, {"telemetry", "registry", "--format", "yaml"}, {"telemetry", "registry", "--bogus"}} {
		if code, _, _ := e.run(bad...); code != cli.ExitUsage {
			t.Errorf("%v: %d", bad, code)
		}
	}
}
