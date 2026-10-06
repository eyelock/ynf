package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/cli"
	"github.com/eyelock/ynf/internal/telemetry"
	"github.com/eyelock/ynf/internal/telemetry/spooltest"
)

// fakeYnrScript is a ynr that records how it was started and, on SIGTERM, that it was told to stop,
// as ynr serve does. A script starts in a few milliseconds, so it has installed its trap before a
// sweep of nothing is over. FAKE_YNR_MODE is info, fail or serve.
const fakeYnrScript = `#!/bin/sh
trap 'echo terminated > "$FAKE_YNR_TERM"; exit 0' TERM
case "$1" in
info) echo '{"version":"0.1.0","build":"slim","capabilities":"0.1.0"}'; exit 0 ;;
esac
if [ "$FAKE_YNR_MODE" = fail ]; then echo "ynr: --upstream is required" >&2; exit 3; fi
[ -z "$FAKE_YNR_ARGS" ] || { printf '%s\n' "$@" > "$FAKE_YNR_ARGS.tmp" && mv "$FAKE_YNR_ARGS.tmp" "$FAKE_YNR_ARGS"; }
while :; do sleep 0.02; done
`

// withTelemetry rewrites the test's config with a telemetry block, and points ynr at the fake.
// fakeYnr puts a fake ynr first on PATH, which is the only place ynf looks for it.
func fakeYnr(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ynr"), []byte(fakeYnrScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// noYnr takes every folder holding a ynr off PATH, so ynf finds none.
func noYnr(t *testing.T) {
	t.Helper()
	var keep []string
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if _, err := os.Stat(filepath.Join(d, "ynr")); err != nil {
			keep = append(keep, d)
		}
	}
	t.Setenv("PATH", strings.Join(keep, string(os.PathListSeparator)))
}

func (e env) withTelemetry(block string) (spool string) {
	e.t.Helper()
	spool = filepath.Join(e.dir, "spool")
	body := "version: 1\nrepos: [o/r]\ntelemetry:\n  spool: " + spool + "\n" + block
	if err := os.WriteFile(e.cfg, []byte(body), 0o644); err != nil {
		e.t.Fatal(err)
	}
	fakeYnr(e.t)
	return spool
}

// runUntil runs the command, again when the fake ynr was stopped before it had installed its signal
// handler (a sweep of nothing ends within milliseconds of starting it), until ready.
func (e env) runUntil(ready func(stderr string) bool, args ...string) (code int, stdout, stderr string) {
	for range 20 {
		if code, stdout, stderr = e.run(args...); code != 0 || ready(stderr) {
			return code, stdout, stderr
		}
	}
	return code, stdout, stderr
}

// written is ready once the file has what the fake wrote in it.
func written(path string) func(string) bool {
	return func(string) bool { b, _ := os.ReadFile(path); return len(b) > 0 }
}

func logged(s string) func(string) bool {
	return func(stderr string) bool { return strings.Contains(stderr, s) }
}

const collectorOn = "  collector:\n    enabled: true\n    id: pool-1\n    instance: job-7\n    upstream: http://central:4318\n    archive: 5s\n"

// TestAFactoryJobStartsYnrServeAndStopsItAtTheEnd: with the collector on, a sweep starts ynr serve
// with the flags the configuration says and stops it with SIGTERM when the job ends, and ynf wrote
// its own telemetry to factory/.
func TestAFactoryJobStartsYnrServeAndStopsItAtTheEnd(t *testing.T) {
	e := setup(t)
	spool := e.withTelemetry(collectorOn)
	args, term := filepath.Join(e.dir, "args"), filepath.Join(e.dir, "term")
	t.Setenv("FAKE_YNR_MODE", "serve")
	t.Setenv("FAKE_YNR_ARGS", args)
	t.Setenv("FAKE_YNR_TERM", term)
	code, _, stderr := e.runUntil(func(s string) bool { return written(term)(s) && written(args)(s) }, "sweep")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	b, err := os.ReadFile(args)
	if err != nil {
		t.Fatalf("ynr serve was not started: %v\n%s", err, stderr)
	}
	want := strings.Join([]string{"serve", "--spool", spool, "--collector-id", "pool-1", "--collector-instance", "job-7", "--upstream", "http://central:4318"}, "\n") + "\n"
	if string(b) != want {
		t.Errorf("ynr serve was started as %q, want %q", b, want)
	}
	if b, _ := os.ReadFile(term); strings.TrimSpace(string(b)) != "terminated" {
		t.Error("ynr serve was not stopped with SIGTERM at the job's end")
	}
	if !strings.Contains(stderr, "collector started") || !strings.Contains(stderr, "collector stopped") {
		t.Errorf("the job's log does not say so:\n%s", stderr)
	}
	d := spooltest.Read(t, filepath.Join(spool, "factory"))
	if len(d.Named(telemetry.SpanStep)) != 1 {
		t.Errorf("ynf's own telemetry is not in factory/: %d steps", len(d.Named(telemetry.SpanStep)))
	}
	for _, dir := range []string{"manifests", "runs"} {
		if fi, err := os.Stat(filepath.Join(spool, dir)); err != nil || !fi.IsDir() {
			t.Errorf("%s/: %v", dir, err)
		}
	}
}

// TestOnlyAFactoryJobStartsYnrServe: a command that only reads, or a person's own start, does not
// start it, however the configuration is set (ADR-012: configuration decides what runs).
func TestOnlyAFactoryJobStartsYnrServe(t *testing.T) {
	e := setup(t)
	e.withTelemetry(collectorOn)
	args := filepath.Join(e.dir, "args")
	t.Setenv("FAKE_YNR_MODE", "serve")
	t.Setenv("FAKE_YNR_ARGS", args)
	for _, cmd := range [][]string{{"items", "ls"}, {"lanes", "show"}, {"stats"}} {
		if code, _, stderr := e.run(cmd...); code != 0 {
			t.Fatalf("%v: %d %s", cmd, code, stderr)
		}
		if _, err := os.Stat(args); err == nil {
			t.Fatalf("%v started ynr serve", cmd)
		}
	}
}

// TestFindingYnrStartsNothing: ynr is on the path, the collector is off, and nothing starts it.
func TestFindingYnrStartsNothing(t *testing.T) {
	e := setup(t)
	e.withTelemetry("")
	args := filepath.Join(e.dir, "args")
	t.Setenv("FAKE_YNR_MODE", "serve")
	t.Setenv("FAKE_YNR_ARGS", args)
	if code, _, stderr := e.run("sweep"); code != 0 {
		t.Fatalf("%d %s", code, stderr)
	}
	if _, err := os.Stat(args); err == nil {
		t.Fatal("ynr serve was started with the collector off")
	}
	// And with no telemetry block at all.
	e2 := setup(t)
	fakeYnr(t)
	if code, _, stderr := e2.run("sweep"); code != 0 {
		t.Fatalf("%d %s", code, stderr)
	}
	if _, err := os.Stat(args); err == nil {
		t.Fatal("ynr serve was started with no configuration")
	}
}

// TestAMissingOrFailingYnrNeverFailsTheJob: the job logs it plainly and runs on, with its
// telemetry left in the spool.
func TestAMissingOrFailingYnrNeverFailsTheJob(t *testing.T) {
	e := setup(t)
	spool := e.withTelemetry(collectorOn)
	noYnr(t)
	code, _, stderr := e.run("sweep")
	if code != 0 {
		t.Fatalf("a missing ynr failed the job: %d %s", code, stderr)
	}
	if !strings.Contains(stderr, "ynr was not found") || !strings.Contains(stderr, "runs without it") {
		t.Errorf("the log does not say so:\n%s", stderr)
	}
	if d := spooltest.Read(t, filepath.Join(spool, "factory")); len(d.Named(telemetry.SpanStep)) != 1 {
		t.Error("ynf's own telemetry was lost with ynr missing")
	}

	e = setup(t)
	e.withTelemetry(collectorOn)
	t.Setenv("FAKE_YNR_MODE", "fail")
	code, _, stderr = e.runUntil(logged("--upstream is required"), "sweep")
	if code != 0 {
		t.Fatalf("a failing ynr failed the job: %d %s", code, stderr)
	}
	if !strings.Contains(stderr, "--upstream is required") {
		t.Errorf("ynr's own message is not in the job's log:\n%s", stderr)
	}
}

// TestTheCollectorOnMakesTheSpoolWinOverTheOperatorsEndpoint: ynf writes to factory/ whatever
// OTEL_EXPORTER_OTLP_* says, and the endpoint becomes ynr serve's upstream; with the collector off
// the operator's endpoint is honoured as before.
func TestTheCollectorOnMakesTheSpoolWinOverTheOperatorsEndpoint(t *testing.T) {
	e := setup(t)
	spool := e.withTelemetry("  collector:\n    enabled: true\n    id: pool-1\n")
	args := filepath.Join(e.dir, "args")
	t.Setenv("FAKE_YNR_MODE", "serve")
	t.Setenv("FAKE_YNR_ARGS", args)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://operator:4318")
	if code, _, stderr := e.runUntil(written(args), "sweep"); code != 0 {
		t.Fatalf("%d %s", code, stderr)
	}
	b, _ := os.ReadFile(args)
	if !strings.Contains(string(b), "--upstream\nhttp://operator:4318\n") {
		t.Errorf("the operator's endpoint is not the upstream: %q", b)
	}
	if d := spooltest.Read(t, filepath.Join(spool, "factory")); len(d.Named(telemetry.SpanStep)) != 1 {
		t.Error("ynf did not write to the spool with the collector on")
	}
}

func TestACollectorWithNoUpstreamFailsAtConfigLoad(t *testing.T) {
	e := setup(t)
	e.withTelemetry("  collector:\n    enabled: true\n    id: pool-1\n")
	code, _, stderr := e.run("sweep")
	if code != cli.ExitPolicy || !strings.Contains(stderr, "no upstream") {
		t.Fatalf("%d %s", code, stderr)
	}
}

// TestWhatYnrServeDidNotShipIsSweptIntoTheRunCapture: at the job's end, a spool file still in the
// spool, in runs/ or factory/, is copied into the run capture (ADR-010).
func TestWhatYnrServeDidNotShipIsSweptIntoTheRunCapture(t *testing.T) {
	e := setup(t)
	spool := e.withTelemetry(collectorOn)
	t.Setenv("FAKE_YNR_MODE", "serve") // a fake ships nothing
	left := filepath.Join(spool, "runs", "01OLDRUN")
	if err := os.MkdirAll(left, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(left, "ynh-1-0.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := e.run("sweep"); code != 0 {
		t.Fatalf("%d %s", code, stderr)
	}
	capture, _ := filepath.Glob(filepath.Join(e.dir, "work", "spool-capture", "*"))
	if len(capture) != 1 {
		t.Fatalf("capture folders: %v", capture)
	}
	for _, p := range []string{filepath.Join("runs", "01OLDRUN", "ynh-1-0.jsonl")} {
		if _, err := os.Stat(filepath.Join(capture[0], p)); err != nil {
			t.Errorf("%s is not in the capture: %v", p, err)
		}
	}
	if m, _ := filepath.Glob(filepath.Join(capture[0], "factory", "ynf-*.jsonl")); len(m) == 0 {
		t.Error("ynf's own unshipped files are not in the capture")
	}
}

// TestDoctorSaysWhatItFoundOfYnr: an optional tool, detected, and starting nothing; missing, it
// says what a job will do.
func TestDoctorSaysWhatItFoundOfYnr(t *testing.T) {
	e := setup(t)
	e.withTelemetry("")
	_, out, _ := e.run("doctor")
	if !strings.Contains(out, "ynr ") || !strings.Contains(out, "ynr 0.1.0 (slim)") || !strings.Contains(out, "nothing starts it") || !strings.Contains(out, "telemetry") {
		t.Errorf("found, collector off:\n%s", out)
	}

	e = setup(t)
	e.withTelemetry(collectorOn)
	_, out, _ = e.run("doctor")
	if !strings.Contains(out, "started as ynr serve for a factory job") || !strings.Contains(out, "the collector pool-1 ships upstream") {
		t.Errorf("found, collector on:\n%s", out)
	}

	e = setup(t)
	e.withTelemetry(collectorOn)
	noYnr(t)
	code, out, _ := e.run("doctor")
	if !strings.Contains(out, "runs on without a collector") {
		t.Errorf("missing, collector on:\n%s", out)
	}
	_ = code
}

// TestAnUnusableSpoolRootIsLoggedAndTheJobGoesOn.
func TestAnUnusableSpoolRootIsLoggedAndTheJobGoesOn(t *testing.T) {
	e := setup(t)
	blocker := filepath.Join(e.dir, "blocker")
	if err := os.WriteFile(blocker, []byte("a file"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := "version: 1\nrepos: [o/r]\ntelemetry:\n  spool: " + filepath.Join(blocker, "spool") + "\n"
	if err := os.WriteFile(e.cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := e.run("sweep")
	if code != 0 || !strings.Contains(stderr, "the spool root cannot be used") {
		t.Fatalf("%d\n%s", code, stderr)
	}
}
