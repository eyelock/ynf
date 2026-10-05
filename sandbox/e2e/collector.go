package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/proto"
)

// The collector proof (ADR-007, ADR-009, ADR-011; ynr ADR-003, ADR-004). With ynr available, e2e
// configures ynf with a spool root and the collector on, and ynf starts ynr serve for the sweep as
// a factory job would, with a receiver of e2e's own as its --upstream. What reaches the receiver is
// what ynr shipped, so it shows the provenance and the factory attributes ynr stamped, and what
// ynr did not ship is looked for in the run capture. It reads files and requests only: nothing in
// ynf reads telemetry back.

// collectorQuota is the run quota e2e configures: the spool-flood lane writes far past it.
const (
	collectorQuota      = "1MiB"
	collectorQuotaBytes = 1 << 20
	// imageUserUID is the user of images/probe, the spool-image lane's image.
	imageUserUID = 10042
)

// ynrBinary says which ynr to use, or why there is none: -ynr, YNF_YNR_BIN, a ynr checkout to build
// from (-ynr-src, YNR_SRC) or ynr on PATH, in that order. A checkout is built into tmp.
func ynrBinary(flagBin, flagSrc, tmp string) (bin, why string) {
	if flagBin == "off" {
		return "", "-ynr=off"
	}
	for _, p := range []string{flagBin, os.Getenv("YNF_YNR_BIN")} {
		if p != "" {
			if _, err := exec.LookPath(p); err != nil {
				return "", fmt.Sprintf("%s is not runnable: %v", p, err)
			}
			return p, ""
		}
	}
	src := flagSrc
	if src == "" {
		src = os.Getenv("YNR_SRC")
	}
	if src != "" {
		out := filepath.Join(tmp, "ynr")
		if o, err := sh(src, "go", "build", "-o", out, "./cmd/ynr"); err != nil {
			return "", fmt.Sprintf("building ynr from %s: %v\n%s", src, err, o)
		}
		return out, ""
	}
	if p, err := exec.LookPath("ynr"); err == nil {
		return p, ""
	}
	return "", "no ynr: set YNF_YNR_BIN (or -ynr), YNR_SRC to a ynr checkout to build from (or -ynr-src), or put ynr on PATH"
}

// receiver is a tiny OTLP/HTTP endpoint: ynr serve's upstream. It keeps what it is sent.
type receiver struct {
	srv *http.Server
	url string

	mu       sync.Mutex
	data     spoolData
	seen     map[string]bool
	requests int
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

func (r *receiver) close() { _ = r.srv.Shutdown(context.Background()) }

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

func attrs(kvs []*commonpb.KeyValue) []otlpAttr {
	out := make([]otlpAttr, 0, len(kvs))
	for _, kv := range kvs {
		var a otlpAttr
		a.Key = kv.Key
		switch v := kv.Value.GetValue().(type) {
		case *commonpb.AnyValue_StringValue:
			a.Value.StringValue = v.StringValue
		case *commonpb.AnyValue_IntValue:
			a.Value.IntValue = strconv.FormatInt(v.IntValue, 10)
		case *commonpb.AnyValue_BoolValue:
			a.Value.StringValue = strconv.FormatBool(v.BoolValue)
		}
		out = append(out, a)
	}
	return out
}

func resourceOf(kvs []*commonpb.KeyValue) map[string]string {
	m := map[string]string{}
	for _, a := range attrs(kvs) {
		m[a.Key] = a.Value.StringValue + a.Value.IntValue
	}
	return m
}

// keep records what arrived, once: ynr ships at least once, so a batch can arrive twice.
func (r *receiver) keep(m proto.Message) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests++
	switch v := m.(type) {
	case *coltrace.ExportTraceServiceRequest:
		for _, rs := range v.ResourceSpans {
			res := resourceOf(rs.GetResource().GetAttributes())
			for _, ss := range rs.ScopeSpans {
				for _, s := range ss.Spans {
					id := hex.EncodeToString(s.SpanId)
					key := hex.EncodeToString(s.TraceId) + "/" + id
					if r.seen[key] {
						continue
					}
					r.seen[key] = true
					sp := spoolSpan{Name: s.Name, TraceID: hex.EncodeToString(s.TraceId), SpanID: id, ParentSpanID: hex.EncodeToString(s.ParentSpanId), Attributes: attrs(s.Attributes), Res: res}
					for _, l := range s.Links {
						sp.Links = append(sp.Links, struct {
							TraceID string `json:"traceId"`
							SpanID  string `json:"spanId"`
						}{hex.EncodeToString(l.TraceId), hex.EncodeToString(l.SpanId)})
					}
					r.data.spans = append(r.data.spans, sp)
				}
			}
		}
	case *collogs.ExportLogsServiceRequest:
		for _, rl := range v.ResourceLogs {
			res := resourceOf(rl.GetResource().GetAttributes())
			for _, sl := range rl.ScopeLogs {
				for _, l := range sl.LogRecords {
					if l.EventName == "" {
						continue
					}
					ev := spoolEvent{Name: l.EventName, SpanID: hex.EncodeToString(l.SpanId), Attrs: attrs(l.Attributes), Res: res}
					key := fmt.Sprintf("%s/%s/%d/%s", ev.Name, ev.SpanID, l.TimeUnixNano, attrOf(ev.Attrs, "cloudevents.event_id"))
					if r.seen[key] {
						continue
					}
					r.seen[key] = true
					r.data.events = append(r.data.events, ev)
				}
			}
		}
	}
}

func attrOf(as []otlpAttr, k string) string {
	for _, a := range as {
		if a.Key == k {
			return a.Value.StringValue + a.Value.IntValue
		}
	}
	return ""
}

func (r *receiver) snapshot() spoolData {
	r.mu.Lock()
	defer r.mu.Unlock()
	return spoolData{spans: slices.Clone(r.data.spans), events: slices.Clone(r.data.events)}
}

// stopOrphans ends any ynr serve still running on this run's spool, such as the one a SIGKILLed ynf
// leaves behind (the crash test), and gives it a moment to ship.
func stopOrphans(spool string) {
	if exec.Command("pkill", "-TERM", "-f", "ynr serve --spool "+spool).Run() == nil {
		time.Sleep(3 * time.Second)
	}
}

// collectorSetup is what a collector run adds to the configuration.
type collectorSetup struct {
	spool string
	rcv   *receiver
	bin   string
}

func (c collectorSetup) config() string {
	return fmt.Sprintf("telemetry:\n  spool: %s\n  run_quota: %s\n  collector: {enabled: true, id: e2e, instance: \"%d\", upstream: %s, archive: 30s}\n",
		c.spool, collectorQuota, os.Getpid(), c.rcv.url)
}

// manifestFile is what ynf wrote for a run, as ynr reads it.
type manifestFile struct {
	Run     string  `json:"run"`
	Lane    string  `json:"lane"`
	Harness string  `json:"harness"`
	Focus   string  `json:"focus"`
	Item    string  `json:"item"`
	Step    string  `json:"step"`
	UID     *uint32 `json:"uid"`
}

// checkCollector proves a factory job with the collector on: ynf's own records arrived from
// factory/; each gofmt run had its manifest and its own folder; the spool lane's own record arrived
// with ynr's run provenance and the lane the manifest names, in the step's trace; the flood was
// held to the quota while the step went on; and nothing is left unshipped that the run capture
// does not hold.
func checkCollector(c collectorSetup, work, logPath string, items []item, lanes []string) error {
	stopOrphans(c.spool)
	log, _ := os.ReadFile(logPath)
	// Where the host gives each run folder a volume of its own, ynf says so and takes the folder
	// away at the run's end, after what is in it is shipped or captured.
	volumes := bytes.Contains(log, []byte("spool folder is a volume of its own, with a hard size limit"))
	d := c.rcv.snapshot()
	if err := checkTelemetry(d, gofmtKeys(items), "gofmt"); err != nil {
		return err
	}

	// ynf's own records came from factory/, through this collector.
	steps := 0
	for _, s := range d.spans {
		if s.Name != "ynf.step" {
			continue
		}
		steps++
		if s.Res["ynr.provenance"] != "factory" || s.Res["ynr.collector.id"] != "e2e" || s.Res["ynr.collector.instance"] != strconv.Itoa(os.Getpid()) {
			return fmt.Errorf("collector: a ynf.step span carries provenance %q, collector %q, instance %q; want factory, e2e, %d",
				s.Res["ynr.provenance"], s.Res["ynr.collector.id"], s.Res["ynr.collector.instance"], os.Getpid())
		}
	}
	if steps == 0 {
		return errors.New("collector: none of ynf's own records reached the receiver")
	}

	// Each run of these lanes had its manifest, as ynr reads it, and its own folder.
	runs := map[string]spoolSpan{} // run id to its ynf.run span
	for _, s := range d.spans {
		if s.Name == "ynf.run" && s.attr("ynf.run.id") != "" {
			runs[s.attr("ynf.run.id")] = s
		}
	}
	checked := map[string]int{}
	for id, s := range runs {
		lane := s.attr("ynf.lane")
		for _, name := range []string{"gofmt", "spool", "spool-image", "spool-flood"} {
			if !strings.HasSuffix(lane, "#"+name) {
				continue
			}
			b, err := os.ReadFile(filepath.Join(c.spool, "manifests", id+".json"))
			if err != nil {
				return fmt.Errorf("collector: run %s (%s) has no manifest: %w", id, name, err)
			}
			var m manifestFile
			if err := json.Unmarshal(b, &m); err != nil || m.Run != id || m.Lane != lane || !strings.HasPrefix(m.Item, "item/") || m.Step == "" {
				return fmt.Errorf("collector: run %s's manifest is %s, want its run, lane %s, item and step", id, b, lane)
			}
			if fi, err := os.Stat(filepath.Join(c.spool, "runs", id)); !volumes && (err != nil || !fi.IsDir()) {
				return fmt.Errorf("collector: run %s has no folder of its own: %v", id, err)
			}
			if name == "spool-image" && (m.UID == nil || *m.UID != imageUserUID) {
				return fmt.Errorf("collector: run %s's manifest names user %v, want the image's %d: %s", id, m.UID, imageUserUID, b)
			}
			if name != "spool-image" && m.UID != nil && *m.UID != uint32(os.Getuid()) {
				return fmt.Errorf("collector: run %s writes as the folder's owner, and its manifest names user %d: %s", id, *m.UID, b)
			}
			checked[name]++
		}
	}
	if checked["gofmt"] == 0 {
		return errors.New("collector: no gofmt run was found to check")
	}

	// The spool lane's own record, in ynr's hands.
	probes := 0
	images := 0
	for _, s := range d.spans {
		if s.Name != "probe.work" && s.Name != "probe.image" {
			continue
		}
		want := "#spool"
		if s.Name == "probe.image" {
			want = "#spool-image"
			images++
		}
		probes++
		run := s.Res["ynf.run.id"]
		parent, ok := runs[run]
		switch {
		case s.Res["ynr.provenance"] != "run":
			return fmt.Errorf("collector: the run's own record has provenance %q, want run (it claimed factory)", s.Res["ynr.provenance"])
		case !ok || !strings.HasSuffix(s.Res["ynf.lane"], want) || s.Res["ynf.lane"] != parent.attr("ynf.lane"):
			return fmt.Errorf("collector: the run's own record carries lane %q for run %q, want the lane its manifest names (it claimed forged/lane#claimed)", s.Res["ynf.lane"], run)
		case !strings.HasPrefix(s.Res["ynf.item.key"], "item/") || s.Res["ynf.step.id"] == "":
			return fmt.Errorf("collector: the run's own record lacks the item and step from its manifest: %v", s.Res)
		case s.Res["ynr.collector.id"] != "e2e":
			return fmt.Errorf("collector: the run's own record has collector %q", s.Res["ynr.collector.id"])
		case s.TraceID != parent.TraceID || s.ParentSpanID != parent.SpanID:
			return fmt.Errorf("collector: the run's own record is not a child of its ynf.run span (trace %s/%s, parent %s/%s)", s.TraceID, parent.TraceID, s.ParentSpanID, parent.SpanID)
		}
	}
	if slices.Contains(lanes, "spool") && probes-images == 0 {
		return errors.New("collector: the spool lane's own record never reached the receiver")
	}
	if slices.Contains(lanes, "spool-image") && images == 0 {
		return errors.New("collector: the image user's record never reached the receiver")
	}

	// The flood: the folder was held to the quota and the step went on (the fixture's own expect
	// checks it reached a draft pull request). Where the folder was a volume of its own the write
	// failed at its limit; elsewhere ynf took the excess away and said so. The run prints which.
	floods := 0
	for id, s := range runs {
		if !strings.HasSuffix(s.attr("ynf.lane"), "#spool-flood") {
			continue
		}
		floods++
		size, err := dirSize(filepath.Join(c.spool, "runs", id))
		if err != nil {
			return err
		}
		if size > collectorQuotaBytes {
			return fmt.Errorf("collector: run %s left %d bytes in its spool folder, over the %s quota", id, size, collectorQuota)
		}
	}
	if slices.Contains(lanes, "spool-flood") {
		exit, held, err := floodResult(work)
		if err != nil {
			return err
		}
		trimmed := bytes.Contains(log, []byte("filled its spool folder"))
		switch {
		case floods == 0:
			return errors.New("collector: no flooded run was found")
		case held > collectorQuotaBytes:
			return fmt.Errorf("collector: the flooded folder held %d bytes after the run's wait, over the %s quota", held, collectorQuota)
		case volumes && (exit == 0 || trimmed):
			return fmt.Errorf("collector: the folder was a volume of its own, so the write should have failed at its limit (dd exit %d) with nothing for ynf to remove (trimmed: %v)", exit, trimmed)
		case !volumes && !trimmed:
			return errors.New("collector: no volume of its own, and ynf did not say it removed the excess")
		}
		path := "the fallback: ynf removed the excess"
		if volumes {
			path = fmt.Sprintf("a volume of its own: dd failed at the limit (exit %d), the folder held %d bytes", exit, held)
		}
		fmt.Printf("collector: the flood was held to %s by %s\n", collectorQuota, path)
	}

	// Nothing is left unshipped, or what is left is in the run capture.
	left, err := filepath.Glob(filepath.Join(c.spool, "*", "*", "*.jsonl"))
	if err != nil {
		return err
	}
	more, _ := filepath.Glob(filepath.Join(c.spool, "factory", "*.jsonl"))
	left = append(left, more...)
	var missing []string
	for _, f := range left {
		if strings.Contains(f, string(filepath.Separator)+"manifests"+string(filepath.Separator)) {
			continue
		}
		found, _ := filepath.Glob(filepath.Join(work, "spool-capture", "*", "*", filepath.Base(f)))
		found2, _ := filepath.Glob(filepath.Join(work, "spool-capture", "*", "*", "*", filepath.Base(f)))
		found3, _ := filepath.Glob(filepath.Join(work, "steps", "*", "*", "spool", filepath.Base(f)))
		if len(found)+len(found2)+len(found3) == 0 {
			missing = append(missing, f)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("collector: %d spool file(s) were left unshipped and are not in the run capture: %v", len(missing), missing)
	}
	fmt.Printf("collector: %d ynf.step span(s) from factory/; %d gofmt, %d spool, %d spool-image and %d spool-flood run(s) had a manifest and a folder; %d run record(s) arrived with run provenance and the manifest's lane; %d spool file(s) left, all in the run capture\n",
		steps, checked["gofmt"], checked["spool"], checked["spool-image"], checked["spool-flood"], probes, len(left))
	return nil
}

func gofmtKeys(items []item) []string {
	var keys []string
	for _, it := range items {
		if it.Lane == "gofmt" {
			keys = append(keys, it.Key)
		}
	}
	return keys
}

func dirSize(dir string) (int64, error) {
	var n int64
	err := filepath.Walk(dir, func(_ string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if fi.Mode().IsRegular() {
			n += fi.Size()
		}
		return nil
	})
	return n, err
}

// floodResult reads what the spool-flood run printed: dd's exit status and the bytes the folder
// held after the run's wait.
func floodResult(work string) (exit int, held int64, err error) {
	files, _ := filepath.Glob(filepath.Join(work, "steps", "*", "*", "run", "stdout"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		var gotExit, gotHeld bool
		for _, l := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(l), "flood-dd-exit="); ok {
				exit, gotExit = atoi(v), true
			}
			if v, ok := strings.CutPrefix(strings.TrimSpace(l), "flood-bytes="); ok {
				held, gotHeld = int64(atoi(v)), true
			}
		}
		if gotExit && gotHeld {
			return exit, held, nil
		}
	}
	return 0, 0, errors.New("collector: no spool-flood run printed what its folder held")
}

func atoi(s string) int { n, _ := strconv.Atoi(strings.TrimSpace(s)); return n }
