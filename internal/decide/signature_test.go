package decide_test

import (
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/eyelock/ynf/internal/decide"
	"github.com/eyelock/ynf/internal/event"
	"github.com/eyelock/ynf/internal/facts"
	"github.com/eyelock/ynf/internal/item"
)

// sigs is the failure signatures an item has counted, sorted.
func sigs(it item.Item) []string {
	var out []string
	for name := range it.Counters {
		if strings.HasPrefix(name, "sig/") {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

func finishedRun(t *testing.T, data map[string]any) item.Item {
	t.Helper()
	d := decide.Decide(decide.Input{Lane: lanes(t).Lanes["lint-paydown"], Item: item.Item{State: item.Running}, Facts: facts.Facts{Ticket: open()},
		Event: ev(event.RunFinished, data), Poll: decide.Poll{CI: time.Minute}})
	return d.Item
}

func TestRunSignatures(t *testing.T) {
	for _, tc := range []struct {
		name string
		data map[string]any
		want []string
	}{
		{"budget names the cap and the harness", map[string]any{"outcome": "budget", "bound_by": "turns", "harness": "local/ynf-sandbox@0.1.0"},
			[]string{"sig/budget/turns/harness:local/ynf-sandbox@0.1.0"}},
		{"budget without a harness", map[string]any{"outcome": "budget", "bound_by": "wall"}, []string{"sig/budget/wall"}},
		{"budget without bound_by falls back", map[string]any{"outcome": "budget"}, []string{"sig/outcome/budget"}},
		{"stuck names each failing sensor, sorted", map[string]any{"outcome": "stuck", "failed_sensors": []any{"vet", "Unit Tests", "vet"}},
			[]string{"sig/stuck/sensor:unit-tests", "sig/stuck/sensor:vet"}},
		{"stuck with no sensors falls back", map[string]any{"outcome": "stuck", "failed_sensors": []any{}}, []string{"sig/outcome/stuck"}},
		{"a cap and a sensor are both named, and the fallback is not", map[string]any{"outcome": "budget", "bound_by": "tokens", "failed_sensors": []any{"lint"}},
			[]string{"sig/budget/tokens", "sig/stuck/sensor:lint"}},
		{"an older event has none of the new data", map[string]any{"outcome": "error", "detail": "exit 2"}, []string{"sig/outcome/error"}},
		{"converged counts no failure", map[string]any{"outcome": "converged", "changed": []any{"a.go"}, "bound_by": "turns"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sigs(finishedRun(t, tc.data)); !slices.Equal(got, tc.want) {
				t.Fatalf("%v, want %v", got, tc.want)
			}
		})
	}
}

func TestSignaturesAreCapped(t *testing.T) {
	it := finishedRun(t, map[string]any{"outcome": "stuck", "failed_sensors": []any{strings.Repeat("é", 300)}})
	got := sigs(it)
	if len(got) != 1 || len(got[0]) > 200 || !utf8.ValidString(got[0]) || !strings.HasPrefix(got[0], "sig/stuck/sensor:é") {
		t.Fatalf("%q", got)
	}
}

func TestCISignatures(t *testing.T) {
	lane := lanes(t).Lanes["lint-paydown"]
	failing := func(it item.Item, names ...string) []string {
		p := &facts.PR{Number: 7, State: "open", HeadSHA: "abc"}
		for _, n := range names {
			p.Checks = append(p.Checks, facts.Check{Name: n, Status: "completed", Conclusion: "failure"})
		}
		it.State, it.PR = item.Proposed, 7
		d := decide.Decide(decide.Input{Lane: lane, Item: it, Facts: facts.Facts{Ticket: open(), PR: p}, Event: ev(event.TimerDue, nil), Poll: decide.Poll{CI: time.Minute}})
		return sigs(d.Item)
	}
	converged := item.Item{LastRun: &item.Run{ID: "r", Outcome: "converged"}}
	if got := failing(converged, "test", "golangci-lint"); !slices.Equal(got, []string{"sig/ci-diverges/golangci-lint", "sig/ci-diverges/test"}) {
		t.Errorf("converged then failed: one signature per check: %v", got)
	}
	if got := failing(item.Item{Kind: "adopt"}, "Lint Go", "test"); !slices.Equal(got, []string{"sig/ci/lint-go", "sig/ci/test"}) {
		t.Errorf("an adopted pull request never converged here: %v", got)
	}
	if got := failing(item.Item{LastRun: &item.Run{Outcome: "stuck"}}, "test"); !slices.Equal(got, []string{"sig/ci/test"}) {
		t.Errorf("last run did not converge: %v", got)
	}
}
