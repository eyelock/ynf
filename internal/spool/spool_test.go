package spool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestMain doubles as the fake ynr: a test starts this binary as ynr, with FAKE_YNR saying how it
// behaves, so the lifecycle is tested without the real thing.
func TestMain(m *testing.M) {
	if mode := os.Getenv("FAKE_YNR"); mode != "" {
		fakeYnr(mode)
		return
	}
	os.Exit(m.Run())
}

func fakeYnr(mode string) {
	if out := os.Getenv("FAKE_YNR_ARGS"); out != "" {
		_ = os.WriteFile(out, []byte(strings.Join(os.Args[1:], "\n")), 0o644)
	}
	switch mode {
	case "info":
		fmt.Println(`{"version":"0.1.0","build":"slim","capabilities":"0.1.0","spool":"/x","serving":{"pid":1}}`)
	case "badinfo":
		fmt.Println(`not json`)
	case "fail":
		fmt.Fprintln(os.Stderr, "ynr: --upstream is required")
		os.Exit(3)
	case "serve", "slow", "deaf":
		term := make(chan os.Signal, 1)
		signal.Notify(term, syscall.SIGTERM)
		fmt.Fprintln(os.Stderr, "ynr serve: ready")
		<-term
		switch mode {
		case "slow":
			time.Sleep(100 * time.Millisecond) // shipping what is left
			if d := os.Getenv("FAKE_YNR_MARK"); d != "" {
				_ = os.WriteFile(d, []byte("shipped"), 0o644)
			}
		case "deaf":
			time.Sleep(time.Minute) // never finishes: the archive time ends it
		}
	}
	os.Exit(0)
}

func newSpool(t *testing.T) (*Spool, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	s, err := New(filepath.Join(t.TempDir(), "spool"), 1<<10, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	s.Interval = 10 * time.Millisecond
	return s, &logs
}

func TestLayout(t *testing.T) {
	s, _ := newSpool(t)
	for name, mode := range map[string]os.FileMode{FactoryDir: 0o700, ManifestsDir: 0o700, RunsDir: 0o711} {
		fi, err := os.Lstat(filepath.Join(s.Root, name))
		if err != nil || !fi.IsDir() || fi.Mode().Perm() != mode {
			t.Errorf("%s: %v %v, want a folder with mode %o", name, fi, err, mode)
		}
	}
	if s.FactoryDir() != filepath.Join(s.Root, "factory") || s.RunDir("r1") != filepath.Join(s.Root, "runs", "r1") {
		t.Errorf("folders: %s %s", s.FactoryDir(), s.RunDir("r1"))
	}
	// Making it again changes nothing.
	if _, err := New(s.Root, 0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := New("", 0, nil); err == nil {
		t.Error("no root: want an error")
	}
}

func TestLayoutRefusesALinkedFolder(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	if err := os.Symlink(other, filepath.Join(root, "manifests")); err != nil {
		t.Fatal(err)
	}
	if _, err := New(root, 0, nil); err == nil || !strings.Contains(err.Error(), "not a folder") {
		t.Fatalf("a link where manifests/ should be: %v", err)
	}
	// And a file where a folder should be.
	root = t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "runs"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := New(root, 0, nil); err == nil {
		t.Fatal("a file where runs/ should be")
	}
}

func TestManifestIsWhatYnrReads(t *testing.T) {
	s, _ := newSpool(t)
	m := Manifest{Run: "01JABC-1", Lane: "github.com/acme/factory-config#lint", Harness: "h@1.0", Focus: "go", Item: "github.com/acme/app#7", Step: "01JABC"}
	if err := s.WriteManifest(m); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Root, "manifests", "01JABC-1.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"run": "01JABC-1", "lane": "github.com/acme/factory-config#lint", "harness": "h@1.0", "focus": "go", "item": "github.com/acme/app#7", "step": "01JABC"}
	if len(got) != len(want) {
		t.Errorf("keys: %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	fi, _ := os.Lstat(path)
	if !fi.Mode().IsRegular() || len(b) >= MaxManifest {
		t.Errorf("a regular file under 64 KiB: %v, %d bytes", fi.Mode(), len(b))
	}
	// Written to a temporary name and renamed: nothing else is left in manifests/.
	ents, _ := os.ReadDir(filepath.Join(s.Root, "manifests"))
	if len(ents) != 1 {
		t.Errorf("manifests/ holds %d entries, want only the manifest", len(ents))
	}
	// Writing it again replaces it.
	m.Focus = "other"
	if err := s.WriteManifest(m); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), `"focus":"other"`) {
		t.Errorf("not replaced: %s", b)
	}
}

func TestManifestRefusesWhatYnrWouldNotRead(t *testing.T) {
	s, _ := newSpool(t)
	for name, m := range map[string]Manifest{
		"no lane":       {Run: "r1"},
		"bad run id":    {Run: "../x", Lane: "l"},
		"empty run id":  {Lane: "l"},
		"too large":     {Run: "r1", Lane: strings.Repeat("l", MaxManifest)},
		"a dot run id":  {Run: ".hidden", Lane: "l"},
		"a slash in it": {Run: "a/b", Lane: "l"},
	} {
		if err := s.WriteManifest(m); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestManifestRefusesALink(t *testing.T) {
	s, _ := newSpool(t)
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(s.Root, "manifests", "r1.json")); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteManifest(Manifest{Run: "r1", Lane: "l"}); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("a link where the manifest goes: %v", err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Errorf("the link's target was written: %q", b)
	}
	// A manifests/ that became a link is refused as well.
	root := t.TempDir()
	s2, err := New(root, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "manifests")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "manifests")); err != nil {
		t.Fatal(err)
	}
	if err := s2.WriteManifest(Manifest{Run: "r1", Lane: "l"}); err == nil {
		t.Fatal("manifests/ is a link: want an error")
	}
}

func TestManifestWriteFailureLeavesNothingBehind(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root writes anywhere")
	}
	s, _ := newSpool(t)
	if err := os.Chmod(filepath.Join(s.Root, "manifests"), 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(filepath.Join(s.Root, "manifests"), 0o700) }()
	if err := s.WriteManifest(Manifest{Run: "r1", Lane: "l"}); err == nil {
		t.Fatal("a manifests/ that cannot be written: want an error")
	}
}

func TestBeginGivesAFolderAndAManifest(t *testing.T) {
	s, _ := newSpool(t)
	r, err := s.Begin(Manifest{Run: "r1", Lane: "l", Item: "i", Step: "s"}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer r.End(filepath.Join(t.TempDir(), "cap"))
	fi, err := os.Lstat(r.Dir)
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("run folder: %v %v", fi, err)
	}
	if r.Dir != filepath.Join(s.Root, "runs", "r1") {
		t.Errorf("run folder at %s", r.Dir)
	}
	if _, err := os.Stat(filepath.Join(s.Root, "manifests", "r1.json")); err != nil {
		t.Errorf("manifest: %v", err)
	}
	// A run id used twice is refused, not shared.
	if _, err := s.Begin(Manifest{Run: "r1", Lane: "l"}, false); err == nil {
		t.Error("a second run with the same id: want an error")
	}
	// An image's own user needs the folder open to them.
	r2, err := s.Begin(Manifest{Run: "r2", Lane: "l"}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.End(filepath.Join(t.TempDir(), "cap"))
	if fi, _ := os.Lstat(r2.Dir); fi.Mode().Perm() != 0o777 {
		t.Errorf("image user's folder mode %o", fi.Mode().Perm())
	}
}

func TestBeginIsAllOrNothing(t *testing.T) {
	s, _ := newSpool(t)
	// No lane, so no manifest: the folder is not left for ynr to read without one.
	if _, err := s.Begin(Manifest{Run: "r1"}, false); err == nil {
		t.Fatal("no lane: want an error")
	}
	if _, err := os.Stat(s.RunDir("r1")); !os.IsNotExist(err) {
		t.Errorf("a folder was left behind: %v", err)
	}
	if _, err := s.Begin(Manifest{Run: "bad/id", Lane: "l"}, false); err == nil {
		t.Error("a run id ynr cannot read: want an error")
	}
}

func TestAFullSpoolNeverFailsTheCaller(t *testing.T) {
	s, _ := newSpool(t)
	// A runs/ nobody can write to: Begin says so, and the caller runs on without a folder.
	if os.Getuid() != 0 {
		if err := os.Chmod(filepath.Join(s.Root, "runs"), 0o500); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Chmod(filepath.Join(s.Root, "runs"), 0o711) }()
		if r, err := s.Begin(Manifest{Run: "r1", Lane: "l"}, false); err == nil || r != nil {
			t.Errorf("an unwritable runs/: %v %v", r, err)
		}
	}
	// A nil Run, which is what the caller holds then, is safe to end.
	var r *Run
	r.End(t.TempDir())
	if r.Trimmed() != 0 {
		t.Error("nil run trimmed")
	}
}

func TestQuotaRemovesTheLargestFilesAndTheRunGoesOn(t *testing.T) {
	s, logs := newSpool(t) // quota 1 KiB
	r, err := s.Begin(Manifest{Run: "r1", Lane: "l"}, false)
	if err != nil {
		t.Fatal(err)
	}
	small := filepath.Join(r.Dir, "small.jsonl")
	if err := os.WriteFile(small, bytes.Repeat([]byte("x"), 100), 0o600); err != nil {
		t.Fatal(err)
	}
	flood := filepath.Join(r.Dir, "flood.bin")
	if err := os.WriteFile(flood, bytes.Repeat([]byte("x"), 1<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for r.Trimmed() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	r.End(filepath.Join(t.TempDir(), "cap"))
	if _, err := os.Stat(flood); !os.IsNotExist(err) {
		t.Errorf("the flood stays: %v", err)
	}
	if _, err := os.Stat(small); err != nil {
		t.Errorf("what was within the quota was removed: %v", err)
	}
	if r.Trimmed() != 1 || !strings.Contains(logs.String(), "filled its spool folder") {
		t.Errorf("trimmed %d, log: %s", r.Trimmed(), logs)
	}
}

func TestQuotaNeverFollowsALink(t *testing.T) {
	s, _ := newSpool(t)
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, bytes.Repeat([]byte("x"), 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := s.Begin(Manifest{Run: "r1", Lane: "l"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(r.Dir, "link")); err != nil {
		t.Fatal(err)
	}
	writeSpoolFile(t, r.Dir, "flood.bin", strings.Repeat("x", 1<<20))
	r.End(filepath.Join(t.TempDir(), "cap"))
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("the link's target was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.Dir, "flood.bin")); !os.IsNotExist(err) {
		t.Errorf("the flood stays: %v", err)
	}
}

func TestQuotaCountsEntries(t *testing.T) {
	s, _ := newSpool(t)
	s.Quota = 1 << 30
	r, err := s.Begin(Manifest{Run: "r1", Lane: "l"}, false)
	if err != nil {
		t.Fatal(err)
	}
	for i := range maxEntries + 50 {
		if err := os.WriteFile(filepath.Join(r.Dir, fmt.Sprintf("f%d", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r.End(filepath.Join(t.TempDir(), "cap"))
	ents, _ := os.ReadDir(r.Dir)
	if len(ents) > maxEntries {
		t.Errorf("%d entries left, want at most %d", len(ents), maxEntries)
	}
}

func writeSpoolFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRunEndKeepsWhatIsLeftWhenNothingShips(t *testing.T) {
	s, _ := newSpool(t)
	s.Quota = 1 << 20
	r, err := s.Begin(Manifest{Run: "r1", Lane: "l"}, false)
	if err != nil {
		t.Fatal(err)
	}
	writeSpoolFile(t, r.Dir, "ynh-1-0.jsonl", `{"a":1}`+"\n")
	writeSpoolFile(t, r.Dir, "vendor-2-0.open.jsonl", `{"b":2}`+"\n")
	writeSpoolFile(t, r.Dir, "notes.txt", "not a spool file")
	if err := os.Symlink("/etc/hosts", filepath.Join(r.Dir, "evil.jsonl")); err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(t.TempDir(), "step", "spool")
	r.End(capture)
	for _, n := range []string{"ynh-1-0.jsonl", "vendor-2-0.open.jsonl"} {
		if b, err := os.ReadFile(filepath.Join(capture, n)); err != nil || len(b) == 0 {
			t.Errorf("%s not in the capture: %v", n, err)
		}
	}
	for _, n := range []string{"notes.txt", "evil.jsonl"} {
		if _, err := os.Lstat(filepath.Join(capture, n)); err == nil {
			t.Errorf("%s was captured", n)
		}
	}
	// The originals stay: a persistent spool is read by the next ynr serve.
	if _, err := os.Stat(filepath.Join(r.Dir, "ynh-1-0.jsonl")); err != nil {
		t.Errorf("original: %v", err)
	}
}

func TestRunEndCapturesNothingWhileYnrShips(t *testing.T) {
	s, _ := newSpool(t)
	s.Shipping = func() bool { return true }
	r, _ := s.Begin(Manifest{Run: "r1", Lane: "l"}, false)
	writeSpoolFile(t, r.Dir, "a.jsonl", "{}\n")
	capture := filepath.Join(t.TempDir(), "cap")
	r.End(capture)
	if _, err := os.Stat(capture); err == nil {
		t.Error("captured while ynr serve ships")
	}
	// At the job's end, what ynr did not ship goes into that run's capture, once.
	jobDir := filepath.Join(t.TempDir(), "job")
	writeSpoolFile(t, s.FactoryDir(), "ynf-1-0.jsonl", "{}\n")
	if n := s.Sweep(jobDir); n != 2 {
		t.Errorf("swept %d files, want 2", n)
	}
	for _, p := range []string{filepath.Join(capture, "a.jsonl"), filepath.Join(jobDir, "factory", "ynf-1-0.jsonl")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	if n := s.Sweep(jobDir); n != 0 {
		t.Errorf("swept the same files again: %d", n)
	}
}

func TestSweepOfARunWithNoCaptureGoesUnderTheJob(t *testing.T) {
	s, _ := newSpool(t)
	writeSpoolFile(t, filepath.Join(s.Root, "runs", "old"), "x.jsonl", "{}\n")
	writeSpoolFile(t, filepath.Join(s.Root, "runs"), "stray.jsonl", "{}\n") // a file in runs/ is not a run
	jobDir := filepath.Join(t.TempDir(), "job")
	if n := s.Sweep(jobDir); n != 1 {
		t.Errorf("swept %d", n)
	}
	if _, err := os.Stat(filepath.Join(jobDir, "runs", "old", "x.jsonl")); err != nil {
		t.Error(err)
	}
}

func TestCaptureRespectsItsLimit(t *testing.T) {
	s, logs := newSpool(t)
	writeSpoolFile(t, filepath.Join(s.Root, "runs", "r1"), "big.jsonl", strings.Repeat("x", 100))
	b := &budget{left: 10}
	if n := copyFiles(s, "runs/r1", filepath.Join(t.TempDir(), "cap"), b); n != 0 || b.dropped != 1 {
		t.Errorf("copied %d, dropped %d", n, b.dropped)
	}
	if !strings.Contains(logs.String(), "over the capture limit") {
		t.Errorf("log: %s", logs)
	}
}

func TestEnvAndStripOTLP(t *testing.T) {
	if got := Env("/s/runs/r1", false)["YNR_SPOOL"]; got != "/s/runs/r1" {
		t.Errorf("host: %s", got)
	}
	if got := Env("/s/runs/r1", true)["YNR_SPOOL"]; got != ContainerPath {
		t.Errorf("container: %s", got)
	}
	got := StripOTLP([]string{"A=1", "OTEL_EXPORTER_OTLP_ENDPOINT=http://x", "OTEL_EXPORTER_OTLP_TRACES_HEADERS=a=b", "OTEL_SDK_DISABLED=false", "OTEL_RESOURCE_ATTRIBUTES=x=y"})
	if strings.Join(got, " ") != "A=1 OTEL_SDK_DISABLED=false OTEL_RESOURCE_ATTRIBUTES=x=y" {
		t.Errorf("stripped: %v", got)
	}
}

func TestUpstreamFromEnv(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	for name, c := range map[string]struct {
		env  map[string]string
		want string
	}{
		"none":              {nil, ""},
		"the endpoint":      {map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://c:4318"}, "http://c:4318"},
		"a signal's own":    {map[string]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "http://c:4318/v1/traces"}, "http://c:4318"},
		"a logs endpoint":   {map[string]string{"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT": "http://c:4318/v1/logs/"}, "http://c:4318"},
		"endpoint wins":     {map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://a", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "http://b/v1/traces"}, "http://a"},
		"ynr's own":         {map[string]string{"YNR_UPSTREAM": "http://u"}, "http://u"},
		"otel before ynr":   {map[string]string{"YNR_UPSTREAM": "http://u", "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT": "http://m/v1/metrics"}, "http://m"},
		"a path of its own": {map[string]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "http://c/custom"}, "http://c/custom"},
	} {
		if got := UpstreamFromEnv(env(c.env)); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

func TestServeArgs(t *testing.T) {
	c := Collector{ID: "pool-1", Instance: "job-9", Upstream: "http://u:4318"}
	want := "serve --spool /s --collector-id pool-1 --collector-instance job-9 --upstream http://u:4318"
	if got := strings.Join(c.Args("/s"), " "); got != want {
		t.Errorf("args %q, want %q", got, want)
	}
	if got := strings.Join(Collector{ID: "p"}.Args("/s"), " "); got != "serve --spool /s --collector-id p" {
		t.Errorf("optional flags left off: %q", got)
	}
}

func fakeEnv(mode string, extra ...string) []string {
	return append(append(os.Environ(), "FAKE_YNR="+mode), extra...)
}

func testLog() (*slog.Logger, *syncBuf) {
	b := &syncBuf{}
	return slog.New(slog.NewTextHandler(b, nil)), b
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func self(t *testing.T) string {
	t.Helper()
	p, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestServeStartsWithTheFlagsAndStopsOnSIGTERM(t *testing.T) {
	log, logs := testLog()
	args := filepath.Join(t.TempDir(), "args")
	mark := filepath.Join(t.TempDir(), "mark")
	c := Collector{Enabled: true, ID: "pool-1", Instance: "job-9", Upstream: "http://u:4318", Archive: 5 * time.Second}
	s := StartServe(c, self(t), "/spool/root", fakeEnv("slow", "FAKE_YNR_ARGS="+args, "FAKE_YNR_MARK="+mark), log)
	if s == nil || !s.Running() {
		t.Fatalf("not started: %s", logs)
	}
	waitFor(t, "the fake to start", func() bool { _, err := os.Stat(args); return err == nil && strings.Contains(logs.String(), "ready") })
	if !s.Stop() {
		t.Error("it ended within its time, and Stop said it did not")
	}
	if b, _ := os.ReadFile(mark); string(b) != "shipped" {
		t.Error("ynr serve was given no time to ship after SIGTERM")
	}
	if s.Running() {
		t.Error("still running after Stop")
	}
	b, _ := os.ReadFile(args)
	if got, want := string(b), strings.Join(c.Args("/spool/root"), "\n"); got != want {
		t.Errorf("args %q, want %q", got, want)
	}
	if !strings.Contains(logs.String(), "collector stopped") {
		t.Errorf("log: %s", logs)
	}
}

func TestServeIsStoppedAfterTheArchiveTime(t *testing.T) {
	log, logs := testLog()
	s := StartServe(Collector{ID: "p", Upstream: "http://u", Archive: 300 * time.Millisecond}, self(t), "/s", fakeEnv("deaf"), log)
	waitFor(t, "the fake to start", func() bool { return strings.Contains(logs.String(), "ready") })
	start := time.Now()
	if s.Stop() {
		t.Error("it did not end within its time, and Stop said it did")
	}
	if d := time.Since(start); d < 250*time.Millisecond || d > 5*time.Second {
		t.Errorf("Stop took %s, want about the archive time", d)
	}
	if !strings.Contains(logs.String(), "did not finish shipping") {
		t.Errorf("log: %s", logs)
	}
}

func TestMissingYnrIsLoggedAndTheJobGoesOn(t *testing.T) {
	log, logs := testLog()
	s := StartServe(Collector{ID: "p", Upstream: "http://u"}, filepath.Join(t.TempDir(), "no-such-ynr"), "/s", nil, log)
	if s != nil {
		t.Fatal("started something that does not exist")
	}
	if !strings.Contains(logs.String(), "ynr was not found") || !strings.Contains(logs.String(), "runs without it") {
		t.Errorf("log: %s", logs)
	}
	// Everything is safe on the nil Serve the job holds then.
	if s.Running() || !s.Stop() {
		t.Error("a nil Serve")
	}
}

func TestYnrThatFailsToStartIsLoggedAndTheJobGoesOn(t *testing.T) {
	log, logs := testLog()
	s := StartServe(Collector{ID: "p", Upstream: "http://u"}, self(t), "/s", fakeEnv("fail"), log)
	if s == nil {
		t.Fatalf("it started: %s", logs)
	}
	waitFor(t, "the early end to be reported", func() bool { return strings.Contains(logs.String(), "ended before the job did") })
	if s.Running() {
		t.Error("running after it failed")
	}
	if !strings.Contains(logs.String(), "--upstream is required") {
		t.Errorf("its own message is in the log: %s", logs)
	}
	if !s.Stop() {
		t.Error("stopping what already ended")
	}
	// A file that exists but cannot be run is the same.
	notExec := filepath.Join(t.TempDir(), "ynr")
	if err := os.WriteFile(notExec, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	log2, logs2 := testLog()
	if s := StartServe(Collector{ID: "p"}, notExec, "/s", nil, log2); s != nil {
		t.Errorf("a file that is not executable started: %s", logs2)
	}
}

func TestDetect(t *testing.T) {
	t.Setenv("FAKE_YNR", "info")
	d := Detect(context.Background(), self(t))
	if !d.Found || d.Version != "0.1.0" || d.Build != "slim" || d.Capabilities != "0.1.0" || !d.Serving {
		t.Errorf("detection: %+v", d)
	}
	if d.String() != "ynr 0.1.0 (slim)" {
		t.Errorf("string: %s", d)
	}
	t.Setenv("FAKE_YNR", "badinfo")
	if d := Detect(context.Background(), self(t)); d.Found || d.Detail == "" || d.String() != "ynr not found" {
		t.Errorf("garbage: %+v", d)
	}
	if d := Detect(context.Background(), filepath.Join(t.TempDir(), "nope")); d.Found || d.Detail == "" {
		t.Errorf("missing: %+v", d)
	}
}

func TestRunsAreIsolatedFromEachOther(t *testing.T) {
	s, _ := newSpool(t)
	a, _ := s.Begin(Manifest{Run: "a", Lane: "l"}, false)
	b, _ := s.Begin(Manifest{Run: "b", Lane: "l"}, false)
	defer a.End(t.TempDir())
	defer b.End(t.TempDir())
	if a.Dir == b.Dir || strings.HasPrefix(b.Dir, a.Dir+string(filepath.Separator)) {
		t.Errorf("run folders overlap: %s %s", a.Dir, b.Dir)
	}
}
