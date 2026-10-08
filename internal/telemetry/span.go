package telemetry

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Finish ends a unit of work's span with ynf's own outcome as an attribute and the span's status
// set from it (ynr ADR-006, rule 11). A failure carries the outcome as its status description,
// never an error's text, which could hold content.
func Finish(span trace.Span, outcome string, attrs ...attribute.KeyValue) {
	span.SetAttributes(append(attrs, attribute.String(AttrOutcome, outcome))...)
	switch Status(outcome) {
	case codes.Error:
		span.SetStatus(codes.Error, outcome)
	case codes.Ok:
		span.SetStatus(codes.Ok, "")
	}
	span.End()
}

// Status is the span status an outcome gives. A step another instance holds is neither a success
// nor an error, so it is left unset.
func Status(outcome string) codes.Code {
	switch outcome {
	case OutcomeConverged, OutcomeCompleted, OutcomeClaimed, OutcomeOk, OutcomeAccepted, OutcomeDeduplicated:
		return codes.Ok
	case OutcomeHeld:
		return codes.Unset
	}
	return codes.Error
}

// Result is the outcome a call or phase ended with: ok, or failed with err.
func Result(err error) string {
	if err != nil {
		return OutcomeFailed
	}
	return OutcomeOk
}

// Call spans a call out to another system (ynr ADR-006, rule 6): a child span of the work that
// made it, with the system and the operation, and an outcome from err. An error in benign, such
// as a not-found that is an answer, is a success. The error's text never reaches the span.
func Call[V any](ctx context.Context, t *T, system, operation string, fn func(context.Context) (V, error), benign ...error) (V, error) {
	ctx, span := t.Tracer().Start(ctx, SpanCall, trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String(AttrCallSystem, system), attribute.String(AttrCallOperation, operation)))
	v, err := fn(ctx)
	failed := err
	for _, b := range benign {
		if errors.Is(err, b) {
			failed = nil
		}
	}
	Finish(span, Result(failed))
	return v, err
}

// IDs are the trace and span ids of span, as lowercase hex, to keep on an item so a later step
// can link to it from another process. A span nobody records, such as a no-op provider's, has
// none: what it carries is its parent's.
func IDs(span trace.Span) (traceID, spanID string) {
	sc := span.SpanContext()
	if !span.IsRecording() || !sc.IsValid() {
		return "", ""
	}
	return sc.TraceID().String(), sc.SpanID().String()
}

// LinkTo is a span link to the span with these ids, or false when they are not a span's.
func LinkTo(traceID, spanID string) (trace.Link, bool) {
	tid, err := trace.TraceIDFromHex(traceID)
	if err != nil {
		return trace.Link{}, false
	}
	sid, err := trace.SpanIDFromHex(spanID)
	if err != nil {
		return trace.Link{}, false
	}
	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled, Remote: true})
	if !sc.IsValid() {
		return trace.Link{}, false
	}
	return trace.Link{SpanContext: sc}, true
}

type intakeKey struct{}

// WithIntake notes in ctx the ynf.intake span of the event the work in ctx is for, so the step it
// starts links to it.
func WithIntake(ctx context.Context, span trace.Span) context.Context {
	if !span.IsRecording() || !span.SpanContext().IsValid() {
		return ctx
	}
	return context.WithValue(ctx, intakeKey{}, span.SpanContext())
}

// Intake is the span context WithIntake noted in ctx.
func Intake(ctx context.Context) (trace.SpanContext, bool) {
	sc, ok := ctx.Value(intakeKey{}).(trace.SpanContext)
	return sc, ok
}
