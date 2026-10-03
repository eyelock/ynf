package policy_test

import (
	"os"
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/policy"
)

func TestSandboxLanesLoad(t *testing.T) {
	doc, err := os.ReadFile("../../sandbox/seed/.agents/factory/lanes.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f, err := policy.Load(doc)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(f.Names(), ",")
	if got != "deps,doc-drift,fix-ci,gofmt,lint-paydown,reclaim" {
		t.Fatalf("lanes %s", got)
	}
	g := f.Lanes["gofmt"]
	if g.Run.Runner != "command" || g.Run.Executor != "docker" || g.Attempts != 3 {
		t.Fatalf("gofmt defaults not applied: %+v", g.Run)
	}
	if len(g.Run.Egress.Allow) != 0 {
		t.Fatalf("gofmt egress %v, want none (its own empty list beats the default)", g.Run.Egress.Allow)
	}
	if f.Lanes["deps"].On() {
		t.Fatal("deps should be off")
	}
	if r := f.Lanes["lint-paydown"].When["ci_failed"]; r.Retry != 2 || r.Then != "escalate" {
		t.Fatalf("ci_failed reaction %+v", r)
	}
	if r := f.Lanes["gofmt"].When["converged"]; r.Action != "open_pr" {
		t.Fatalf("converged reaction %+v", r)
	}
	if g.Hash() == f.Lanes["lint-paydown"].Hash() || g.Hash() != f.Lanes["gofmt"].Hash() {
		t.Fatal("hash should be per lane and stable")
	}
}

func TestSchemaRejectsHandEditingMistakes(t *testing.T) {
	for name, doc := range map[string]string{
		"bare on key":     "version: 1\nlanes:\n  x:\n    kind: originate\n    intake: [{github.search: q, every: 5m}]\n    run: {runner: command, command: {argv: [true]}}\n    on: {converged: open_pr}\n",
		"unknown action":  "version: 1\nlanes:\n  x:\n    kind: originate\n    intake: [{github.search: q, every: 5m}]\n    run: {runner: command, command: {argv: [true]}}\n    when: {converged: merge_it}\n",
		"runner no block": "version: 1\nlanes:\n  x:\n    kind: originate\n    intake: [{github.search: q, every: 5m}]\n    run: {runner: ynh}\n    when: {converged: open_pr}\n",
	} {
		if _, err := policy.Load([]byte(doc)); err == nil {
			t.Errorf("%s: loaded, want a schema error", name)
		}
	}
}

func TestExpandOnlyFromSafeLabels(t *testing.T) {
	got, err := policy.Expand("gofmt -w ./{label.pkg}", []string{"ynf:fmt", "pkg:internal/format"})
	if err != nil || got != "gofmt -w ./internal/format" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := policy.Expand("x {label.pkg}", []string{"pkg:a;rm -rf /"}); err == nil {
		t.Fatal("unsafe label value accepted")
	}
	if _, err := policy.Expand("x {label.pkg}", nil); err == nil {
		t.Fatal("missing label accepted")
	}
}

func TestGuard(t *testing.T) {
	facts := map[string]any{"ticket": map[string]any{"labels": []any{"ynf:fmt"}, "state": "open"}}
	ok, err := policy.Guard(`facts.ticket.state == "open" && facts.ticket.labels.exists(l, l == "ynf:fmt")`, facts, map[string]any{})
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	if _, err := policy.Guard(`facts.ticket.state +`, facts, nil); err == nil {
		t.Fatal("syntax error accepted")
	}
}

func TestResolveShadows(t *testing.T) {
	exists := map[string]bool{".ynh/ynf": true, ".ynf": true}
	dir, shadowed := policy.Resolve(func(d string) bool { return exists[d] })
	if dir != ".ynh/ynf" || len(shadowed) != 1 || shadowed[0] != ".ynf" {
		t.Fatalf("%s %v", dir, shadowed)
	}
}

func TestParseDuration(t *testing.T) {
	for in, want := range map[string]string{"5m": "5m0s", "90d": "2160h0m0s", "30s": "30s"} {
		d, err := policy.ParseDuration(in)
		if err != nil || d.String() != want {
			t.Errorf("%s: %v %v", in, d, err)
		}
	}
	if _, err := policy.ParseDuration("xd"); err == nil {
		t.Error("xd parsed")
	}
	l := policy.Intake{Every: "5m"}
	if l.Interval().Minutes() != 5 {
		t.Error("Interval")
	}
}

func TestDraftDefaultAndGuardTypes(t *testing.T) {
	if !(policy.PR{}).IsDraft() {
		t.Fatal("pull requests should be drafts by default")
	}
	f := false
	if (policy.PR{Draft: &f}).IsDraft() {
		t.Fatal("draft: false ignored")
	}
	if ok, err := policy.Guard("", nil, nil); !ok || err != nil {
		t.Fatal("empty guard should pass")
	}
	if _, err := policy.Guard(`"text"`, map[string]any{}, map[string]any{}); err == nil {
		t.Fatal("non-bool guard accepted")
	}
}
