package facts_test

import (
	"reflect"
	"testing"

	"github.com/eyelock/ynf/internal/facts"
)

func check(name, status, conclusion string, required bool) facts.Check {
	return facts.Check{Name: name, Status: status, Conclusion: conclusion, Required: required}
}

func TestCIState(t *testing.T) {
	cases := []struct {
		name   string
		checks []facts.Check
		want   string
		failed []string
	}{
		{"nothing reported", nil, "pending", nil},
		{"all green", []facts.Check{check("a", "completed", "success", false), check("b", "completed", "skipped", false)}, "success", nil},
		{"one running", []facts.Check{check("a", "completed", "success", false), check("b", "in_progress", "", false)}, "pending", nil},
		{"one red", []facts.Check{check("a", "completed", "failure", false), check("b", "in_progress", "", false)}, "failure", []string{"a"}},
		{"only required gate", []facts.Check{check("req", "completed", "success", true), check("extra", "completed", "failure", false)}, "success", []string{"extra"}},
		{"required red", []facts.Check{check("req", "completed", "timed_out", true)}, "failure", []string{"req"}},
	}
	for _, c := range cases {
		p := facts.PR{Checks: c.checks}
		if got := p.CIState(); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
		if got := p.Failed(); !reflect.DeepEqual(got, c.failed) {
			t.Errorf("%s: failed %v, want %v", c.name, got, c.failed)
		}
	}
}

func TestCELHasNoFreeText(t *testing.T) {
	f := facts.Facts{
		Ticket: &facts.Ticket{Number: 1, State: "open", Labels: []string{"ynf:fmt"}},
		PR:     &facts.PR{Number: 2, State: "open", Checks: []facts.Check{check("lint", "completed", "success", true)}},
	}
	m := f.CEL()
	tk := m["ticket"].(map[string]any)
	if tk["state"] != "open" || len(tk["labels"].([]any)) != 1 {
		t.Fatalf("ticket %v", tk)
	}
	pr := m["pr"].(map[string]any)
	if pr["ci"] != "success" || len(pr["checks"].([]any)) != 1 {
		t.Fatalf("pr %v", pr)
	}
	for _, k := range []string{"title", "body"} {
		if _, ok := tk[k]; ok {
			t.Errorf("ticket exposes %s to guards", k)
		}
	}
	if len((facts.Facts{}).CEL()) != 0 {
		t.Fatal("empty facts should be empty")
	}
}
