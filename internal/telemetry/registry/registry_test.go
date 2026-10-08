package registry_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/eyelock/ynf/internal/telemetry/registry"
)

const manifest = `name: t
schema_url: https://example.test/t/1
dependencies:
  - name: otel
    schema_url: https://opentelemetry.io/schemas/1.40.0
    registry_path: https://example.test/x.zip[model]
`

func load(t *testing.T, groups string) (*registry.Registry, error) {
	t.Helper()
	return registry.Load(fstest.MapFS{
		"manifest.yaml": {Data: []byte(manifest)},
		"g.yaml":        {Data: []byte(groups)},
	}, "1.2.3")
}

func TestLoadAndGenerate(t *testing.T) {
	r, err := load(t, `groups:
  - id: registry.t
    type: attribute_group
    attributes:
      - {id: t.thing.id, type: string, brief: A thing., stability: development}
      - id: t.kind
        type:
          members:
            - {id: big, value: big, brief: Big.}
        brief: A kind.
        annotations: {ynr.cardinality: 4}
  - id: span.t.work
    type: span
    span_kind: internal
    brief: Work.
    attributes:
      - {ref: t.thing.id, requirement_level: required}
      - {ref: url.full, requirement_level: recommended}
  - id: event.t.began
    type: event
    name: t.began
    brief: Began.
    attributes: [{ref: t.thing.id}]
  - id: metric.t.n
    type: metric
    metric_name: t.n
    instrument: counter
    unit: "{n}"
    brief: Count.
    attributes:
      - {ref: t.kind, requirement_level: required}
`)
	if err != nil {
		t.Fatal(err)
	}
	if r.Tool != "t" || r.Version != "1.2.3" || r.Semconv.Version != "1.40.0" || len(r.Standard) != 1 || r.Standard[0] != "url.full" {
		t.Errorf("%+v", r)
	}
	if r.Metrics[0].Attributes[0].Cardinality != 4 || r.Attributes[1].Type != "enum" || r.Spans[0].Name != "t.work" {
		t.Errorf("%+v", r)
	}
	src, err := registry.Generate(r, "p")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"AttrTThingID = \"t.thing.id\"", "SpanTWork = \"t.work\"", "EventTBegan = \"t.began\"", "MetricTN = \"t.n\"", "TKindBig = \"big\"", "AttrURLFull", "\"t.kind\": 4"} {
		if !strings.Contains(string(src), want) {
			t.Errorf("generated code lacks %s:\n%s", want, src)
		}
	}
}

func TestLoadRefusals(t *testing.T) {
	for name, groups := range map[string]string{
		"not yaml":       "groups: [",
		"no id":          "groups:\n  - {id: g, type: attribute_group, attributes: [{type: string}]}",
		"no type":        "groups:\n  - {id: g, type: attribute_group, attributes: [{id: a.b}]}",
		"unknown group":  "groups:\n  - {id: g, type: entity}",
		"defined in use": "groups:\n  - {id: s, type: span, attributes: [{id: a.b, type: string}]}",
	} {
		if _, err := load(t, groups); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
	if _, err := registry.Load(fstest.MapFS{}, ""); err == nil {
		t.Error("no manifest")
	}
	if _, err := registry.Load(fstest.MapFS{"manifest.yaml": {Data: []byte("name: [")}}, ""); err == nil {
		t.Error("bad manifest")
	}
}
