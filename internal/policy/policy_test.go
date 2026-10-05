package policy_test

import (
	"os"
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/policy"
)

// TestSandboxLanesLoad loads the sandbox's lanes as ynf does: its own laid over its configuration
// repository's, which enrols it.
func TestSandboxLanesLoad(t *testing.T) {
	own, err := os.ReadFile("../../sandbox/seed/.agents/factory/lanes.yaml")
	if err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile("../../sandbox/factory-seed/.agents/factory/lanes.yaml")
	if err != nil {
		t.Fatal(err)
	}
	fy, err := os.ReadFile("../../sandbox/factory-seed/.agents/factory/factory.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if fac, err := policy.LoadFactory(fy); err != nil || strings.Join(fac.Repos, ",") != "github.com/eyelock/ynf-sandbox" {
		t.Fatalf("the sandbox factory: %+v %v", fac, err)
	}
	if _, err := policy.Load(own); err == nil {
		t.Fatal("the sandbox's lanes alone should need the configuration repository's")
	}
	doc, err := policy.MergeLanes(config, own)
	if err != nil {
		t.Fatal(err)
	}
	f, err := policy.Load(doc)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(f.Names(), ",")
	if got != "deps,detect,doc-drift,fix-ci,gofmt,lint-paydown,reclaim,relaxed" {
		t.Fatalf("lanes %s", got)
	}
	if d := f.Lanes["deps"]; d.On() || d.Kind != "originate" || len(d.Intake) != 1 {
		t.Fatalf("deps comes from the configuration repository and is switched off here: %+v", d)
	}
	if d := f.Lanes["detect"]; d.Run.Runner != "" || d.Run.Ynh == nil || d.Run.Command == nil {
		t.Fatalf("detect names no runner and offers both: %+v", d.Run)
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
		"bare on key":         "version: 1\nlanes:\n  x:\n    kind: originate\n    intake: [{github.search: q, every: 5m}]\n    run: {runner: command, command: {argv: [true]}}\n    on: {converged: open_pr}\n",
		"unknown action":      "version: 1\nlanes:\n  x:\n    kind: originate\n    intake: [{github.search: q, every: 5m}]\n    run: {runner: command, command: {argv: [true]}}\n    when: {converged: merge_it}\n",
		"no runner, no block": "version: 1\nlanes:\n  x:\n    kind: originate\n    intake: [{github.search: q, every: 5m}]\n    run: {executor: docker}\n    when: {converged: open_pr}\n",
		"runner no block":     "version: 1\nlanes:\n  x:\n    kind: originate\n    intake: [{github.search: q, every: 5m}]\n    run: {runner: ynh}\n    when: {converged: open_pr}\n",
	} {
		if _, err := policy.Load([]byte(doc)); err == nil {
			t.Errorf("%s: loaded, want a schema error", name)
		}
	}
}

func TestALaneMayOmitItsRunnerWithABlockToUse(t *testing.T) {
	for name, run := range map[string]string{
		"command": "{command: {argv: [x]}}",
		"ynh":     "{ynh: {harness: .}}",
		"both":    "{ynh: {harness: .}, command: {argv: [x]}}",
	} {
		doc := "version: 1\nlanes:\n  x:\n    kind: originate\n    intake: [{github.search: q, every: 5m}]\n    run: " + run + "\n    when: {converged: open_pr}\n"
		if _, err := policy.Load([]byte(doc)); err != nil {
			t.Errorf("%s: %v", name, err)
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

// TestLabelsInheritPerReaction: a lane's label reactions fall back to the defaults one by one.
func TestLabelsInheritPerReaction(t *testing.T) {
	f, err := policy.Load([]byte(`version: 1
defaults:
  labels:
    on_claim: {remove: [ynf:go]}
    on_escalate: {add: [ynf:needs-human]}
lanes:
  a:
    kind: originate
    intake: [{github.search: "x", every: 5m}]
    run: {runner: command, command: {argv: ["true"]}}
    when: {converged: open_pr}
    labels:
      on_claim: {add: [ynf:working], remove: [ynf:a]}
  b:
    kind: originate
    intake: [{github.search: "x", every: 5m}]
    run: {runner: command, command: {argv: ["true"]}}
    when: {converged: open_pr}
`))
	if err != nil {
		t.Fatal(err)
	}
	a, b := f.Lanes["a"].Labels, f.Lanes["b"].Labels
	if c := a.For("ready"); c == nil || c.Add[0] != "ynf:working" || c.Remove[0] != "ynf:a" {
		t.Fatalf("a's own on_claim: %+v", c)
	}
	if a.For("escalated").Add[0] != "ynf:needs-human" || a.For("quarantined").Add[0] != "ynf:needs-human" {
		t.Fatal("a inherits on_escalate")
	}
	if b.For("ready").Remove[0] != "ynf:go" || b.For("proposed") != nil || b.For("running") != nil {
		t.Fatal("b inherits the defaults only")
	}
	var none *policy.Labels
	if none.For("ready") != nil {
		t.Fatal("no labels")
	}
	for _, st := range []string{"in_review", "done", "closed"} {
		if b.For(st) != nil {
			t.Errorf("%s: nothing configured", st)
		}
	}
}

// TestMergeLanes: the repository wins key by key; maps merge, anything else is replaced; the
// merged document is what gets validated, so a repository may hold only overrides.
func TestMergeLanes(t *testing.T) {
	config := []byte(`version: 1
defaults: {attempts: 2}
lanes:
  lint:
    kind: originate
    intake: [{github.search: "label:a", every: 5m}]
    run: {runner: command, command: {argv: [a, b]}}
    when: {converged: open_pr, ci_failed: escalate}
  docs:
    kind: originate
    intake: [{github.search: "label:d", every: 5m}]
    run: {runner: command, command: {argv: ["true"]}}
    when: {converged: open_pr}
`)
	repo := []byte(`version: 1
lanes:
  lint:
    run: {command: {argv: [c]}}
    when: {ci_failed: {retry: 1, then: escalate}}
  docs: {enabled: false}
  own:
    kind: originate
    intake: [{github.search: "label:o", every: 5m}]
    run: {runner: command, command: {argv: ["true"]}}
    when: {converged: open_pr}
`)
	if _, err := policy.Load(repo); err == nil {
		t.Fatal("the overrides alone should not be a valid lanes file")
	}
	doc, err := policy.MergeLanes(config, repo)
	if err != nil {
		t.Fatal(err)
	}
	f, err := policy.Load(doc)
	if err != nil {
		t.Fatalf("%v\n%s", err, doc)
	}
	lint := f.Lanes["lint"]
	if strings.Join(lint.Run.Command.Argv, " ") != "c" || lint.Run.Runner != "command" || lint.Kind != "originate" {
		t.Fatalf("lint: %+v", lint.Run)
	}
	if lint.When["converged"].Then == "" && lint.When["converged"].Action == "" || lint.When["ci_failed"].Retry != 1 {
		t.Fatalf("when merges key by key: %+v", lint.When)
	}
	if f.Lanes["docs"].On() || f.Lanes["own"].Kind != "originate" || lint.Attempts != 2 {
		t.Fatalf("docs off, own added, defaults kept: %v %+v %d", f.Lanes["docs"].On(), f.Lanes["own"], lint.Attempts)
	}
	if only, err := policy.MergeLanes(config, nil); err != nil || !strings.Contains(string(only), "label:a") {
		t.Fatalf("no repository lanes: %v", err)
	}
	if _, err := policy.MergeLanes([]byte("lanes: ["), repo); err == nil {
		t.Fatal("bad config yaml")
	}
	if _, err := policy.MergeLanes(config, []byte("lanes: [")); err == nil {
		t.Fatal("bad repo yaml")
	}
	src, err := policy.LaneSources(config, repo, "lint")
	if err != nil {
		t.Fatal(err)
	}
	if src["run.command.argv"] != "repo" || src["run.runner"] != "config" || src["when.ci_failed.retry"] != "repo" || src["kind"] != "config" {
		t.Fatalf("sources: %v", src)
	}
	if _, err := policy.LaneSources([]byte("x: ["), repo, "lint"); err == nil {
		t.Fatal("bad config yaml in sources")
	}
	if _, err := policy.LaneSources(config, []byte("x: ["), "lint"); err == nil {
		t.Fatal("bad repo yaml in sources")
	}
}

func TestLoadFactory(t *testing.T) {
	f, err := policy.LoadFactory([]byte("version: 1\nrepos: [o/r, github.acme.internal/acme/x]\n" +
		"forges:\n  ghe: {provider: github, url: https://github.acme.internal, token_env: GHE_TOKEN}\n" +
		"trackers:\n  jira:\n    provider: mcp\n    site: https://acme.atlassian.net\n    server: {command: [jira-mcp], env: [JIRA_TOKEN]}\n" +
		"    get: {tool: get, args: {issueKey: \"{key}\"}}\n    comment: {tool: comment}\n    label: {tool: label}\n" +
		"    fields: {title: result.t, labels: result.l, status: result.s}\n"))
	if err != nil || len(f.Repos) != 2 || f.Trackers["jira"]["provider"] != "mcp" {
		t.Fatalf("%+v %v", f, err)
	}
	for _, bad := range []string{"version: 1\n", "version: 2\nrepos: [o/r]\n", "version: 1\nrepos: [not a repo]\n", "version: 1\nrepos: [o/r]\nextra: 1\n", "version: 1\nrepos: [",
		"version: 1\nrepos: [o/r]\ntrackers:\n  jira: {provider: mcp}\n",
		"version: 1\nrepos: [o/r]\nforges:\n  ghe: {provider: gitlab, url: https://g.example, token_env: T}\n"} {
		if _, err := policy.LoadFactory([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// TestCheckLabels: a lane's command arguments and sensor scopes must all expand from the labels.
func TestCheckLabels(t *testing.T) {
	cmd := policy.Lane{Run: policy.Run{Command: &policy.Command{Argv: []string{"gofmt", "-w", "./{label.pkg}"}}}}
	if err := cmd.CheckLabels([]string{"pkg:internal/x"}); err != nil {
		t.Fatal(err)
	}
	if err := cmd.CheckLabels(nil); err == nil || !strings.Contains(err.Error(), "no pkg: label") {
		t.Fatalf("missing: %v", err)
	}
	if err := cmd.CheckLabels([]string{"pkg:a b"}); err == nil {
		t.Fatal("an unsafe value")
	}
	ynh := policy.Lane{Run: policy.Run{Ynh: &policy.Ynh{SensorScope: map[string]string{"test": "go test ./{label.area}/..."}}}}
	if err := ynh.CheckLabels([]string{"pkg:x"}); err == nil || !strings.Contains(err.Error(), "no area: label") {
		t.Fatalf("a sensor scope: %v", err)
	}
	if err := (policy.Lane{}).CheckLabels(nil); err != nil {
		t.Fatal("a lane with no templates needs no labels")
	}
}

// TestYnhModelIsASafeName: a lane's model reaches ynh's argv, so only a plain model name loads.
func TestYnhModelIsASafeName(t *testing.T) {
	lane := func(model string) []byte {
		return []byte("version: 1\nlanes:\n  x:\n    kind: originate\n    intake: [{github.search: q, every: 5m}]\n" +
			"    run: {runner: ynh, ynh: {harness: ., model: " + model + "}}\n    when: {converged: open_pr}\n")
	}
	for _, ok := range []string{"sonnet", "claude-sonnet-5-5", "anthropic/claude-3.5-sonnet:beta", "gpt_4o"} {
		f, err := policy.Load(lane(ok))
		if err != nil || f.Lanes["x"].Run.Ynh.Model != ok {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{`"a b"`, `"$(id)"`, `"x;y"`, `"-rf"`, `""`} {
		if _, err := policy.Load(lane(bad)); err == nil {
			t.Errorf("%s: loaded, want a schema error", bad)
		}
	}
}

// TestYnhEffortLevels: a lane's effort is one of the levels ynh accepts.
func TestYnhEffortLevels(t *testing.T) {
	lane := func(effort string) []byte {
		return []byte("version: 1\nlanes:\n  x:\n    kind: originate\n    intake: [{github.search: q, every: 5m}]\n" +
			"    run: {runner: ynh, ynh: {harness: ., effort: " + effort + "}}\n    when: {converged: open_pr}\n")
	}
	for _, ok := range []string{"low", "medium", "high"} {
		if f, err := policy.Load(lane(ok)); err != nil || f.Lanes["x"].Run.Ynh.Effort != ok {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"max", `""`, "High"} {
		if _, err := policy.Load(lane(bad)); err == nil {
			t.Errorf("%s: loaded, want a schema error", bad)
		}
	}
}

func TestACommandsImageWinsOnlyForTheCommandRunner(t *testing.T) {
	r := policy.Run{Image: "agent:1", Command: &policy.Command{Argv: []string{"x"}, Image: "golang:1"}}
	if r.ImageFor("command") != "golang:1" || r.ImageFor("ynh") != "agent:1" {
		t.Fatalf("%q %q", r.ImageFor("command"), r.ImageFor("ynh"))
	}
	r.Command.Image = ""
	if r.ImageFor("command") != "agent:1" {
		t.Fatal("run.image is the default")
	}
	if (policy.Run{Image: "a"}).ImageFor("command") != "a" {
		t.Fatal("no command block")
	}
}
