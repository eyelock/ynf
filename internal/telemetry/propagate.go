package telemetry

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

var prop = propagation.TraceContext{}

// envCarrier reads and writes W3C trace context as the TRACEPARENT and TRACESTATE variables.
type envCarrier map[string]string

func (c envCarrier) Get(key string) string { return c[strings.ToUpper(key)] }
func (c envCarrier) Set(key, v string)     { c[strings.ToUpper(key)] = v }
func (c envCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}

// FromEnviron joins the trace a parent gave this process: TRACEPARENT and TRACESTATE in environ.
// With none, ctx is returned as it is, and the first span starts a trace.
func FromEnviron(ctx context.Context, environ []string) context.Context {
	c := envCarrier{}
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && (k == "TRACEPARENT" || k == "TRACESTATE") {
			c[k] = v
		}
	}
	if len(c) == 0 {
		return ctx
	}
	return prop.Extract(ctx, c)
}

// Inject sets TRACEPARENT and, when there is one, TRACESTATE in env from the span in ctx, for a
// process ynf starts (ynr ADR-006, rule 4). It leaves env alone when ctx carries no trace.
func Inject(ctx context.Context, env map[string]string) {
	c := envCarrier{}
	prop.Inject(ctx, c)
	for k, v := range c {
		env[k] = v
	}
}

// Environ is environ with the trace context of ctx set, replacing any the parent gave, for a
// process ynf starts. It returns environ unchanged when ctx carries no trace.
func Environ(ctx context.Context, environ []string) []string {
	c := envCarrier{}
	prop.Inject(ctx, c)
	if len(c) == 0 {
		return environ
	}
	out := make([]string, 0, len(environ)+2)
	for _, kv := range environ {
		if k, _, _ := strings.Cut(kv, "="); k != "TRACEPARENT" && k != "TRACESTATE" {
			out = append(out, kv)
		}
	}
	for _, k := range []string{"TRACEPARENT", "TRACESTATE"} {
		if v, ok := c[k]; ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// Transport is an http.RoundTripper that sends the W3C trace-context headers of the request's
// context, for every HTTP call ynf makes to another system.
func Transport(next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return headers{next}
}

type headers struct{ next http.RoundTripper }

func (h headers) RoundTrip(r *http.Request) (*http.Response, error) {
	if !trace.SpanContextFromContext(r.Context()).IsValid() {
		return h.next.RoundTrip(r)
	}
	r = r.Clone(r.Context())
	prop.Inject(r.Context(), propagation.HeaderCarrier(r.Header))
	return h.next.RoundTrip(r)
}

// Command gives a process ynf is about to start the trace context of ctx in its environment
// (ynr ADR-006, rule 4), keeping the environment it would have had. It does nothing when ctx
// carries no trace, so with telemetry off and no parent the process inherits exactly as before.
func Command(ctx context.Context, c *exec.Cmd) {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return
	}
	base := c.Env
	if base == nil {
		base = os.Environ()
	}
	c.Env = Environ(ctx, base)
}

// FromHeaders joins the trace a client sent in W3C trace-context headers, for a request ynf
// serves (ynr ADR-006, rule 3).
func FromHeaders(ctx context.Context, h http.Header) context.Context {
	return prop.Extract(ctx, propagation.HeaderCarrier(h))
}
