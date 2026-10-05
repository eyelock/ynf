package engine_test

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/eyelock/ynf/internal/engine"
	"github.com/eyelock/ynf/internal/event"
	"github.com/eyelock/ynf/internal/item"
	"github.com/eyelock/ynf/internal/telemetry"
	"github.com/eyelock/ynf/internal/telemetry/spooltest"
	"github.com/eyelock/ynf/internal/tracker"
)

// withSpool turns telemetry on for the harness, writing to a temporary spool, and returns a
// function that ends it and reads the spool back.
func withSpool(t *testing.T, h *harness) func() spooltest.Data {
	t.Helper()
	dir := t.TempDir()
	tel := telemetry.Setup(context.Background(), telemetry.Options{Version: "test", Environ: []string{"YNR_SPOOL=" + dir}})
	h.e.Telemetry = tel
	return func() spooltest.Data {
		tel.Shutdown(context.Background())
		return spooltest.Read(t, dir)
	}
}

// TestAStepIsATraceLinkedToItsItemsHistory: a step is one trace with a span for each phase and a
// child for each call out, carries the item, step, lane, policy hash, lease epoch and repository,
// and links to the item's previous step and to the intake that triggered it, read back from the
// spool files.
func TestAStepIsATraceLinkedToItsItemsHistory(t *testing.T) {
	h := newHarness(t)
	finish := withSpool(t, h)
	ctx := context.Background()
	h.f.labels[1] = []string{"ynf:fmt", "pkg:internal/format"}

	if err := h.e.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	first := h.item(t, 1)
	if first.Trace == nil || first.Trace.Step == nil || first.Trace.Step.TraceID == "" {
		t.Fatalf("the item does not keep its last step's span: %+v", first.Trace)
	}
	h.advance(time.Minute)
	if n, err := h.e.RunDue(ctx); err != nil || n != 1 {
		t.Fatalf("second step: %d, %v", n, err)
	}
	second := h.item(t, 1)
	if second.Trace.Step.SpanID == first.Trace.Step.SpanID {
		t.Fatal("the second step left the first step's span on the item")
	}
	d := finish()

	steps := d.Named(telemetry.SpanStep)
	if len(steps) != 2 {
		t.Fatalf("%d step spans: %+v", len(steps), steps)
	}
	s1, s2 := steps[0], steps[1]
	if s1.SpanID != first.Trace.Step.SpanID || s1.TraceID != first.Trace.Step.TraceID {
		t.Errorf("the item says %+v, the spool has the first step as %s/%s", first.Trace.Step, s1.TraceID, s1.SpanID)
	}
	for _, want := range [][2]string{
		{telemetry.AttrItemKey, first.Key},
		{telemetry.AttrLane, "github.com/o/r#fmt"},
		{telemetry.AttrRepo, "github.com/o/r"},
		{telemetry.AttrVcsRepositoryURLFull, "https://github.com/o/r"},
		{telemetry.AttrOutcome, telemetry.OutcomeCompleted},
	} {
		if got := s1.Attrs.Str(want[0]); got != want[1] {
			t.Errorf("step span %s = %q, want %q", want[0], got, want[1])
		}
	}
	if s1.Attrs.Str(telemetry.AttrStepID) == "" || len(s1.Attrs.Str(telemetry.AttrPolicyHash)) != 64 {
		t.Errorf("step id or policy hash missing: %v", s1.Attrs)
	}
	// A released lease is gone, so each clean step starts at epoch 1; only a lease taken over from a
	// holder that died counts on.
	if s1.Attrs[telemetry.AttrLeaseEpoch] != int64(1) || s2.Attrs[telemetry.AttrLeaseEpoch] != int64(1) {
		t.Errorf("lease epochs %v and %v, want 1 and 1", s1.Attrs[telemetry.AttrLeaseEpoch], s2.Attrs[telemetry.AttrLeaseEpoch])
	}
	if s1.StatusCode != 1 {
		t.Errorf("a completed step's status is %d, want OK (1)", s1.StatusCode)
	}

	// The trace of a step: claim, probe, decide, act and the run, each a descendant of the step,
	// and the calls out to other systems beneath them.
	inTrace := func(name string, tr string) []spooltest.Span {
		var out []spooltest.Span
		for _, s := range d.Named(name) {
			if s.TraceID == tr {
				out = append(out, s)
			}
		}
		return out
	}
	for _, name := range []string{telemetry.SpanClaim, telemetry.SpanProbe, telemetry.SpanDecide, telemetry.SpanAct, telemetry.SpanRun, telemetry.SpanCall} {
		if len(inTrace(name, s1.TraceID)) == 0 {
			t.Errorf("the first step's trace has no %s span", name)
		}
	}
	run := inTrace(telemetry.SpanRun, s1.TraceID)[0]
	if run.Attrs.Str(telemetry.AttrOutcome) != "converged" || run.StatusCode != 1 || run.Attrs.Str(telemetry.AttrRunID) == "" {
		t.Errorf("run span: %v status %d", run.Attrs, run.StatusCode)
	}
	if run.Attrs.Str(telemetry.AttrLane) != "github.com/o/r#fmt" {
		t.Errorf("run lane: %v", run.Attrs)
	}
	systems := map[string]bool{}
	for _, c := range inTrace(telemetry.SpanCall, s1.TraceID) {
		systems[c.Attrs.Str(telemetry.AttrCallSystem)] = true
		if c.Attrs.Str(telemetry.AttrCallOperation) == "" || c.Attrs.Str(telemetry.AttrOutcome) == "" {
			t.Errorf("call span without operation or outcome: %v", c.Attrs)
		}
	}
	for _, want := range []string{"forge", "tracker", "git", "executor"} {
		if !systems[want] {
			t.Errorf("no call span to %s; have %v", want, systems)
		}
	}
	// Every unit announces its start, so a crash shows as a start with no finish.
	if n := len(d.Events(telemetry.EventStepStarted)); n != 2 {
		t.Errorf("%d ynf.step.started events, want 2", n)
	}
	if n := len(d.Events(telemetry.EventRunStarted)); n != 1 {
		t.Errorf("%d ynf.run.started events, want 1", n)
	}

	// The second step links to the first, and each to the intake of the event that started it.
	has := func(s spooltest.Span, tid, sid string) bool {
		for _, l := range s.Links {
			if l.TraceID == tid && l.SpanID == sid {
				return true
			}
		}
		return false
	}
	if !has(s2, s1.TraceID, s1.SpanID) {
		t.Errorf("the second step does not link to the first: %+v", s2.Links)
	}
	for i, s := range steps {
		var intake *spooltest.Span
		for _, l := range s.Links {
			if sp, ok := d.Span(l.SpanID); ok && sp.Name == telemetry.SpanIntake {
				intake = &sp
			}
		}
		if intake == nil {
			t.Errorf("step %d links to no ynf.intake span: %+v", i+1, s.Links)
		}
	}
	if len(s1.Links) != 1 {
		t.Errorf("the first step has no previous step, so one link (its intake): %+v", s1.Links)
	}
	if s1.ParentID != "" {
		t.Errorf("a step with no TRACEPARENT starts a trace, but has a parent %s", s1.ParentID)
	}

	// The metrics.
	var runs float64
	for _, p := range d.Points {
		if p.Metric == telemetry.MetricRunCount && p.Attrs.Str(telemetry.AttrOutcome) == "converged" && p.Attrs.Str(telemetry.AttrLane) == "github.com/o/r#fmt" {
			runs += p.Value
		}
		for k := range p.Attrs {
			if k == telemetry.AttrItemKey || k == telemetry.AttrRunID || k == telemetry.AttrStepID {
				t.Errorf("metric %s carries %s, which is never a metric attribute", p.Metric, k)
			}
		}
	}
	if runs != 1 {
		t.Errorf("ynf.run.count for the converged run = %v, want 1", runs)
	}
}

// TestEveryReceivedEventIsMirroredOnce: each CloudEvent received is one ynf.intake span with
// exactly one ynf.intake.received event, accepted, deduplicated or rejected, the standard
// cloudevents attributes on it.
func TestEveryReceivedEventIsMirroredOnce(t *testing.T) {
	h := newHarness(t)
	finish := withSpool(t, h)
	ctx := context.Background()
	h.f.labels[1] = []string{"ynf:fmt", "pkg:internal/format"}
	if err := h.e.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	h.advance(time.Minute)
	if _, err := h.e.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	h.e.MirrorDelivery(ctx, "issues", "delivery-1", "", telemetry.OutcomeDeduplicated)
	h.e.MirrorDelivery(ctx, "issues", "delivery-2", "", telemetry.OutcomeRejected)
	// An event for an unenrolled repository is refused, and that is mirrored too.
	if _, err := h.e.HandleGitHubEvent(ctx, "issues", []byte(`{"repository":{"full_name":"x/y","html_url":"https://github.com/x/y"},"issue":{"number":1}}`)); err == nil {
		t.Fatal("an unenrolled repository was accepted")
	}
	d := finish()

	received := d.Events(telemetry.EventIntakeReceived)
	intakes := d.Named(telemetry.SpanIntake)
	if len(received) != len(intakes) || len(received) != 5 {
		t.Fatalf("%d intake spans and %d received events, want 5 of each (ticket matched, timer due, one deduplicated, two rejected)", len(intakes), len(received))
	}
	bySpan := map[string]int{}
	ids := map[string]bool{}
	results := map[string]int{}
	for _, r := range received {
		bySpan[r.SpanID]++
		ids[r.Attrs.Str(telemetry.AttrCloudeventsEventID)] = true
		results[r.Attrs.Str(telemetry.AttrIntakeResult)]++
		for _, k := range []string{telemetry.AttrCloudeventsEventID, telemetry.AttrCloudeventsEventSource, telemetry.AttrCloudeventsEventType} {
			if r.Attrs.Str(k) == "" {
				t.Errorf("received event without %s: %v", k, r.Attrs)
			}
		}
	}
	for _, in := range intakes {
		if bySpan[in.SpanID] != 1 {
			t.Errorf("intake span %s carries %d received events, want exactly one", in.SpanID, bySpan[in.SpanID])
		}
	}
	if len(ids) != 5 {
		t.Errorf("event ids are not distinct: %v", ids)
	}
	if results[telemetry.OutcomeAccepted] != 2 || results[telemetry.OutcomeDeduplicated] != 1 || results[telemetry.OutcomeRejected] != 2 {
		t.Errorf("results %v", results)
	}
	var matched bool
	for _, r := range received {
		if r.Attrs.Str(telemetry.AttrCloudeventsEventType) == event.TicketMatched && r.Attrs.Str(telemetry.AttrCloudeventsEventSubject) == "github.com/o/r#1" {
			matched = true
		}
	}
	if !matched {
		t.Errorf("no mirror of the ticket match: %+v", received)
	}
	// A rejected span is an error, a deduplicated one is not.
	for _, in := range intakes {
		want := 1
		if in.Attrs.Str(telemetry.AttrOutcome) == telemetry.OutcomeRejected {
			want = 2
		}
		if in.StatusCode != want {
			t.Errorf("intake %s status %d, want %d", in.Attrs.Str(telemetry.AttrOutcome), in.StatusCode, want)
		}
	}
}

var anEmail = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+\.[A-Za-z]{2,}`)

// TestNoContentAndNoEmailsAreExported: the ticket's text, the lane's task and people's addresses
// never reach the spool.
func TestNoContentAndNoEmailsAreExported(t *testing.T) {
	h := newHarness(t)
	finish := withSpool(t, h)
	h.f.labels[1] = []string{"ynf:fmt", "pkg:internal/format"}
	if err := h.e.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	d := finish()
	if len(d.Spans) == 0 {
		t.Fatal("nothing was exported")
	}
	for _, banned := range []string{"ignore previous instructions", "Issue 1", "func F", "gofmt -w"} {
		if strings.Contains(d.Raw, banned) {
			t.Errorf("the spool holds content %q", banned)
		}
	}
	if m := anEmail.FindString(d.Raw); m != "" {
		t.Errorf("the spool holds an email address: %s", m)
	}
}

// TestTelemetryOffLeavesTheItemAsItWas: with no spool and no endpoint nothing is recorded on the
// item, so behaviour is what it was.
func TestTelemetryOffLeavesTheItemAsItWas(t *testing.T) {
	h := newHarness(t)
	h.e.Telemetry = telemetry.Setup(context.Background(), telemetry.Options{Environ: []string{"XDG_STATE_HOME=" + t.TempDir()}})
	h.f.labels[1] = []string{"ynf:fmt", "pkg:internal/format"}
	if err := h.e.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if it := h.item(t, 1); it.Trace != nil || it.State != item.Proposed {
		t.Fatalf("trace %+v, state %s", it.Trace, it.State)
	}
}

// TestADetachedStartsIntakeIsLinkedByTheLaterStep: a start that only records the item leaves its
// intake span on the item, so the step a running serve takes later, in another process, links to
// it.
func TestADetachedStartsIntakeIsLinkedByTheLaterStep(t *testing.T) {
	h := newHarness(t)
	finish := withSpool(t, h)
	ctx := context.Background()
	h.f.labels[1] = []string{"ynf:fmt", "pkg:internal/format"}
	if _, err := h.e.Start(ctx, engine.StartRequest{Ref: tracker.Ref{Host: "github.com", Key: "o/r#1"}, Lane: "fmt", Detach: true}); err != nil {
		t.Fatal(err)
	}
	pending := h.item(t, 1)
	if pending.Trace == nil || pending.Trace.Intake == nil {
		t.Fatalf("the detached start's intake is not on the item: %+v", pending.Trace)
	}
	if _, err := h.e.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	d := finish()
	steps := d.Named(telemetry.SpanStep)
	if len(steps) != 1 {
		t.Fatalf("%d steps", len(steps))
	}
	linked := 0
	for _, l := range steps[0].Links {
		if l.SpanID == pending.Trace.Intake.SpanID {
			linked++
		}
	}
	if linked != 1 {
		t.Errorf("the step does not link to the start's intake: %+v", steps[0].Links)
	}
	if got := h.item(t, 1); got.Trace.Intake != nil {
		t.Errorf("the intake stays on the item after a step linked to it: %+v", got.Trace)
	}
}

// TestAStepJoinsTheTraceItWasGiven: with TRACEPARENT in the environment the step is its child.
func TestAStepJoinsTheTraceItWasGiven(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	const tp = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	tel := telemetry.Setup(context.Background(), telemetry.Options{Environ: []string{"YNR_SPOOL=" + dir, "TRACEPARENT=" + tp}})
	h.e.Telemetry = tel
	h.f.labels[1] = []string{"ynf:fmt", "pkg:internal/format"}
	if err := h.e.Sweep(tel.Context(context.Background())); err != nil {
		t.Fatal(err)
	}
	tel.Shutdown(context.Background())
	steps := spooltest.Read(t, dir).Named(telemetry.SpanStep)
	if len(steps) != 1 || steps[0].TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || steps[0].ParentID != "00f067aa0ba902b7" {
		t.Fatalf("the step did not join the trace it was given: %+v", steps)
	}
}

func TestMain(m *testing.M) {
	// Telemetry in these tests goes where each test says, never to a developer's own spool.
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); k == "YNR_SPOOL" || strings.HasPrefix(k, "OTEL_") || k == "TRACEPARENT" || k == "TRACESTATE" {
			_ = os.Unsetenv(k)
		}
	}
	os.Exit(m.Run())
}
