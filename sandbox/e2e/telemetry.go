package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// The OpenTelemetry proof (ADR-011, ynr ADR-002): ynf runs with YNR_SPOOL set to a temporary
// folder (or, with ynr, a spool root and a collector: see collector.go), and what was written,
// read back as JSON lines, must show a gofmt step as one trace with
// its spans and attributes, an item's second step linked to its first, and every received
// CloudEvent mirrored once. It reads files only: nothing in ynf reads telemetry back.

type otlpAttr struct {
	Key   string `json:"key"`
	Value struct {
		StringValue string `json:"stringValue"`
		IntValue    string `json:"intValue"`
	} `json:"value"`
}

type spoolSpan struct {
	Name         string     `json:"name"`
	TraceID      string     `json:"traceId"`
	SpanID       string     `json:"spanId"`
	ParentSpanID string     `json:"parentSpanId"`
	Attributes   []otlpAttr `json:"attributes"`
	Links        []struct {
		TraceID string `json:"traceId"`
		SpanID  string `json:"spanId"`
	} `json:"links"`
	// Res is the span's resource attributes: for a record that went through ynr, what it stamped.
	Res map[string]string `json:"-"`
}

func (s spoolSpan) attr(k string) string {
	for _, a := range s.Attributes {
		if a.Key == k {
			if a.Value.StringValue != "" {
				return a.Value.StringValue
			}
			return a.Value.IntValue
		}
	}
	return ""
}

type spoolEvent struct {
	Name, SpanID string
	Attrs        []otlpAttr
	Res          map[string]string
}

type spoolData struct {
	spans  []spoolSpan
	events []spoolEvent
}

func readSpool(dir string) (spoolData, error) {
	var d spoolData
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			return d, err
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 1<<20), 8<<20)
		for sc.Scan() {
			var req struct {
				ResourceSpans []struct {
					Resource struct {
						Attributes []otlpAttr `json:"attributes"`
					} `json:"resource"`
					ScopeSpans []struct {
						Spans []spoolSpan `json:"spans"`
					} `json:"scopeSpans"`
				} `json:"resourceSpans"`
				ResourceLogs []struct {
					ScopeLogs []struct {
						LogRecords []struct {
							EventName  string     `json:"eventName"`
							SpanID     string     `json:"spanId"`
							Attributes []otlpAttr `json:"attributes"`
						} `json:"logRecords"`
					} `json:"scopeLogs"`
				} `json:"resourceLogs"`
			}
			if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
				_ = fh.Close()
				return d, fmt.Errorf("%s: %w", f, err)
			}
			for _, rs := range req.ResourceSpans {
				res := map[string]string{}
				for _, a := range rs.Resource.Attributes {
					res[a.Key] = a.Value.StringValue + a.Value.IntValue
				}
				for _, ss := range rs.ScopeSpans {
					for _, s := range ss.Spans {
						s.Res = res
						d.spans = append(d.spans, s)
					}
				}
			}
			for _, rl := range req.ResourceLogs {
				for _, sl := range rl.ScopeLogs {
					for _, l := range sl.LogRecords {
						if l.EventName != "" {
							d.events = append(d.events, spoolEvent{Name: l.EventName, SpanID: l.SpanID, Attrs: l.Attributes})
						}
					}
				}
			}
		}
		_ = fh.Close()
	}
	return d, nil
}

// checkSpool proves the spool ynf wrote, when there is no collector.
func checkSpool(spool string, keys []string, lane string) error {
	d, err := readSpool(spool)
	if err != nil {
		return err
	}
	return checkTelemetry(d, keys, lane)
}

// checkTelemetry proves what was written, from the spool or as a collector shipped it. keys are
// the items of the lanes this run covered.
func checkTelemetry(d spoolData, keys []string, lane string) error {
	byID := map[string]spoolSpan{}
	for _, s := range d.spans {
		byID[s.SpanID] = s
	}

	// Every intake event was mirrored once: one received event on each intake span, with distinct
	// CloudEvent ids, and every step linked to an intake span.
	received := map[string]int{}
	ids := map[string]bool{}
	for _, e := range d.events {
		if e.Name != "ynf.intake.received" {
			continue
		}
		received[e.SpanID]++
		for _, a := range e.Attrs {
			if a.Key == "cloudevents.event_id" {
				if ids[a.Value.StringValue] {
					return fmt.Errorf("telemetry: CloudEvent %s was mirrored twice", a.Value.StringValue)
				}
				ids[a.Value.StringValue] = true
			}
		}
	}
	intakes := 0
	for _, s := range d.spans {
		if s.Name == "ynf.intake" {
			intakes++
			if received[s.SpanID] != 1 {
				return fmt.Errorf("telemetry: intake span %s carries %d ynf.intake.received events, want 1", s.SpanID, received[s.SpanID])
			}
		}
	}
	if intakes == 0 || intakes != len(ids) {
		return fmt.Errorf("telemetry: %d intake spans and %d mirrored events", intakes, len(ids))
	}

	// A gofmt step is one trace with its spans and attributes; an item's later step links to its
	// earlier one.
	checked, linked := 0, 0
	for _, key := range keys {
		var steps []spoolSpan
		for _, s := range d.spans {
			if s.Name == "ynf.step" && s.attr("ynf.item.key") == key {
				steps = append(steps, s)
			}
		}
		for i, st := range steps {
			if st.attr("ynf.outcome") == "held" {
				continue // another instance had it: no claim, so nothing else
			}
			intake := false
			for _, l := range st.Links {
				if byID[l.SpanID].Name == "ynf.intake" {
					intake = true
				}
			}
			if !intake {
				return fmt.Errorf("telemetry: %s step %d links to no ynf.intake span", key, i+1)
			}
			for _, k := range []string{"ynf.step.id", "ynf.policy.hash", "ynf.lease.epoch", "ynf.repo"} {
				if st.attr(k) == "" {
					return fmt.Errorf("telemetry: %s step lacks %s", key, k)
				}
			}
			if !strings.HasSuffix(st.attr("ynf.lane"), "#"+lane) || !strings.HasPrefix(st.attr("ynf.repo"), "github.com/") {
				return fmt.Errorf("telemetry: %s step lane %q repo %q", key, st.attr("ynf.lane"), st.attr("ynf.repo"))
			}
			names := map[string]bool{}
			for _, s := range d.spans {
				if s.TraceID == st.TraceID {
					names[s.Name] = true
				}
			}
			if i == 0 {
				for _, want := range []string{"ynf.claim", "ynf.probe", "ynf.decide", "ynf.act", "ynf.run", "ynf.call"} {
					if !names[want] {
						var have []string
						for n := range names {
							have = append(have, n)
						}
						sort.Strings(have)
						return fmt.Errorf("telemetry: the first step of %s has no %s span; its trace has %v", key, want, have)
					}
				}
				checked++
			}
			if i > 0 {
				prev := steps[i-1]
				if !slices.ContainsFunc(st.Links, func(l struct {
					TraceID string `json:"traceId"`
					SpanID  string `json:"spanId"`
				}) bool {
					return l.SpanID == prev.SpanID && l.TraceID == prev.TraceID
				}) {
					return fmt.Errorf("telemetry: step %d of %s does not link to step %d", i+1, key, i)
				}
				linked++
			}
		}
	}
	if checked == 0 {
		return fmt.Errorf("telemetry: no %s step was found in the spool", lane)
	}
	if linked == 0 {
		return fmt.Errorf("telemetry: no item had a second step to link to its first")
	}
	fmt.Printf("telemetry: %d item(s) traced, %d later step(s) linked to their previous one, %d intake event(s) each mirrored once\n", checked, linked, intakes)
	return nil
}
