// Package telemetry is ynf's side of ynr's OpenTelemetry contract (ADR-011; ynr ADR-002, 003, 004
// and 006). It sets up the official SDK once at process start, writes where the contract says,
// names everything from the registry in telemetry/registry, and never changes what ynf prints or
// how it exits: its errors are swallowed and counted.
package telemetry

//go:generate go run ./gen -registry ../../telemetry/registry -out names.go

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eyelock/ynr/spoolexporter"
	"github.com/oklog/ulid/v2"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	otellog "go.opentelemetry.io/otel/log"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// Service is the name ynf's records carry as service.name, which with the version identifies
// its registry (ynr ADR-007).
const Service = "ynf"

// scope names the instrumentation scope of everything ynf emits.
const scope = "github.com/eyelock/ynf"

// swallowed counts the SDK's own errors. The SDK's default is to print them, which would change
// ynf's output; telemetry never does (ynr ADR-006, rule 12).
var swallowed atomic.Int64

// Options configures Setup.
type Options struct {
	// Version is service.version: ynf's own.
	Version string
	// Environ is the environment the choice and the resource are read from; default os.Environ.
	Environ []string
	// Watch is for a long-lived process, such as serve: when no spool was found it looks again
	// once a minute, so one that starts after ynf is still found.
	Watch bool
	// WatchEvery is how often Watch looks; default a minute.
	WatchEvery time.Duration
	// FlushTimeout bounds the flush at exit; default 2 seconds.
	FlushTimeout time.Duration
}

// T is a process's telemetry. Every method is safe on a nil T, which writes nothing.
type T struct {
	opts Options
	res  *resource.Resource

	cur     atomic.Pointer[bundle]
	limiter limiter

	stop chan struct{}
	once sync.Once
	wg   sync.WaitGroup
}

// bundle is one set of providers and where they write. The no-op bundle has none.
type bundle struct {
	choice Choice
	tracer trace.Tracer
	tp     *sdktrace.TracerProvider
	lp     *sdklog.LoggerProvider
	mp     *sdkmetric.MeterProvider
	w      *spoolexporter.Writer
	logger otellog.Logger
	slog   slog.Handler
	m      instruments
}

// Setup sets up telemetry once, at process start, choosing where to write by Choose. Call
// Shutdown at exit.
func Setup(ctx context.Context, o Options) *T {
	if o.Environ == nil {
		o.Environ = os.Environ()
	}
	if o.FlushTimeout <= 0 {
		o.FlushTimeout = 2 * time.Second
	}
	if o.WatchEvery <= 0 {
		o.WatchEvery = time.Minute
	}
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(error) { swallowed.Add(1) }))
	t := &T{opts: o, stop: make(chan struct{})}
	// The environment's resource first, so ynf's three identifying attributes win over it.
	t.res, _ = resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithAttributes(
			semconv.ServiceName(Service),
			semconv.ServiceVersion(o.Version),
			semconv.ServiceInstanceID(ulid.Make().String()),
		))
	c := Choose(o.Environ)
	t.cur.Store(t.build(ctx, c))
	if o.Watch && c.Mode == ModeNone {
		t.wg.Add(1)
		go t.watch()
	}
	return t
}

// Context joins the trace this process was given, in TRACEPARENT and TRACESTATE (ynr ADR-006,
// rule 3). With none, ctx is returned as it is and the first span starts a trace.
func (t *T) Context(ctx context.Context) context.Context {
	if t == nil {
		return ctx
	}
	return FromEnviron(ctx, t.opts.Environ)
}

// Choice is where this process writes now.
func (t *T) Choice() Choice {
	if t == nil {
		return Choice{Mode: ModeNone}
	}
	return t.cur.Load().choice
}

// Active reports whether telemetry is being written, or may be soon: the spans, calls and
// copies of log records are worth making. When it is not, ynf does exactly what it did before.
func (t *T) Active() bool {
	return t != nil && (t.cur.Load().choice.Mode != ModeNone || t.opts.Watch)
}

// Tracer is ynf's tracer. It follows the providers, so spans started after a spool is found
// reach it.
func (t *T) Tracer() trace.Tracer {
	if t == nil {
		return tracenoop.NewTracerProvider().Tracer(scope)
	}
	return tracer{t: t}
}

type tracer struct {
	embedded.Tracer
	t *T
}

func (tr tracer) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return tr.t.cur.Load().tracer.Start(ctx, name, opts...)
}

// Event writes an event: a log record with an event name, correlated to the span in ctx, so it is
// inside that span's unit of work. The attributes are names from the registry.
func (t *T) Event(ctx context.Context, name string, attrs ...attribute.KeyValue) {
	if t == nil {
		return
	}
	b := t.cur.Load()
	if b.logger == nil {
		return
	}
	var r otellog.Record
	r.SetTimestamp(time.Now())
	r.SetEventName(name)
	r.SetSeverity(otellog.SeverityInfo)
	r.AddAttributes(attrs...)
	b.logger.Emit(ctx, r)
}

// Shutdown ends telemetry with a bounded flush (ynr ADR-006, rule 12): the providers' ForceFlush,
// then the spool's Sync, then the providers' Shutdown, then the spool's Close. It never fails.
func (t *T) Shutdown(ctx context.Context) {
	if t == nil {
		return
	}
	t.once.Do(func() { close(t.stop) })
	t.wg.Wait()
	b := t.cur.Swap(t.noop())
	ctx, cancel := context.WithTimeout(ctx, t.opts.FlushTimeout)
	defer cancel()
	if b.tp != nil {
		_ = b.tp.ForceFlush(ctx)
	}
	if b.lp != nil {
		_ = b.lp.ForceFlush(ctx)
	}
	if b.mp != nil {
		_ = b.mp.ForceFlush(ctx)
	}
	if b.w != nil {
		b.w.Sync()
	}
	if b.tp != nil {
		_ = b.tp.Shutdown(ctx)
	}
	if b.lp != nil {
		_ = b.lp.Shutdown(ctx)
	}
	if b.mp != nil {
		_ = b.mp.Shutdown(ctx)
	}
	if b.w != nil {
		b.w.Close()
	}
}

// Swallowed is how many errors the SDK raised that ynf did not show.
func Swallowed() int64 { return swallowed.Load() }

func (t *T) watch() {
	defer t.wg.Done()
	tick := time.NewTicker(t.opts.WatchEvery)
	defer tick.Stop()
	for {
		select {
		case <-t.stop:
			return
		case <-tick.C:
			if c := Choose(t.opts.Environ); c.Mode != ModeNone {
				t.cur.Store(t.build(context.Background(), c))
				return
			}
		}
	}
}

func (t *T) noop() *bundle {
	b := &bundle{choice: Choice{Mode: ModeNone}, tracer: tracenoop.NewTracerProvider().Tracer(scope)}
	b.m = newInstruments(metricnoop.NewMeterProvider().Meter(scope))
	return b
}

func (t *T) build(ctx context.Context, c Choice) *bundle {
	var (
		te  sdktrace.SpanExporter
		le  sdklog.Exporter
		me  sdkmetric.Exporter
		w   *spoolexporter.Writer
		err error
	)
	switch c.Mode {
	case ModeSpool:
		w = spoolexporter.NewWriter(spoolexporter.Options{Dir: c.Dir, Service: Service, InstanceID: instanceID(t.res)})
		te, le = spoolexporter.NewTraceExporter(w), spoolexporter.NewLogExporter(w)
		// Delta, so a quiet second writes nothing: a long-lived serve would otherwise repeat every
		// total once a second and fill the writer's cap.
		me = spoolexporter.NewMetricExporter(w, spoolexporter.WithTemporality(sdkmetric.DeltaTemporalitySelector))
	case ModeOTLP:
		// The operator's endpoint, headers and timeouts come from their OTEL_EXPORTER_OTLP_*
		// variables, which these exporters read themselves. HTTP only.
		if te, err = otlptracehttp.New(ctx); err != nil {
			te = nil
		}
		if le, err = otlploghttp.New(ctx); err != nil {
			le = nil
		}
		if me, err = otlpmetrichttp.New(ctx); err != nil {
			me = nil
		}
	default:
		return t.noop()
	}
	b := &bundle{choice: c, w: w, tracer: tracenoop.NewTracerProvider().Tracer(scope)}
	if te != nil {
		b.tp = sdktrace.NewTracerProvider(sdktrace.WithResource(t.res),
			sdktrace.WithBatcher(te, sdktrace.WithBatchTimeout(time.Second)))
		b.tracer = b.tp.Tracer(scope)
	}
	if le != nil {
		b.lp = sdklog.NewLoggerProvider(sdklog.WithResource(t.res),
			sdklog.WithProcessor(sdklog.NewBatchProcessor(le, sdklog.WithExportInterval(time.Second))))
		b.logger = b.lp.Logger(scope)
		b.slog = otelslog.NewHandler(scope, otelslog.WithLoggerProvider(b.lp))
	}
	meter := metricnoop.NewMeterProvider().Meter(scope)
	if me != nil {
		b.mp = sdkmetric.NewMeterProvider(sdkmetric.WithResource(t.res),
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(me, sdkmetric.WithInterval(time.Second))))
		meter = b.mp.Meter(scope)
	}
	b.m = newInstruments(meter)
	return b
}

func instanceID(r *resource.Resource) string {
	if v, ok := r.Set().Value(semconv.ServiceInstanceIDKey); ok {
		return v.AsString()
	}
	return ""
}
