// Package spooltest reads a spool folder back, for tests that prove what ynf wrote: the OTLP JSON
// lines of every file in it, parsed into spans, events, log records and metric points.
package spooltest

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Attrs are an attribute list as a map; an integer is an int64, a float a float64.
type Attrs map[string]any

// Str is a string attribute, or "".
func (a Attrs) Str(k string) string { s, _ := a[k].(string); return s }

// Link is a span link.
type Link struct{ TraceID, SpanID string }

// Span is one exported span.
type Span struct {
	Name, TraceID, SpanID, ParentID string
	Attrs                           Attrs
	Links                           []Link
	StatusCode                      int
	Resource                        Attrs
}

// Log is one exported log record: an event when it has an event name.
type Log struct {
	Event, TraceID, SpanID, Body string
	Attrs                        Attrs
	Resource                     Attrs
}

// Point is one metric data point.
type Point struct {
	Metric string
	Attrs  Attrs
	Value  float64
}

// Data is everything in a spool folder.
type Data struct {
	Spans  []Span
	Logs   []Log
	Points []Point
	// Raw is every line, as written, for checks over the whole content.
	Raw string
}

// Read parses every file in dir, closed or open. It fails the test on a line that is not JSON.
func Read(t testing.TB, dir string) Data {
	t.Helper()
	var d Data
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	sort.Strings(files)
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 1<<20), 8<<20)
		for sc.Scan() {
			line := sc.Bytes()
			d.Raw += string(line) + "\n"
			var req struct {
				ResourceSpans []struct {
					Resource   resource `json:"resource"`
					ScopeSpans []struct {
						Spans []struct {
							TraceID, SpanID, ParentSpanID, Name string
							Attributes                          []kv `json:"attributes"`
							Links                               []struct{ TraceID, SpanID string }
							Status                              struct{ Code int }
						} `json:"spans"`
					} `json:"scopeSpans"`
				} `json:"resourceSpans"`
				ResourceLogs []struct {
					Resource  resource `json:"resource"`
					ScopeLogs []struct {
						LogRecords []struct {
							EventName, TraceID, SpanID string
							Body                       struct{ StringValue string }
							Attributes                 []kv `json:"attributes"`
						} `json:"logRecords"`
					} `json:"scopeLogs"`
				} `json:"resourceLogs"`
				ResourceMetrics []struct {
					ScopeMetrics []struct {
						Metrics []struct {
							Name string
							Sum  struct {
								DataPoints []struct {
									Attributes []kv `json:"attributes"`
									AsInt      string
									AsDouble   float64
								} `json:"dataPoints"`
							} `json:"sum"`
						} `json:"metrics"`
					} `json:"scopeMetrics"`
				} `json:"resourceMetrics"`
			}
			if err := json.Unmarshal(line, &req); err != nil {
				t.Fatalf("%s: not JSON: %v", f, err)
			}
			for _, rs := range req.ResourceSpans {
				for _, ss := range rs.ScopeSpans {
					for _, s := range ss.Spans {
						sp := Span{Name: s.Name, TraceID: s.TraceID, SpanID: s.SpanID, ParentID: s.ParentSpanID,
							Attrs: attrs(s.Attributes), StatusCode: s.Status.Code, Resource: attrs(rs.Resource.Attributes)}
						for _, l := range s.Links {
							sp.Links = append(sp.Links, Link{l.TraceID, l.SpanID})
						}
						d.Spans = append(d.Spans, sp)
					}
				}
			}
			for _, rl := range req.ResourceLogs {
				for _, sl := range rl.ScopeLogs {
					for _, l := range sl.LogRecords {
						d.Logs = append(d.Logs, Log{Event: l.EventName, TraceID: l.TraceID, SpanID: l.SpanID, Body: l.Body.StringValue,
							Attrs: attrs(l.Attributes), Resource: attrs(rl.Resource.Attributes)})
					}
				}
			}
			for _, rm := range req.ResourceMetrics {
				for _, sm := range rm.ScopeMetrics {
					for _, m := range sm.Metrics {
						for _, p := range m.Sum.DataPoints {
							v := p.AsDouble
							if p.AsInt != "" {
								var n int64
								_ = json.Unmarshal([]byte(p.AsInt), &n)
								v = float64(n)
							}
							d.Points = append(d.Points, Point{Metric: m.Name, Attrs: attrs(p.Attributes), Value: v})
						}
					}
				}
			}
		}
		_ = fh.Close()
		if err := sc.Err(); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	return d
}

// Named are the spans with a name, in the order written.
func (d Data) Named(name string) []Span {
	var out []Span
	for _, s := range d.Spans {
		if s.Name == name {
			out = append(out, s)
		}
	}
	return out
}

// Events are the log records with an event name.
func (d Data) Events(name string) []Log {
	var out []Log
	for _, l := range d.Logs {
		if l.Event == name {
			out = append(out, l)
		}
	}
	return out
}

// Span is the span with an id, or false.
func (d Data) Span(id string) (Span, bool) {
	for _, s := range d.Spans {
		if s.SpanID == id {
			return s, true
		}
	}
	return Span{}, false
}

type resource struct {
	Attributes []kv `json:"attributes"`
}

type kv struct {
	Key   string `json:"key"`
	Value struct {
		StringValue *string  `json:"stringValue"`
		IntValue    *string  `json:"intValue"`
		BoolValue   *bool    `json:"boolValue"`
		DoubleValue *float64 `json:"doubleValue"`
	} `json:"value"`
}

func attrs(in []kv) Attrs {
	out := Attrs{}
	for _, a := range in {
		switch {
		case a.Value.StringValue != nil:
			out[a.Key] = *a.Value.StringValue
		case a.Value.IntValue != nil:
			var n int64
			_ = json.Unmarshal([]byte(strings.Trim(*a.Value.IntValue, `"`)), &n)
			out[a.Key] = n
		case a.Value.BoolValue != nil:
			out[a.Key] = *a.Value.BoolValue
		case a.Value.DoubleValue != nil:
			out[a.Key] = *a.Value.DoubleValue
		}
	}
	return out
}
