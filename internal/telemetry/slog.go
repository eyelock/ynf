package telemetry

import (
	"context"
	"log/slog"
	"slices"
)

// Tee sends every record to primary, as it would go anyway, and a copy to ynf's OpenTelemetry
// logs. What primary writes is byte for byte what it wrote without the copy: the copy is made
// after it and its errors are not returned. It sends the copy only for records primary would
// write, so the level people chose applies to both. With no telemetry to write to, it returns
// primary itself.
func Tee(primary slog.Handler, t *T) slog.Handler {
	if t == nil || !t.bridging() {
		return primary
	}
	return tee{primary, bridge{t: t}}
}

// bridging reports whether log records have anywhere to go now or may have: a spool found later
// by a long-lived process counts.
func (t *T) bridging() bool {
	return t.cur.Load().slog != nil || t.opts.Watch && t.cur.Load().choice.Mode == ModeNone
}

type tee struct{ a, b slog.Handler }

func (h tee) Enabled(ctx context.Context, l slog.Level) bool { return h.a.Enabled(ctx, l) }

func (h tee) Handle(ctx context.Context, r slog.Record) error {
	err := h.a.Handle(ctx, r)
	_ = h.b.Handle(ctx, r.Clone())
	return err
}

func (h tee) WithAttrs(as []slog.Attr) slog.Handler { return tee{h.a.WithAttrs(as), h.b.WithAttrs(as)} }
func (h tee) WithGroup(name string) slog.Handler {
	return tee{h.a.WithGroup(name), h.b.WithGroup(name)}
}

// bridge is the OpenTelemetry side: the official otelslog bridge on the current logger provider,
// with what must never be exported removed first. It looks up the provider on every record, so a
// spool found after start is used from then on.
type bridge struct {
	t   *T
	ops []func(slog.Handler) slog.Handler
}

func (b bridge) Enabled(context.Context, slog.Level) bool { return true }

func (b bridge) Handle(ctx context.Context, r slog.Record) error {
	h := b.t.cur.Load().slog
	if h == nil {
		return nil
	}
	for _, op := range b.ops {
		h = op(h)
	}
	return h.Handle(ctx, clean(r))
}

func (b bridge) WithAttrs(as []slog.Attr) slog.Handler {
	as = cleanAttrs(as)
	return bridge{b.t, append(slices.Clone(b.ops), func(h slog.Handler) slog.Handler { return h.WithAttrs(as) })}
}

func (b bridge) WithGroup(name string) slog.Handler {
	return bridge{b.t, append(slices.Clone(b.ops), func(h slog.Handler) slog.Handler { return h.WithGroup(name) })}
}

// withheld are the keys whose values are content or personal, which ynf's logs carry for people
// but telemetry never does (ynr ADR-006, rule 10): what a run reported in its own words, why a
// decision was made, who paused a lane, free text.
var withheld = map[string]bool{
	"detail": true, "reason": true, "by": true, "last": true, "text": true, "body": true,
	"title": true, "prompt": true, "content": true, "argv": true, "stdout": true,
	"stderr": true, "feedback": true, "author": true, "email": true, "name": true,
}

// maxValue bounds a string value that goes to telemetry.
const maxValue = 200

func clean(r slog.Record) slog.Record {
	out := slog.NewRecord(r.Time, r.Level, Scrub(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		if a = cleanAttr(a); a.Key != "" {
			out.AddAttrs(a)
		}
		return true
	})
	return out
}

func cleanAttrs(as []slog.Attr) []slog.Attr {
	out := make([]slog.Attr, 0, len(as))
	for _, a := range as {
		if a = cleanAttr(a); a.Key != "" {
			out = append(out, a)
		}
	}
	return out
}

func cleanAttr(a slog.Attr) slog.Attr {
	a.Value = a.Value.Resolve()
	if withheld[a.Key] {
		return slog.Attr{}
	}
	switch a.Value.Kind() {
	case slog.KindGroup:
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(cleanAttrs(a.Value.Group())...)}
	case slog.KindString:
		return slog.String(a.Key, bound(Scrub(a.Value.String())))
	case slog.KindAny:
		// An error or any other value is its text, scrubbed, never the value itself.
		return slog.String(a.Key, bound(Scrub(a.Value.String())))
	}
	return a
}

func bound(s string) string {
	if len(s) > maxValue {
		return s[:maxValue] + "..."
	}
	return s
}
