// Command fullchain is ynf's full-chain check (issue #86; ynr ADR-008): one ynf step runs a lane
// whose runner is ynh, with the vendor relay on and ynr's stub vendor standing in for the vendor
// CLI, and what ynr serve ships must be one trace with ynr's provenance and the factory attributes
// stamped on every record the run wrote.
//
//	fullchain
//
// ynf, ynh, ynr, ynr-stub-vendor and fakeforge are found on PATH, like any tool ynf runs. The check
// builds everything else itself in a temporary directory: a local repository with a one-sensor
// harness, an offline forge (fakeforge), a ynf config with the collector on and a receiver of its
// own as ynr serve's upstream. It uses no model, no network and no docker, and touches nothing of
// yours: HOME, XDG_STATE_HOME and YNH_HOME are its own.
//
// The lane's harness is ".", the one the repository carries. ynf installs it into a ynh home of
// the run's own, so the check also asserts that YNH_HOME, which stands for the operator's, is left
// as it was.
//
// The lane runs on the process executor. The docker executor needs an agent image with ynh and the
// vendor in it, which is a build of its own; the process executor is the same ynf code path for the
// run folder, the manifest, TRACEPARENT and the relay setting, and the allowed uncontained run is
// exactly what a stub vendor needs.
package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/proto"

	"github.com/eyelock/ynf/internal/telemetry"
)

const (
	lane    = "chain"
	harness = "."
	focus   = "tidy"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fullchain: FAIL:", err)
		os.Exit(1)
	}
}

// record is anything ynr shipped, with the resource it was shipped under.
type record struct {
	kind, name        string
	trace, span, from string
	res, attrs        map[string]string
}

type receiver struct {
	url  string
	srv  *http.Server
	mu   sync.Mutex
	recs []record
	seen map[string]bool
}

func startReceiver() (*receiver, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	r := &receiver{url: "http://" + ln.Addr().String(), seen: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/traces", r.handle(&coltrace.ExportTraceServiceRequest{}, &coltrace.ExportTraceServiceResponse{}))
	mux.HandleFunc("/v1/logs", r.handle(&collogs.ExportLogsServiceRequest{}, &collogs.ExportLogsServiceResponse{}))
	mux.HandleFunc("/v1/metrics", r.handle(&colmetrics.ExportMetricsServiceRequest{}, &colmetrics.ExportMetricsServiceResponse{}))
	r.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = r.srv.Serve(ln) }()
	return r, nil
}

func (r *receiver) handle(req, resp proto.Message) http.HandlerFunc {
	return func(w http.ResponseWriter, hr *http.Request) {
		var body io.Reader = hr.Body
		if hr.Header.Get("Content-Encoding") == "gzip" {
			gz, err := gzip.NewReader(hr.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			body = gz
		}
		b, err := io.ReadAll(body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		m := proto.Clone(req)
		if err := proto.Unmarshal(b, m); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		r.keep(m)
		out, _ := proto.Marshal(resp)
		w.Header().Set("Content-Type", "application/x-protobuf")
		_, _ = w.Write(out)
	}
}

func resourceOf(kvs []*commonpb.KeyValue) map[string]string {
	m := map[string]string{}
	for _, kv := range kvs {
		switch v := kv.GetValue().GetValue().(type) {
		case *commonpb.AnyValue_StringValue:
			m[kv.Key] = v.StringValue
		case *commonpb.AnyValue_IntValue:
			m[kv.Key] = fmt.Sprint(v.IntValue)
		}
	}
	return m
}

// keep records what arrived once: ynr ships at least once, so a batch can arrive twice.
func (r *receiver) keep(m proto.Message) {
	r.mu.Lock()
	defer r.mu.Unlock()
	add := func(key string, rec record) {
		if !r.seen[key] {
			r.seen[key] = true
			r.recs = append(r.recs, rec)
		}
	}
	switch v := m.(type) {
	case *coltrace.ExportTraceServiceRequest:
		for _, rs := range v.ResourceSpans {
			res := resourceOf(rs.GetResource().GetAttributes())
			for _, ss := range rs.ScopeSpans {
				for _, s := range ss.Spans {
					t, id := hex.EncodeToString(s.TraceId), hex.EncodeToString(s.SpanId)
					add("span/"+t+"/"+id, record{kind: "span", name: s.Name, trace: t, span: id, from: hex.EncodeToString(s.ParentSpanId), res: res, attrs: resourceOf(s.Attributes)})
				}
			}
		}
	case *collogs.ExportLogsServiceRequest:
		for _, rl := range v.ResourceLogs {
			res := resourceOf(rl.GetResource().GetAttributes())
			for _, sl := range rl.ScopeLogs {
				for _, l := range sl.LogRecords {
					t, id := hex.EncodeToString(l.TraceId), hex.EncodeToString(l.SpanId)
					name := l.EventName
					if name == "" {
						name = "(log)"
					}
					add(fmt.Sprintf("log/%s/%s/%s/%d/%s", name, t, id, l.TimeUnixNano, res["service.instance.id"]), record{kind: "log", name: name, trace: t, span: id, res: res})
				}
			}
		}
	case *colmetrics.ExportMetricsServiceRequest:
		for _, rm := range v.ResourceMetrics {
			res := resourceOf(rm.GetResource().GetAttributes())
			for _, sm := range rm.ScopeMetrics {
				for _, mt := range sm.Metrics {
					add("metric/"+mt.Name+"/"+res["service.instance.id"]+"/"+res[telemetry.AttrRunID], record{kind: "metric", name: mt.Name, res: res})
				}
			}
		}
	}
}

func (r *receiver) snapshot() []record {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]record(nil), r.recs...)
}

func lookPath(names ...string) (map[string]string, error) {
	out := map[string]string{}
	var missing []string
	for _, n := range names {
		p, err := exec.LookPath(n)
		if err != nil {
			missing = append(missing, n)
			continue
		}
		out[n] = p
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("not on PATH: %s", strings.Join(missing, ", "))
	}
	return out, nil
}

func sh(dir string, env []string, name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Dir, c.Env = dir, env
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, out)
	}
	return nil
}

const pluginJSON = `{
  "$schema": "https://eyelock.github.io/ynh/schema/plugin.schema.json",
  "name": "chain",
  "version": "0.1.0",
  "description": "A harness for ynf's full-chain check",
  "default_vendor": "claude",
  "focuses": {"tidy": {"prompt": "Do the task."}},
  "agent": {"max_turns": 3, "max_wall": "2m"},
  "sensors": {"ok": {"category": "behaviour", "tolerance": "blocking", "source": {"command": "true"}, "output": {"format": "text"}}}
}
`

const lanesYAML = `version: 1
lanes:
  chain:
    kind: originate
    intake: [{github.search: "label:ynf:fmt", every: 5m}]
    run:
      runner: ynh
      executor: process
      ynh: {harness: ".", vendor: claude, focus: tidy, telemetry_relay: true}
    when: {converged: open_pr}
`

func run() error {
	tools, err := lookPath("ynf", "ynh", "ynr", "ynr-stub-vendor", "fakeforge", "git")
	if err != nil {
		return err
	}
	t, err := os.MkdirTemp("", "fullchain-")
	if err != nil {
		return err
	}
	if os.Getenv("FULLCHAIN_KEEP") == "" {
		defer func() { _ = os.RemoveAll(t) }()
	} else {
		fmt.Println("kept", t)
	}
	bin := filepath.Join(t, "bin")
	for _, d := range []string{bin, filepath.Join(t, "home"), filepath.Join(t, "state"), filepath.Join(t, "ynh"), filepath.Join(t, "src", ".agents", "harness")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	// The vendor CLI, first on PATH under its own name: ynr's stub vendor answers the stream-json session ynh drives.
	if err := os.Symlink(tools["ynr-stub-vendor"], filepath.Join(bin, "claude")); err != nil {
		return err
	}

	// A local repository standing in for github.com/o/r, with the harness in it.
	src, remote := filepath.Join(t, "src"), filepath.Join(t, "remote", "o", "r.git")
	if err := os.WriteFile(filepath.Join(src, ".agents", "harness", "plugin.json"), []byte(pluginJSON), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("hello\n"), 0o644); err != nil {
		return err
	}
	genv := append(cleanEnv(), "HOME="+filepath.Join(t, "home"), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=chain", "GIT_AUTHOR_EMAIL=chain@example.com", "GIT_COMMITTER_NAME=chain", "GIT_COMMITTER_EMAIL=chain@example.com")
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"add", "."}, {"commit", "-q", "-m", "init"}} {
		if err := sh(src, genv, "git", a...); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(remote), 0o755); err != nil {
		return err
	}
	if err := sh(src, genv, "git", "clone", "-q", "--bare", src, remote); err != nil {
		return err
	}
	gitconfig := filepath.Join(t, "gitconfig")
	if err := os.WriteFile(gitconfig, []byte(fmt.Sprintf("[url %q]\n\tinsteadOf = https://github.com/o/r.git\n", remote)), 0o644); err != nil {
		return err
	}

	rcv, err := startReceiver()
	if err != nil {
		return err
	}
	defer func() { _ = rcv.srv.Shutdown(context.Background()) }()
	spool := filepath.Join(t, "spool")
	files := map[string]string{
		"lanes.yaml": lanesYAML,
		"extra.yaml": fmt.Sprintf("telemetry:\n  spool: %s\n  run_quota: 16MiB\n  collector: {enabled: true, id: fullchain, upstream: %s, archive: 20s}\n", spool, rcv.url),
	}
	for n, c := range files {
		if err := os.WriteFile(filepath.Join(t, n), []byte(c), 0o644); err != nil {
			return err
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, tools["fakeforge"], "--lanes", filepath.Join(t, "lanes.yaml"), "--config-extra", filepath.Join(t, "extra.yaml"),
		"--", tools["ynf"], "--interactive", "sweep")
	cmd.Dir = t
	cmd.Env = append(cleanEnv(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"HOME="+filepath.Join(t, "home"), "XDG_STATE_HOME="+filepath.Join(t, "state"), "YNH_HOME="+filepath.Join(t, "ynh"),
		"GIT_CONFIG_GLOBAL="+gitconfig, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=chain", "GIT_AUTHOR_EMAIL=chain@example.com", "GIT_COMMITTER_NAME=chain", "GIT_COMMITTER_EMAIL=chain@example.com")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	runErr := cmd.Run()
	if runErr != nil {
		return fmt.Errorf("ynf sweep: %w\n%s", runErr, out.String())
	}
	// The run installed the harness into a home of its own: the one standing for the operator's is
	// as it was made.
	if es, err := os.ReadDir(filepath.Join(t, "ynh")); err != nil || len(es) != 0 {
		return fmt.Errorf("the run changed the operator's ynh home (%d entries, %v)\n%s", len(es), err, out.String())
	}
	if os.Getenv("FULLCHAIN_VERBOSE") != "" {
		fmt.Println(out.String())
	}

	// Wait for the records the check turns on to be shipped: ynf's step, ynh's run and the vendor's
	// first span. ynr reads each writer's folder on its own schedule.
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		seen := map[string]bool{}
		for _, r := range rcv.snapshot() {
			if r.kind == "span" {
				seen[r.res["service.name"]+" "+r.name] = true
			}
		}
		if seen["ynf ynf.step"] && seen["ynh ynh.run"] && seen["claude-code claude_code.interaction"] {
			time.Sleep(time.Second) // and what was written beside them
			break
		}
	}
	recs := rcv.snapshot()
	if err := check(recs); err != nil {
		return fmt.Errorf("%w\n--- ynf's output ---\n%s", err, out.String())
	}
	return nil
}

// cleanEnv is the environment without anything that steers telemetry or a laptop's tools.
func cleanEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "PATH" || k == "HOME" || k == "XDG_STATE_HOME" || k == "TRACEPARENT" || k == "TRACESTATE" ||
			strings.HasPrefix(k, "OTEL_") || strings.HasPrefix(k, "YNR_") || strings.HasPrefix(k, "YNH_") || strings.HasPrefix(k, "YNF_") || strings.HasPrefix(k, "GIT_") {
			continue
		}
		env = append(env, kv)
	}
	return env
}

// check is the full-chain assertion over what ynr serve shipped.
func check(recs []record) error {
	var problems []string
	bad := func(f string, a ...any) { problems = append(problems, fmt.Sprintf(f, a...)) }

	byService := map[string]int{}
	for _, r := range recs {
		byService[r.res["service.name"]]++
	}
	var steps []record
	for _, r := range recs {
		if r.kind == "span" && r.name == telemetry.SpanStep && r.res["service.name"] == "ynf" {
			steps = append(steps, r)
		}
	}
	if len(steps) != 1 {
		names := map[string]int{}
		for _, r := range recs {
			names[r.res["service.name"]+" "+r.kind+" "+r.name]++
		}
		return fmt.Errorf("want one ynf.step span shipped, got %d (records shipped: %v)", len(steps), names)
	}
	step := steps[0]
	trace := step.trace

	// The step's descendants, by parent links.
	spans := map[string]record{}
	for _, r := range recs {
		if r.kind == "span" && r.trace == trace {
			spans[r.span] = r
		}
	}
	under := func(r record, ancestor string) bool {
		for p, hops := r.from, 0; p != "" && hops < 64; hops++ {
			if p == ancestor {
				return true
			}
			next, ok := spans[p]
			if !ok {
				return false
			}
			p = next.from
		}
		return false
	}

	// ynf.step, then ynh.run beneath it, then the relayed vendor's records beneath that.
	var ynhRuns, vendorSpans []record
	for _, r := range spans {
		switch {
		case r.name == "ynh.run" && r.res["service.name"] == "ynh":
			ynhRuns = append(ynhRuns, r)
		case r.res["service.name"] == "claude-code":
			vendorSpans = append(vendorSpans, r)
		}
	}
	if len(ynhRuns) != 1 {
		bad("want one ynh.run span in the step's trace %s, got %d", trace, len(ynhRuns))
	} else {
		if !under(ynhRuns[0], step.span) {
			bad("ynh.run is not beneath ynf.step in the trace")
		}
		if len(vendorSpans) == 0 {
			bad("no span from the relayed vendor (service claude-code) is in the step's trace")
		}
		for _, v := range vendorSpans {
			if !under(v, ynhRuns[0].span) {
				bad("vendor span %q is not beneath ynh.run", v.name)
			}
		}
	}

	// Everything the run wrote carries run provenance and the factory attributes from the manifest;
	// everything ynf wrote itself is factory. No record is in another trace than the step's.
	runWrote := 0
	for _, r := range recs {
		svc := r.res["service.name"]
		if svc == "ynf" {
			if r.res["ynr.provenance"] != "factory" {
				bad("%s %q of ynf has provenance %q, want factory", r.kind, r.name, r.res["ynr.provenance"])
			}
			continue
		}
		runWrote++
		if svc != "ynh" && svc != "claude-code" {
			bad("%s %q has service.name %q: not ynf, ynh or the vendor", r.kind, r.name, svc)
		}
		if r.res["ynr.provenance"] != "run" {
			bad("%s %q of %s has provenance %q, want run", r.kind, r.name, svc, r.res["ynr.provenance"])
		}
		for k, want := range map[string]string{telemetry.AttrLaneHarness: harness, telemetry.AttrLaneFocus: focus} {
			if r.res[k] != want {
				bad("%s %q of %s has %s=%q, want %q", r.kind, r.name, svc, k, r.res[k], want)
			}
		}
		if r.res[telemetry.AttrLane] != step.attrs[telemetry.AttrLane] || !strings.HasSuffix(r.res[telemetry.AttrLane], "#"+lane) {
			bad("%s %q of %s has ynf.lane=%q, want the lane the step ran, %q", r.kind, r.name, svc, r.res[telemetry.AttrLane], step.attrs[telemetry.AttrLane])
		}
		if r.trace != "" && r.trace != trace {
			bad("%s %q of %s is in trace %s, not the step's %s", r.kind, r.name, svc, r.trace, trace)
		}
	}
	if runWrote == 0 {
		bad("nothing the run wrote reached the receiver")
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		var tree []string
		for _, r := range recs {
			if r.kind == "span" && r.res["service.name"] != "ynf" || r.name == telemetry.SpanStep || r.name == telemetry.SpanRun {
				tree = append(tree, fmt.Sprintf("  %s span %q trace %.6s span %.6s parent %.6s provenance=%s", r.res["service.name"], r.name, r.trace, r.span, r.from, r.res["ynr.provenance"]))
			}
		}
		sort.Strings(tree)
		return errors.New(strings.Join(dedupe(problems), "\n") + "\nspans shipped:\n" + strings.Join(tree, "\n"))
	}
	fmt.Printf("fullchain: ok\n  one trace %s: ynf.step -> ynh.run (%d) -> %d vendor span(s)\n  %d records from the run, all with ynr.provenance=run, ynf.lane=%s, ynf.lane.harness=%s, ynf.lane.focus=%s\n  records by service: %v\n",
		trace, len(ynhRuns), len(vendorSpans), runWrote, step.attrs[telemetry.AttrLane], harness, focus, byService)
	return nil
}

func dedupe(in []string) []string {
	var out []string
	for i, s := range in {
		if i == 0 || s != in[i-1] {
			out = append(out, s)
		}
	}
	return out
}
