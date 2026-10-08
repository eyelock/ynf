package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eyelock/ynf/internal/config"
)

func loadTelemetry(t *testing.T, block string) (*config.Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	write(t, p, "version: 1\nrepos: [o/r]\n"+block)
	return config.Load(p)
}

func TestTelemetrySettingsAreOffByDefault(t *testing.T) {
	c, err := loadTelemetry(t, "")
	if err != nil {
		t.Fatal(err)
	}
	ts, err := c.TelemetrySettings(os.Getenv)
	if err != nil || ts.Root != "" || ts.Collector.Enabled {
		t.Fatalf("no block: %+v %v", ts, err)
	}
	// A spool alone does not turn the collector on.
	c, err = loadTelemetry(t, "telemetry:\n  spool: spool\n")
	if err != nil {
		t.Fatal(err)
	}
	ts, _ = c.TelemetrySettings(os.Getenv)
	if ts.Root != filepath.Join(c.Dir(), "spool") || ts.Collector.Enabled || ts.Quota != 0 {
		t.Fatalf("a spool root, relative to the config file: %+v", ts)
	}
}

func TestTelemetrySettingsResolve(t *testing.T) {
	c, err := loadTelemetry(t, `telemetry:
  spool: /var/spool/ynf
  run_quota: 8MiB
  collector:
    enabled: true
    id: gha-pool-1
    instance: job-42
    upstream: http://central:4318
    archive: 45s
`)
	if err != nil {
		t.Fatal(err)
	}
	ts, err := c.TelemetrySettings(func(string) string { return "http://ignored:4318" })
	if err != nil {
		t.Fatal(err)
	}
	col := ts.Collector
	if ts.Root != "/var/spool/ynf" || ts.Quota != 8<<20 || !col.Enabled || col.ID != "gha-pool-1" || col.Instance != "job-42" ||
		col.Upstream != "http://central:4318" || col.Archive != 45*time.Second {
		t.Fatalf("%+v", ts)
	}
}

func TestTheCollectorsDefaults(t *testing.T) {
	for _, k := range []string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "YNR_UPSTREAM"} {
		t.Setenv(k, "")
	}
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	c, err := loadTelemetry(t, "telemetry:\n  spool: /s\n  collector: {enabled: true, id: pool}\n")
	if err == nil {
		t.Fatalf("no upstream anywhere loaded: %+v", c)
	}
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://operator:4318")
	c, err = loadTelemetry(t, "telemetry:\n  spool: /s\n  collector: {enabled: true, id: pool}\n")
	if err != nil {
		t.Fatal(err)
	}
	ts, _ := c.TelemetrySettings(os.Getenv)
	if ts.Collector.Upstream != "http://operator:4318" || ts.Collector.Archive != 30*time.Second || ts.Collector.Instance != "" {
		t.Fatalf("the operator's endpoint is the upstream, and the archive time 30s: %+v", ts.Collector)
	}
}

func TestACollectorWithNoUpstreamFailsAtLoadWithAClearMessage(t *testing.T) {
	for _, k := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "YNR_UPSTREAM"} {
		t.Setenv(k, "")
	}
	_, err := loadTelemetry(t, "telemetry:\n  spool: /s\n  collector: {enabled: true, id: pool}\n")
	if err == nil || !strings.Contains(err.Error(), "no upstream") || !strings.Contains(err.Error(), "OTEL_EXPORTER_OTLP_ENDPOINT") {
		t.Fatalf("want a message that names the upstream and the variable: %v", err)
	}
	// Disabled, it asks for nothing.
	if _, err := loadTelemetry(t, "telemetry:\n  spool: /s\n  collector: {enabled: false}\n"); err != nil {
		t.Fatalf("a disabled collector needs nothing: %v", err)
	}
}

func TestTelemetryRejects(t *testing.T) {
	for name, block := range map[string]string{
		"an id is needed":         "telemetry:\n  spool: /s\n  collector: {enabled: true, upstream: 'http://u'}\n",
		"a spool is needed":       "telemetry:\n  collector: {enabled: true, id: p, upstream: 'http://u'}\n",
		"an odd size":             "telemetry:\n  spool: /s\n  run_quota: lots\n",
		"a zero size":             "telemetry:\n  spool: /s\n  run_quota: 0MiB\n",
		"an upstream that is not": "telemetry:\n  spool: /s\n  collector: {enabled: true, id: p, upstream: central}\n",
		"a bad archive time":      "telemetry:\n  spool: /s\n  collector: {enabled: true, id: p, upstream: 'http://u', archive: soon}\n",
		"an unknown key":          "telemetry:\n  spool: /s\n  collector: {enabled: true, id: p, upstream: 'http://u', pool: x}\n",
		"a collector id with a /": "telemetry:\n  spool: /s\n  collector: {enabled: true, id: a/b, upstream: 'http://u'}\n",
	} {
		if _, err := loadTelemetry(t, block); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"1KiB": 1 << 10, "64MiB": 64 << 20, "2GiB": 2 << 30} {
		if got, err := config.ParseSize(in); err != nil || got != want {
			t.Errorf("%s: %d %v", in, got, err)
		}
	}
	for _, in := range []string{"", "64", "MiB", "-1MiB", "1.5MiB", "1MB", "0KiB"} {
		if _, err := config.ParseSize(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}
