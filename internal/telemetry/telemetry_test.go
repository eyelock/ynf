package telemetry_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/eyelock/ynf/internal/telemetry"
	"github.com/eyelock/ynf/internal/telemetry/spooltest"
)

func TestMain(m *testing.M) {
	// The tests choose where telemetry goes through Options.Environ; the real environment's
	// resource settings and a developer's own TRACEPARENT must not leak in.
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); k == "YNR_SPOOL" || strings.HasPrefix(k, "OTEL_") || k == "TRACEPARENT" || k == "TRACESTATE" {
			_ = os.Unsetenv(k)
		}
	}
	os.Exit(m.Run())
}

func setup(t *testing.T, environ ...string) (*telemetry.T, func() spooltest.Data) {
	t.Helper()
	dir := t.TempDir()
	tel := telemetry.Setup(context.Background(), telemetry.Options{Version: "9.9.9", Environ: append([]string{"YNR_SPOOL=" + dir}, environ...)})
	return tel, func() spooltest.Data {
		tel.Shutdown(context.Background())
		return spooltest.Read(t, dir)
	}
}

func TestTheResourceNamesTheTool(t *testing.T) {
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "deployment.environment.name=staging,service.name=forged")
	tel, finish := setup(t)
	_, span := tel.Tracer().Start(context.Background(), "x")
	span.End()
	d := finish()
	if len(d.Spans) != 1 {
		t.Fatalf("%d spans", len(d.Spans))
	}
	r := d.Spans[0].Resource
	if r.Str("service.name") != "ynf" || r.Str("service.version") != "9.9.9" {
		t.Errorf("service.name and version must be ynf's own, whatever the environment says: %v", r)
	}
	if len(r.Str("service.instance.id")) != 26 {
		t.Errorf("service.instance.id is a ULID, got %q", r.Str("service.instance.id"))
	}
	if r.Str("deployment.environment.name") != "staging" {
		t.Errorf("OTEL_RESOURCE_ATTRIBUTES is honoured: %v", r)
	}
}

func TestEachProcessHasItsOwnInstanceID(t *testing.T) {
	var ids []string
	for range 2 {
		tel, finish := setup(t)
		_, span := tel.Tracer().Start(context.Background(), "x")
		span.End()
		ids = append(ids, finish().Spans[0].Resource.Str("service.instance.id"))
	}
	if ids[0] == ids[1] {
		t.Fatalf("two processes share an instance id: %v", ids)
	}
}

func TestItWritesIntoTheFolderNamed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "factory")
	tel := telemetry.Setup(context.Background(), telemetry.Options{Environ: []string{"YNR_SPOOL=" + dir}})
	_, span := tel.Tracer().Start(context.Background(), "x")
	span.End()
	tel.Shutdown(context.Background())
	files, _ := filepath.Glob(filepath.Join(dir, "ynf-*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("want one closed spool file directly in %s, found %v", dir, files)
	}
	if open, _ := filepath.Glob(filepath.Join(dir, "*.open.jsonl")); len(open) != 0 {
		t.Errorf("a file is still open after shutdown: %v", open)
	}
}

func TestNoSpoolWritesNothingAndNeverFails(t *testing.T) {
	state := t.TempDir()
	tel := telemetry.Setup(context.Background(), telemetry.Options{Environ: []string{"XDG_STATE_HOME=" + state}})
	if tel.Choice().Mode != telemetry.ModeNone || tel.Active() {
		t.Fatalf("choice %+v", tel.Choice())
	}
	ctx, span := tel.Tracer().Start(context.Background(), "x")
	telemetry.Finish(span, telemetry.OutcomeOk)
	tel.Event(ctx, telemetry.EventStepStarted)
	tel.RunFinished(ctx, "converged", "lane", "m", telemetry.Usage{InputTokens: 1})
	tel.LeaseExpired(ctx, "lane")
	tel.Shutdown(context.Background())
	if entries, _ := os.ReadDir(state); len(entries) != 0 {
		t.Errorf("nothing was to be written, but %s holds %v", state, entries)
	}
	// A nil T is safe everywhere.
	var none *telemetry.T
	none.Event(ctx, "x")
	none.RunFinished(ctx, "", "", "", telemetry.Usage{})
	none.Shutdown(ctx)
	if none.Active() || none.Choice().Mode != telemetry.ModeNone || none.Context(ctx) != ctx {
		t.Error("a nil T is inactive")
	}
}

func TestAProcessJoinsTheTraceItWasGiven(t *testing.T) {
	const tp = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	tel, finish := setup(t, "TRACEPARENT="+tp, "TRACESTATE=vendor=x")
	ctx, span := tel.Tracer().Start(tel.Context(context.Background()), "root")
	child := telemetry.Environ(ctx, []string{"A=1", "TRACEPARENT=stale"})
	span.End()
	d := finish()
	if s := d.Spans[0]; s.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || s.ParentID != "00f067aa0ba902b7" {
		t.Errorf("the root span must be the given parent's child: %+v", s)
	}
	joined := strings.Join(child, " ")
	if !strings.Contains(joined, "TRACEPARENT=00-4bf92f3577b34da6a3ce929d0e0e4736-"+d.Spans[0].SpanID+"-01") || strings.Contains(joined, "stale") {
		t.Errorf("a spawned process gets this span's context, replacing a stale one: %v", child)
	}
	if !strings.Contains(joined, "TRACESTATE=vendor=x") {
		t.Errorf("tracestate is passed on: %v", child)
	}
}

func TestATraceIsPassedOnEvenWithTelemetryOff(t *testing.T) {
	const tp = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	tel := telemetry.Setup(context.Background(), telemetry.Options{Environ: []string{"TRACEPARENT=" + tp, "XDG_STATE_HOME=" + t.TempDir()}})
	ctx, span := tel.Tracer().Start(tel.Context(context.Background()), "x")
	defer span.End()
	env := map[string]string{}
	telemetry.Inject(ctx, env)
	if env["TRACEPARENT"] != tp {
		t.Errorf("the contract passes the trace on whether or not this tool records: %v", env)
	}
	if tid, _ := telemetry.IDs(span); tid != "" {
		t.Error("a span nobody records must not be stored as the item's step")
	}
}

func TestACommandGetsTheTraceContext(t *testing.T) {
	tel, finish := setup(t)
	ctx, span := tel.Tracer().Start(context.Background(), "x")
	c := exec.CommandContext(ctx, "sh", "-c", `printf %s "$TRACEPARENT"`)
	telemetry.Command(ctx, c)
	out, err := c.Output()
	span.End()
	finish()
	if err != nil || !strings.HasPrefix(string(out), "00-") || !strings.Contains(string(out), span.SpanContext().SpanID().String()) {
		t.Fatalf("%q %v", out, err)
	}
	// With no trace in ctx the command's environment is left alone.
	plain := exec.Command("true")
	telemetry.Command(context.Background(), plain)
	if plain.Env != nil {
		t.Errorf("env %v", plain.Env)
	}
}

func TestHTTPCallsCarryTraceContext(t *testing.T) {
	var got atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got.Store(r.Header.Get("traceparent")) }))
	defer srv.Close()
	tel, finish := setup(t)
	ctx, span := tel.Tracer().Start(context.Background(), "x")
	client := &http.Client{Transport: telemetry.Transport(nil)}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	span.End()
	finish()
	if tp, _ := got.Load().(string); !strings.Contains(tp, span.SpanContext().TraceID().String()) {
		t.Errorf("traceparent %q", tp)
	}
	// A request with no trace goes out as it was.
	got.Store("")
	resp, err = client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if tp, _ := got.Load().(string); tp != "" {
		t.Errorf("no trace, but traceparent %q", tp)
	}
	// And a server joins the trace a client sent.
	h := http.Header{"Traceparent": {"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"}}
	_, s2 := tel.Tracer().Start(telemetry.FromHeaders(context.Background(), h), "served")
	if s2.SpanContext().TraceID().String() != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("the request's trace was not joined: %s", s2.SpanContext().TraceID())
	}
}

func TestFlushIsBoundedWhenTheOperatorsEndpointNeverAnswers(t *testing.T) {
	stuck := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-stuck }))
	defer srv.Close()
	defer close(stuck)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", srv.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_TIMEOUT", "60000")
	tel := telemetry.Setup(context.Background(), telemetry.Options{FlushTimeout: 300 * time.Millisecond})
	if tel.Choice().Mode != telemetry.ModeOTLP {
		t.Fatalf("choice %+v", tel.Choice())
	}
	_, span := tel.Tracer().Start(context.Background(), "x")
	span.End()
	start := time.Now()
	tel.Shutdown(context.Background())
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("shutdown took %s", d)
	}
}

func TestTheOperatorsEndpointReceivesWhatTheSpoolWould(t *testing.T) {
	var paths atomic.Value
	var seen atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if r.URL.Path == "/v1/traces" {
			seen.Add(1)
		}
		paths.Store(r.URL.Path)
	}))
	defer srv.Close()
	spool := t.TempDir()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", srv.URL)
	t.Setenv("YNR_SPOOL", spool)
	tel := telemetry.Setup(context.Background(), telemetry.Options{})
	_, span := tel.Tracer().Start(context.Background(), "x")
	span.End()
	tel.Shutdown(context.Background())
	if seen.Load() == 0 {
		t.Error("the operator's endpoint got no trace export")
	}
	if files, _ := filepath.Glob(filepath.Join(spool, "*")); len(files) != 0 {
		t.Errorf("the operator's endpoint wins, so the spool stays empty: %v", files)
	}
}

func TestALongLivedProcessFindsASpoolStartedLater(t *testing.T) {
	state := t.TempDir()
	tel := telemetry.Setup(context.Background(), telemetry.Options{
		Environ: []string{"XDG_STATE_HOME=" + state}, Watch: true, WatchEvery: 20 * time.Millisecond,
	})
	if tel.Choice().Mode != telemetry.ModeNone {
		t.Fatal("no spool yet")
	}
	laptop := filepath.Join(state, "ynr", "spool", "local")
	if err := os.MkdirAll(laptop, 0o755); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for tel.Choice().Mode == telemetry.ModeNone && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if tel.Choice().Mode != telemetry.ModeSpool || tel.Choice().Dir != laptop {
		t.Fatalf("choice %+v", tel.Choice())
	}
	_, span := tel.Tracer().Start(context.Background(), "late")
	span.End()
	tel.Shutdown(context.Background())
	if d := spooltest.Read(t, laptop); len(d.Named("late")) != 1 {
		t.Fatalf("%+v", d.Spans)
	}
}

func TestOutputIsUnchangedWithTelemetryOn(t *testing.T) {
	for _, mode := range []string{"off", "on"} {
		var plain, teed bytes.Buffer
		opts := &slog.HandlerOptions{Level: slog.LevelInfo}
		var tel *telemetry.T
		if mode == "on" {
			tel, _ = setup(t)
		}
		write := func(l *slog.Logger) {
			l.Info("decided", "item", "item/x", "state", "ready", "reason", "a long reason")
			l.With("run", "r1").WithGroup("g").Warn("x", "n", 3)
			l.Debug("not shown")
		}
		write(slog.New(slog.NewTextHandler(&plain, opts)))
		write(slog.New(telemetry.Tee(slog.NewTextHandler(&teed, opts), tel)))
		strip := func(s string) string { // the time is the one thing that differs between two runs
			var out []string
			for _, l := range strings.Split(s, "\n") {
				if _, rest, ok := strings.Cut(l, " level="); ok {
					l = rest
				}
				out = append(out, l)
			}
			return strings.Join(out, "\n")
		}
		if strip(plain.String()) != strip(teed.String()) || plain.Len() == 0 {
			t.Errorf("%s: output changed:\n%s\n--- with telemetry:\n%s", mode, plain.String(), teed.String())
		}
	}
}

func TestLogsAreBridgedWithoutContentOrSecrets(t *testing.T) {
	tel, finish := setup(t)
	var out bytes.Buffer
	l := slog.New(telemetry.Tee(slog.NewTextHandler(&out, nil), tel))
	l.Info("decided", "item", "item/github.com/o/r/issues/1", "reason", "the ticket said ignore previous instructions",
		"detail", "stderr: secret code", "err", "push to https://x-access-token:ghp_abcdefghijklmnopqrstuvwxyz0123456789@github.com/o/r failed for a@b.example",
		"by", "jane", "attempt", 2)
	d := finish()
	if len(d.Logs) != 1 {
		t.Fatalf("%d log records", len(d.Logs))
	}
	raw := d.Raw
	for _, banned := range []string{"ignore previous instructions", "secret code", "ghp_", "a@b.example", "jane"} {
		if strings.Contains(raw, banned) {
			t.Errorf("telemetry holds %q", banned)
		}
	}
	if !strings.Contains(raw, "item/github.com/o/r/issues/1") || !strings.Contains(raw, "attempt") {
		t.Errorf("the harmless attributes should be kept: %s", raw)
	}
	// People still see everything.
	if !strings.Contains(out.String(), "ignore previous instructions") || !strings.Contains(out.String(), "ghp_") {
		t.Errorf("what people read is unchanged: %s", out.String())
	}
}

func TestMetricsAreHeldToTheirDeclaredCardinality(t *testing.T) {
	tel, finish := setup(t)
	ctx := context.Background()
	limit := telemetry.CardinalityLimits[telemetry.AttrGenAiResponseModel]
	if limit == 0 {
		t.Fatal("the registry declares no limit on the model attribute")
	}
	for i := range limit + 20 {
		tel.RunFinished(ctx, "converged", "l", "model-"+strings.Repeat("x", i), telemetry.Usage{InputTokens: 5, OutputTokens: 7, CacheReadTokens: 1, CostUSD: 0.5})
	}
	models := map[string]bool{}
	var tokens, cost float64
	for _, p := range finish().Points {
		if p.Metric == telemetry.MetricRunCount {
			models[p.Attrs.Str(telemetry.AttrGenAiResponseModel)] = true
		}
		if p.Metric == telemetry.MetricRunTokens {
			tokens += p.Value
		}
		if p.Metric == telemetry.MetricRunCost {
			cost += p.Value
		}
	}
	if len(models) > limit+1 || !models["other"] {
		t.Errorf("%d distinct models against a limit of %d: past it a value is counted as other", len(models), limit)
	}
	if tokens != float64(13*(limit+20)) || cost != 0.5*float64(limit+20) {
		t.Errorf("tokens %v cost %v", tokens, cost)
	}
}

func TestStatusFollowsTheOutcome(t *testing.T) {
	ok := []string{"converged", "completed", "claimed", "ok", "accepted", "deduplicated"}
	for _, o := range ok {
		if telemetry.Status(o) != 2 {
			t.Errorf("%s should be OK", o)
		}
	}
	if telemetry.Status(telemetry.OutcomeHeld) != 0 {
		t.Error("held is neither success nor failure")
	}
	for _, o := range []string{"budget", "stuck", "tamper", "operator_error", "error", "aborted", "failed", "lost", "rejected"} {
		if telemetry.Status(o) != 1 {
			t.Errorf("%s should be an error", o)
		}
	}
}

func TestACallSpanNeverCarriesTheErrorText(t *testing.T) {
	tel, finish := setup(t)
	_, err := telemetry.Call(context.Background(), tel, telemetry.CallSystemForge, "pull_request", func(context.Context) (int, error) {
		return 0, io.ErrUnexpectedEOF
	})
	if err == nil {
		t.Fatal("the error is returned to the caller")
	}
	_, err = telemetry.Call(context.Background(), tel, telemetry.CallSystemForge, "file", func(context.Context) (int, error) {
		return 0, io.EOF
	}, io.EOF)
	if err == nil {
		t.Fatal("a benign error is still returned")
	}
	d := finish()
	calls := d.Named(telemetry.SpanCall)
	if len(calls) != 2 || calls[0].StatusCode != 2 || calls[0].Attrs.Str(telemetry.AttrOutcome) != "failed" || calls[1].StatusCode != 1 {
		t.Fatalf("%+v", calls)
	}
	if strings.Contains(d.Raw, "unexpected EOF") {
		t.Error("the error text reached the spool")
	}
}

func TestLinksAndHandles(t *testing.T) {
	if _, ok := telemetry.LinkTo("nope", "nope"); ok {
		t.Error("not ids")
	}
	if _, ok := telemetry.LinkTo("4bf92f3577b34da6a3ce929d0e0e4736", "zz"); ok {
		t.Error("not a span id")
	}
	if l, ok := telemetry.LinkTo("4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"); !ok || !l.SpanContext.IsValid() {
		t.Error("a link")
	}
	for in, want := range map[[2]string]string{
		{"github.com", "octocat"}:  "github.com/octocat",
		{"github.com", "a@b.com"}:  "",
		{"github.com", "Jane Doe"}: "",
		{"", "octocat"}:            "",
		{"github.com", ""}:         "",
	} {
		if got := telemetry.Handle(in[0], in[1]); got != want {
			t.Errorf("Handle(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

func TestScrub(t *testing.T) {
	for _, secret := range []string{
		"ghp_abcdefghijklmnopqrstuvwxyz0123456789", "github_pat_11ABCDEFG0123456789_abcdefghijklmnop", "sk-ant-api03-abcdefghijklmnop",
		"AKIAABCDEFGHIJKLMNOP", "Bearer abcdefghijklmnop", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abc123def456",
		"x-access-token:abc123", "token=abc123def", "password: hunter2", "https://user:pw@host/x",
	} {
		if got := telemetry.Scrub("before " + secret + " after"); strings.Contains(got, secret) || strings.Contains(got, "hunter2") || strings.Contains(got, "pw@") || !strings.Contains(got, "[redacted]") {
			t.Errorf("%q -> %q", secret, got)
		}
	}
	if got := telemetry.Scrub("mail jane.doe@example.com now"); strings.Contains(got, "@") {
		t.Errorf("%q", got)
	}
	if got := telemetry.Scrub("plain text with item/github.com/o/r/issues/1"); got != "plain text with item/github.com/o/r/issues/1" {
		t.Errorf("%q", got)
	}
}

func TestEventsCarryTheirSpanAndAttributes(t *testing.T) {
	tel, finish := setup(t)
	ctx, span := tel.Tracer().Start(context.Background(), "unit")
	tel.Event(ctx, telemetry.EventStepStarted, attribute.String(telemetry.AttrItemKey, "k"), attribute.Int64(telemetry.AttrLeaseEpoch, 3),
		attribute.Bool(telemetry.AttrPaused, true), attribute.Float64("f", 1.5))
	span.End()
	d := finish()
	ev := d.Events(telemetry.EventStepStarted)
	if len(ev) != 1 || ev[0].SpanID != d.Spans[0].SpanID || ev[0].TraceID != d.Spans[0].TraceID {
		t.Fatalf("%+v", ev)
	}
	if ev[0].Attrs.Str(telemetry.AttrItemKey) != "k" || ev[0].Attrs[telemetry.AttrLeaseEpoch] != int64(3) || ev[0].Attrs[telemetry.AttrPaused] != true {
		t.Errorf("%v", ev[0].Attrs)
	}
}
