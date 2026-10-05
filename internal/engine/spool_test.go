package engine_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eyelock/ynf/internal/executor"
	"github.com/eyelock/ynf/internal/item"
	"github.com/eyelock/ynf/internal/spool"
)

// spoolLane is a command lane whose run says what it was given, then formats the file as the
// other lanes do.
func spoolLane(script string) string {
	return "version: 1\ndefaults:\n  executor: process\nlanes:\n  sp:\n    kind: originate\n    intake: [{github.search: \"label:ynf:sp\", every: 5m}]\n" +
		"    run:\n      runner: command\n      command:\n        argv:\n          - sh\n          - -c\n          - |\n" +
		indent(script+"\ngofmt -w ./internal/format\n", "            ") +
		"    when: {converged: open_pr}\n"
}

func indent(s, p string) string {
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		b.WriteString(p + l + "\n")
	}
	return b.String()
}

type spoolRun struct {
	h    *harness
	sp   *spool.Spool
	logs *bytes.Buffer
}

func newSpoolRun(t *testing.T, script string, collector bool) *spoolRun {
	t.Helper()
	h := newHarness(t)
	logs := &bytes.Buffer{}
	h.e.Log = slog.New(slog.NewTextHandler(logs, nil))
	sp, err := spool.New(filepath.Join(t.TempDir(), "spool"), 1<<10, h.e.Log)
	if err != nil {
		t.Fatal(err)
	}
	sp.Interval = 10 * time.Millisecond
	h.e.Spool, h.e.SpoolCollector = sp, collector
	h.f.lanes = spoolLane(script)
	h.f.labels[1] = []string{"ynf:sp", "pkg:internal/format"}
	return &spoolRun{h, sp, logs}
}

func (r *spoolRun) sweep(t *testing.T) item.Item {
	t.Helper()
	if err := r.h.e.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	return r.h.item(t, 1)
}

func glob1(t *testing.T, pattern string) string {
	t.Helper()
	m, _ := filepath.Glob(pattern)
	if len(m) != 1 {
		t.Fatalf("%s matches %v, want one", pattern, m)
	}
	return m[0]
}

// TestARunGetsItsOwnFolderAndManifest: before the run starts, runs/<run id>/ and
// manifests/<run id>.json exist, the manifest names the run's lane, item and step as ynr reads them,
// and the run starts with YNR_SPOOL set to its own folder (ynr ADR-003, ADR-004).
func TestARunGetsItsOwnFolderAndManifest(t *testing.T) {
	t.Setenv("YNR_SPOOL", "/the/hosts/own")
	r := newSpoolRun(t, `env | grep -E '^(YNR_SPOOL|OTEL_)' | sort > {run_dir}/env.txt
ls "$YNR_SPOOL/../../manifests" > {run_dir}/manifests-at-start.txt`, false)
	if it := r.sweep(t); it.State != item.Proposed {
		t.Fatalf("%s %s", it.State, it.Reason)
	}
	run := r.h.item(t, 1).LastRun.ID
	envFile := glob1(t, filepath.Join(r.h.e.WorkDir, "steps", "*", "*", "run", "env.txt"))
	env, _ := os.ReadFile(envFile)
	if want := "YNR_SPOOL=" + filepath.Join(r.sp.Root, "runs", run); strings.TrimSpace(string(env)) != want {
		t.Errorf("the run's environment is %q, want %q", env, want)
	}
	// It existed before the run started.
	if b, _ := os.ReadFile(filepath.Join(filepath.Dir(envFile), "manifests-at-start.txt")); strings.TrimSpace(string(b)) != run+".json" {
		t.Errorf("the manifest was not there when the run started: %q", b)
	}
	b, err := os.ReadFile(filepath.Join(r.sp.Root, "manifests", run+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var m spool.Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m.Run != run || m.Item != "item/github.com/o/r/issues/1" || m.Step == "" || !strings.HasPrefix(m.Lane, "github.com/") || !strings.HasSuffix(m.Lane, "#sp") || !strings.HasPrefix(run, m.Step+"-") {
		t.Errorf("manifest: %+v", m)
	}
	if fi, err := os.Stat(filepath.Join(r.sp.Root, "runs", run)); err != nil || !fi.IsDir() {
		t.Errorf("run folder: %v", err)
	}
}

// TestTheCollectorOnStartsRunsWithoutTheOperatorsEndpoint: the operator's OTEL_EXPORTER_OTLP_* does
// not reach a run when the collector is on, because ynr serve ships to it; with it off the run
// inherits it as it did.
func TestTheCollectorOnStartsRunsWithoutTheOperatorsEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://operator:4318")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "k=v")
	for _, collector := range []bool{true, false} {
		r := newSpoolRun(t, `env | grep -E '^(YNR_SPOOL|OTEL_)' | sort > {run_dir}/env.txt`, collector)
		if it := r.sweep(t); it.State != item.Proposed {
			t.Fatalf("collector %v: %s %s", collector, it.State, it.Reason)
		}
		env, _ := os.ReadFile(glob1(t, filepath.Join(r.h.e.WorkDir, "steps", "*", "*", "run", "env.txt")))
		has := strings.Contains(string(env), "OTEL_EXPORTER_OTLP_ENDPOINT")
		if has == collector || !strings.Contains(string(env), "YNR_SPOOL="+r.sp.Root) {
			t.Errorf("collector %v: the run's environment is %q", collector, env)
		}
	}
}

// TestAFloodedRunFolderHitsItsQuotaAndTheStepGoesOn: a run that fills its folder has the excess
// removed, and the step carries on to its pull request.
func TestAFloodedRunFolderHitsItsQuotaAndTheStepGoesOn(t *testing.T) {
	r := newSpoolRun(t, `head -c 2000000 /dev/zero > "$YNR_SPOOL/flood.bin"
printf '{}\n' > "$YNR_SPOOL/small.jsonl"
sleep 0.3`, false)
	if it := r.sweep(t); it.State != item.Proposed {
		t.Fatalf("a flooded folder failed the step: %s %s", it.State, it.Reason)
	}
	run := r.h.item(t, 1).LastRun.ID
	if _, err := os.Stat(filepath.Join(r.sp.Root, "runs", run, "flood.bin")); !os.IsNotExist(err) {
		t.Errorf("the flood was not removed: %v", err)
	}
	if !strings.Contains(r.logs.String(), "filled its spool folder") {
		t.Errorf("not logged:\n%s", r.logs)
	}
}

// TestAFullOrBrokenSpoolNeverFailsAStep: a spool that cannot be written costs the run its
// telemetry, says so in the log, and the step goes on.
func TestAFullOrBrokenSpoolNeverFailsAStep(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root writes anywhere")
	}
	for name, breakIt := range map[string]func(root string){
		"runs/ cannot be written":      func(root string) { _ = os.Chmod(filepath.Join(root, "runs"), 0o500) },
		"manifests/ cannot be written": func(root string) { _ = os.Chmod(filepath.Join(root, "manifests"), 0o500) },
	} {
		r := newSpoolRun(t, `echo "$YNR_SPOOL" > {run_dir}/spool.txt`, true)
		breakIt(r.sp.Root)
		if it := r.sweep(t); it.State != item.Proposed {
			t.Fatalf("%s: the step failed: %s %s", name, it.State, it.Reason)
		}
		if !strings.Contains(r.logs.String(), "this run has no spool folder") {
			t.Errorf("%s: not logged:\n%s", name, r.logs)
		}
		// The run was left with no folder of its own, and with no way to reach the operator's endpoint.
		out, _ := os.ReadFile(glob1(t, filepath.Join(r.h.e.WorkDir, "steps", "*", "*", "run", "spool.txt")))
		if strings.Contains(string(out), r.sp.Root) {
			t.Errorf("%s: the run was told of a folder that was not made: %s", name, out)
		}
		_ = os.Chmod(filepath.Join(r.sp.Root, "runs"), 0o755)
		_ = os.Chmod(filepath.Join(r.sp.Root, "manifests"), 0o755)
	}
}

// TestWhatARunLeavesInItsFolderIsKeptWithTheRun: nothing ships a run's files when the collector is
// off, so they are copied into the run's capture, beside its trajectory (ADR-010). With the
// collector on, ynr serve ships them, and the job's end sweeps what it did not.
func TestWhatARunLeavesInItsFolderIsKeptWithTheRun(t *testing.T) {
	script := `printf '{"a":1}\n' > "$YNR_SPOOL/ynh-1-0.jsonl"; printf '{"b":2}\n' > "$YNR_SPOOL/vendor-1-0.open.jsonl"`
	r := newSpoolRun(t, script, false)
	if it := r.sweep(t); it.State != item.Proposed {
		t.Fatalf("%s %s", it.State, it.Reason)
	}
	step := filepath.Dir(glob1(t, filepath.Join(r.h.e.WorkDir, "steps", "*", "*", "spool")))
	for _, n := range []string{"ynh-1-0.jsonl", "vendor-1-0.open.jsonl"} {
		if b, err := os.ReadFile(filepath.Join(step, "spool", n)); err != nil || len(b) == 0 {
			t.Errorf("%s is not in the run's capture: %v", n, err)
		}
	}
	if _, err := os.Stat(filepath.Join(step, "run")); err != nil {
		t.Errorf("the capture is beside the run's own folder: %v", err)
	}

	// The collector on, with ynr serve running: nothing at the run's end.
	r = newSpoolRun(t, script, true)
	r.sp.Shipping = func() bool { return true }
	if it := r.sweep(t); it.State != item.Proposed {
		t.Fatalf("%s %s", it.State, it.Reason)
	}
	if m, _ := filepath.Glob(filepath.Join(r.h.e.WorkDir, "steps", "*", "*", "spool")); len(m) != 0 {
		t.Errorf("captured at the run's end while ynr serve ships: %v", m)
	}
	job := filepath.Join(t.TempDir(), "job")
	if n := r.sp.Sweep(job); n != 2 {
		t.Errorf("the job's end swept %d files, want 2", n)
	}
	if m, _ := filepath.Glob(filepath.Join(r.h.e.WorkDir, "steps", "*", "*", "spool", "*.jsonl")); len(m) != 2 {
		t.Errorf("the sweep put them with the run's own capture: %v", m)
	}
}

// TestTheVendorRelayReachesAYnhRunsEnvironment: a lane with run.ynh.telemetry_relay sets
// YNH_TELEMETRY_RELAY=1 for its runs, with the run's folder for the relay to write into; a lane
// without it does not.
func TestTheVendorRelayReachesAYnhRunsEnvironment(t *testing.T) {
	for _, on := range []bool{true, false} {
		h := newHarness(t)
		calls := fakeYnhEnv(t)
		sp, err := spool.New(filepath.Join(t.TempDir(), "spool"), 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		h.e.Spool = sp
		relay := ""
		if on {
			relay = ", telemetry_relay: true"
		}
		h.f.lanes = strings.Replace(lanesYAML, `      ynh: {harness: ".", focus: tidy}`, `      ynh: {harness: ".", focus: tidy`+relay+`}`, 1)
		h.e.Getenv = func(k string) string { return map[string]string{"ANTHROPIC_API_KEY": "sk-test"}[k] }
		h.f.labels[1] = []string{"ynf:agentic"}
		if err := h.e.Sweep(context.Background()); err != nil {
			t.Fatal(err)
		}
		if it := h.item(t, 1); it.State != item.Proposed {
			t.Fatalf("relay %v: %s %s", on, it.State, it.Reason)
		}
		b, _ := os.ReadFile(calls)
		if got := strings.Contains(string(b), "YNH_TELEMETRY_RELAY=1"); got != on {
			t.Errorf("relay %v: ynh was given %q", on, b)
		}
		if !strings.Contains(string(b), "YNR_SPOOL="+sp.Root+"/runs/") {
			t.Errorf("relay %v: ynh was not given its run's folder: %q", on, b)
		}
	}
}

// userExec is the process executor that says it writes as another user, as docker does for an
// image's own user or inline for the run user.
type userExec struct {
	executor.Process
	uid   uint32
	other bool
	err   error
}

func (u userExec) RunUID(context.Context, executor.Job) (uint32, bool, error) {
	return u.uid, u.other, u.err
}

// TestAManifestNamesTheUserAnExecutorSays: the manifest carries uid when the executor says the run
// writes as another user, and omits it when it writes as the folder's owner or the user cannot be
// found out (which is logged, and never fails the step).
func TestAManifestNamesTheUserAnExecutorSays(t *testing.T) {
	for name, tc := range map[string]struct {
		ex      userExec
		wantUID string
		logged  bool
	}{
		"another user": {userExec{uid: 10042, other: true}, `"uid":10042`, false},
		"the owner":    {userExec{uid: 501}, "", false},
		"not found":    {userExec{err: errors.New("no such image")}, "", true},
	} {
		r := newSpoolRun(t, `true`, false)
		r.h.e.Executor = func(string) (executor.Executor, error) { return tc.ex, nil }
		if it := r.sweep(t); it.State != item.Proposed {
			t.Fatalf("%s: %s %s", name, it.State, it.Reason)
		}
		b, err := os.ReadFile(glob1(t, filepath.Join(r.sp.Root, "manifests", "*.json")))
		if err != nil {
			t.Fatal(err)
		}
		if has := strings.Contains(string(b), `"uid"`); has != (tc.wantUID != "") || (tc.wantUID != "" && !strings.Contains(string(b), tc.wantUID)) {
			t.Errorf("%s: manifest %s, want %q", name, b, tc.wantUID)
		}
		if got := strings.Contains(r.logs.String(), "could not be found out"); got != tc.logged {
			t.Errorf("%s: logged %v:\n%s", name, got, r.logs)
		}
	}
}

// TestARelayWithNoSpoolSaysSo: the setting is on but there is nowhere for the relay to write.
func TestARelayWithNoSpoolSaysSo(t *testing.T) {
	h := newHarness(t)
	calls := fakeYnhEnv(t)
	var logs bytes.Buffer
	h.e.Log = slog.New(slog.NewTextHandler(&logs, nil))
	h.f.lanes = strings.Replace(lanesYAML, `      ynh: {harness: ".", focus: tidy}`, `      ynh: {harness: ".", focus: tidy, telemetry_relay: true}`, 1)
	h.e.Getenv = func(k string) string { return "k" }
	h.f.labels[1] = []string{"ynf:agentic"}
	if err := h.e.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(calls)
	if !strings.Contains(logs.String(), "no spool root to relay into") || !strings.Contains(string(b), "YNH_TELEMETRY_RELAY=1") {
		t.Errorf("log:\n%s\nynh:\n%s", logs.String(), b)
	}
}

// fakeYnhEnv is a ynh that records the telemetry variables it was started with.
func fakeYnhEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	script := `#!/bin/sh
env | grep -E '^(YNH_TELEMETRY_RELAY|YNR_SPOOL)=' >> "` + calls + `"
gofmt -w ./internal/format
echo '{"exit_code":0,"reason":"converged","session_id":"S-1","backend":"claude","model":"opus"}'
`
	if err := os.WriteFile(filepath.Join(dir, "ynh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}
