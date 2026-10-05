package telemetry_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eyelock/ynf"
	"github.com/eyelock/ynf/internal/telemetry/registry"
)

func loadRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	sub, err := fs.Sub(ynf.TelemetryRegistry, "telemetry/registry")
	if err != nil {
		t.Fatal(err)
	}
	r, err := registry.Load(sub, "test")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestConstantsMatchTheRegistry: names.go is exactly what the registry generates, so a name added
// to the registry without `go generate ./...`, or a constant edited by hand, fails here.
func TestConstantsMatchTheRegistry(t *testing.T) {
	want, err := registry.Generate(loadRegistry(t), "telemetry")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("names.go")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatal("internal/telemetry/names.go is not what telemetry/registry generates: run `go generate ./...`")
	}
}

// TestTheRegistryIsConsistent: every attribute a signal uses is defined by ynf or is an upstream
// name, ynf's own names are under its prefix, and every metric attribute has its limit declared
// where it could grow.
func TestTheRegistryIsConsistent(t *testing.T) {
	r := loadRegistry(t)
	if r.Tool != "ynf" || r.Semconv.Version == "" {
		t.Fatalf("%+v", r)
	}
	known := map[string]bool{}
	for _, a := range r.Attributes {
		known[a.ID] = true
		if !strings.HasPrefix(a.ID, "ynf.") {
			t.Errorf("%s is not under ynf's prefix", a.ID)
		}
	}
	for _, id := range r.Standard {
		known[id] = true
		if strings.HasPrefix(id, "ynf.") {
			t.Errorf("%s is ynf's and must be defined here", id)
		}
	}
	check := func(what string, uses []registry.Use) {
		for _, u := range uses {
			if !known[u.Name] {
				t.Errorf("%s uses unknown attribute %s", what, u.Name)
			}
		}
	}
	for _, s := range r.Spans {
		check(s.Name, s.Attributes)
	}
	for _, e := range r.Events {
		check(e.Name, e.Attributes)
	}
	for _, m := range r.Metrics {
		check(m.Name, m.Attributes)
		for _, u := range m.Attributes {
			switch u.Name {
			case "ynf.item.key", "ynf.step.id", "ynf.run.id":
				t.Errorf("%s: %s is never a metric attribute", m.Name, u.Name)
			case "ynf.lane", "gen_ai.response.model":
				if u.Cardinality == 0 {
					t.Errorf("%s: %s declares no limit", m.Name, u.Name)
				}
			}
		}
	}
}

// TestCodeUsesOnlyTheConstants: no non-test source outside the generated file spells a registered
// name as a string.
func TestCodeUsesOnlyTheConstants(t *testing.T) {
	r := loadRegistry(t)
	var names []string
	for _, a := range r.Attributes {
		names = append(names, a.ID)
	}
	for _, s := range r.Spans {
		names = append(names, s.Name)
	}
	for _, e := range r.Events {
		names = append(names, e.Name)
	}
	for _, m := range r.Metrics {
		names = append(names, m.Name)
	}
	err := filepath.WalkDir(filepath.Join("..", ".."), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") || filepath.Base(p) == "names.go" {
			return err
		}
		if strings.Contains(filepath.ToSlash(p), "/sandbox/") || strings.Contains(filepath.ToSlash(p), "/telemetry/registry/") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, n := range names {
			if strings.Contains(string(b), `"`+n+`"`) {
				t.Errorf("%s spells the registered name %q: use the generated constant", p, n)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
