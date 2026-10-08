package forge_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/facts"
	"github.com/eyelock/ynf/internal/forge"
)

// checksServer is a GitHub with one pull request, o/r#7 on main, whose protection, rulesets, check
// runs and commit statuses a test sets. A nil protection or rules answers 403, as a token that
// cannot read them would be answered.
type checksServer struct {
	protection map[string]any // the required_status_checks body; nil: 403
	unprotect  bool           // 404 "Branch not protected" instead
	rules      []any          // the rules for the branch; nil: 403
	runs       []any
	statuses   []any
	hits       map[string]int
}

func (c *checksServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	deny := func(code int, msg string) {
		w.WriteHeader(code)
		reply(map[string]any{"message": msg})
	}
	if c.hits == nil {
		c.hits = map[string]int{}
	}
	c.hits[r.URL.Path]++
	// page serves one page of items, per_page at a time, with a Link to the next.
	page := func(items []any, key string, extra map[string]any) {
		per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
		pg, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if per == 0 {
			per = len(items)
		}
		if pg == 0 {
			pg = 1
		}
		lo, hi := min((pg-1)*per, len(items)), min(pg*per, len(items))
		if hi < len(items) {
			q := r.URL.Query()
			q.Set("page", strconv.Itoa(pg+1))
			w.Header().Set("Link", fmt.Sprintf(`<http://%s%s?%s>; rel="next"`, r.Host, r.URL.Path, q.Encode()))
		}
		body := map[string]any{key: items[lo:hi], "total_count": len(items)}
		for k, v := range extra {
			body[k] = v
		}
		reply(body)
	}
	switch p := r.URL.Path; p {
	case "/repos/o/r/pulls/7":
		reply(map[string]any{"number": 7, "state": "open",
			"head": map[string]any{"sha": "abc", "repo": map[string]any{"full_name": "o/r"}},
			"base": map[string]any{"ref": "main", "repo": map[string]any{"full_name": "o/r"}}})
	case "/repos/o/r/pulls/7/reviews":
		reply([]any{})
	case "/repos/o/r/branches/main/protection/required_status_checks":
		switch {
		case c.unprotect:
			deny(http.StatusNotFound, "Branch not protected")
		case c.protection == nil:
			deny(http.StatusForbidden, "Resource not accessible")
		default:
			reply(c.protection)
		}
	case "/repos/o/r/rules/branches/main":
		if c.rules == nil {
			deny(http.StatusForbidden, "Resource not accessible")
			return
		}
		reply(c.rules)
	case "/repos/o/r/commits/abc/check-runs":
		page(c.runs, "check_runs", nil)
	case "/repos/o/r/commits/abc/status":
		page(c.statuses, "statuses", map[string]any{"state": "success"})
	default:
		deny(http.StatusNotFound, "Not Found")
	}
}

func rule(checks ...any) any {
	return map[string]any{"type": "required_status_checks", "parameters": map[string]any{"required_status_checks": checks}}
}

func run(name, conclusion, app string, appID int) any {
	m := map[string]any{"name": name, "status": "completed", "conclusion": conclusion}
	if app != "" {
		m["app"] = map[string]any{"id": appID, "slug": app}
	}
	return m
}

func pullRequest(t *testing.T, cs *checksServer) (*facts.PR, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(cs)
	t.Cleanup(srv.Close)
	g, err := forge.NewGitHub("token", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	logged := &bytes.Buffer{}
	g.Log = slog.New(slog.NewTextHandler(logged, nil))
	p, err := g.PullRequest(context.Background(), "o/r", 7)
	if err != nil {
		t.Fatal(err)
	}
	return p, logged
}

func byName(p *facts.PR) map[string]facts.Check {
	out := map[string]facts.Check{}
	for _, c := range p.Checks {
		out[c.Name] = c
	}
	return out
}

func TestRequiredCheckThatHasNotStartedIsListed(t *testing.T) {
	p, _ := pullRequest(t, &checksServer{
		protection: map[string]any{"contexts": []string{"lint", "build"}},
		rules:      []any{},
		runs:       []any{run("lint", "success", "github-actions", 15368)},
	})
	got := byName(p)
	want := facts.Check{Name: "build", Status: facts.StatusExpected, Required: true}
	if got["build"] != want {
		t.Fatalf("build: %+v, want %+v", got["build"], want)
	}
	if p.CIState() != "pending" {
		t.Fatalf("every reported check passed but a required one has not started: %s", p.CIState())
	}
	if p.RequiredUnknown {
		t.Fatal("protection was read")
	}
}

func TestRulesetRequiredCheckIsRead(t *testing.T) {
	p, _ := pullRequest(t, &checksServer{
		unprotect: true,
		rules: []any{
			map[string]any{"type": "deletion"},
			rule(map[string]any{"context": "gate"}, map[string]any{"context": "lint"}),
		},
		runs: []any{run("lint", "success", "github-actions", 15368), run("docs", "failure", "github-actions", 15368)},
	})
	got := byName(p)
	if !got["lint"].Required || got["docs"].Required {
		t.Fatalf("lint is ruleset-required, docs is not: %+v", p.Checks)
	}
	if g := got["gate"]; !g.Required || g.Status != facts.StatusExpected {
		t.Fatalf("gate is ruleset-required and has not started: %+v", g)
	}
	if p.CIState() != "pending" || p.RequiredUnknown {
		t.Fatalf("state %s, unknown %v", p.CIState(), p.RequiredUnknown)
	}
}

func TestRequiredChecksAreTheUnionOfProtectionAndRulesets(t *testing.T) {
	p, _ := pullRequest(t, &checksServer{
		protection: map[string]any{"contexts": []string{"lint"}, "checks": []any{map[string]any{"context": "lint"}}},
		rules:      []any{rule(map[string]any{"context": "lint"}, map[string]any{"context": "test"})},
		runs:       []any{run("lint", "success", "", 0), run("test", "success", "", 0)},
	})
	n := 0
	for _, c := range p.Checks {
		if c.Required {
			n++
		}
	}
	if len(p.Checks) != 2 || n != 2 {
		t.Fatalf("lint once, from both sources, and test: %+v", p.Checks)
	}
}

func TestAppBoundRequirementIsMetOnlyByThatApp(t *testing.T) {
	cs := &checksServer{
		protection: map[string]any{
			"contexts": []string{"scan"},
			"checks":   []any{map[string]any{"context": "scan", "app_id": 42}},
		},
		rules: []any{},
		runs: []any{
			run("scan", "success", "impostor", 7),
		},
	}
	p, _ := pullRequest(t, cs)
	var other, wanted *facts.Check
	for i := range p.Checks {
		if p.Checks[i].App == "impostor" {
			other = &p.Checks[i]
		} else {
			wanted = &p.Checks[i]
		}
	}
	if other == nil || other.Required || other.AppID != 7 {
		t.Fatalf("the same name from another App is not the required check: %+v", p.Checks)
	}
	if wanted == nil || !wanted.Required || wanted.Status != facts.StatusExpected || wanted.AppID != 42 {
		t.Fatalf("the App's own run has not reported: %+v", p.Checks)
	}
	if p.CIState() != "pending" {
		t.Fatalf("got %s", p.CIState())
	}

	// With the bound App's run present, that run is the required check and the other is not.
	cs.runs = append(cs.runs, run("scan", "success", "scanner", 42))
	p, _ = pullRequest(t, cs)
	for _, c := range p.Checks {
		if c.Required != (c.App == "scanner") {
			t.Fatalf("only scanner's run is required: %+v", p.Checks)
		}
		if c.Status == facts.StatusExpected {
			t.Fatalf("nothing is expected now: %+v", p.Checks)
		}
	}
	if p.CIState() != "success" {
		t.Fatalf("got %s", p.CIState())
	}
}

func TestRulesetAppBindingAndStatusesHaveNoApp(t *testing.T) {
	p, _ := pullRequest(t, &checksServer{
		unprotect: true,
		rules:     []any{rule(map[string]any{"context": "ext", "integration_id": 9})},
		runs:      []any{},
		statuses:  []any{map[string]any{"context": "ext", "state": "success"}},
	})
	for _, c := range p.Checks {
		if c.Name == "ext" && c.Status == "completed" && c.Required {
			t.Fatalf("a commit status has no App, so it cannot meet an App-bound requirement: %+v", p.Checks)
		}
	}
	if p.CIState() != "pending" {
		t.Fatalf("got %s", p.CIState())
	}
}

func TestRequiredUnknownWhenNeitherSourceCanBeRead(t *testing.T) {
	cs := &checksServer{
		runs: []any{run("lint", "success", "github-actions", 15368), run("test", "failure", "github-actions", 15368)},
	}
	srv := httptest.NewServer(cs)
	t.Cleanup(srv.Close)
	g, err := forge.NewGitHub("token", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	logged := &bytes.Buffer{}
	g.Log = slog.New(slog.NewTextHandler(logged, nil))
	for range 3 {
		p, err := g.PullRequest(context.Background(), "o/r", 7)
		if err != nil {
			t.Fatal(err)
		}
		if !p.RequiredUnknown {
			t.Fatal("the facts should say the required checks are unknown")
		}
		for _, c := range p.Checks {
			if c.Required {
				t.Fatalf("nothing can be called required: %+v", c)
			}
		}
		if p.CIState() != "failure" {
			t.Fatalf("every check gates, and test failed: %s", p.CIState())
		}
	}
	if n := strings.Count(logged.String(), "required checks unreadable"); n != 1 {
		t.Fatalf("logged %d times, want once per repository:\n%s", n, logged)
	}
	if !strings.Contains(logged.String(), "repo=o/r") {
		t.Fatalf("the log should name the repository:\n%s", logged)
	}
	req := g.RequiredChecks(context.Background(), "o/r", "main")
	if req.Known || req.Detail == "" {
		t.Fatalf("%+v", req)
	}
}

func TestOneReadableSourceIsEnough(t *testing.T) {
	// Protection is unreadable, the rulesets are readable and name one check: known.
	p, logged := pullRequest(t, &checksServer{rules: []any{rule(map[string]any{"context": "lint"})}, runs: []any{run("lint", "success", "", 0)}})
	if p.RequiredUnknown || logged.Len() != 0 {
		t.Fatalf("unknown %v, log %q", p.RequiredUnknown, logged)
	}
	// Rulesets are unreadable, protection is readable.
	p, logged = pullRequest(t, &checksServer{protection: map[string]any{"contexts": []string{"lint"}}, runs: []any{run("lint", "success", "", 0)}})
	if p.RequiredUnknown || logged.Len() != 0 || !p.Checks[0].Required {
		t.Fatalf("unknown %v, log %q, %+v", p.RequiredUnknown, logged, p.Checks)
	}
}

func TestChecksAndStatusesPaginateToTheEnd(t *testing.T) {
	var runs, statuses []any
	for i := range 230 {
		runs = append(runs, run(fmt.Sprintf("run-%03d", i), "success", "github-actions", 15368))
	}
	for i := range 130 {
		statuses = append(statuses, map[string]any{"context": fmt.Sprintf("status-%03d", i), "state": "success"})
	}
	// The required check is the last run, on the third page, and one status, on the second.
	cs := &checksServer{
		protection: map[string]any{"contexts": []string{"run-229", "status-129", "missing"}},
		rules:      []any{},
		runs:       runs,
		statuses:   statuses,
	}
	p, _ := pullRequest(t, cs)
	if len(p.Checks) != 230+130+1 {
		t.Fatalf("%d checks", len(p.Checks))
	}
	got := byName(p)
	if !got["run-229"].Required || !got["status-129"].Required {
		t.Fatal("a required check on a later page was not marked")
	}
	if got["missing"].Status != facts.StatusExpected {
		t.Fatalf("%+v", got["missing"])
	}
	if cs.hits["/repos/o/r/commits/abc/check-runs"] != 3 || cs.hits["/repos/o/r/commits/abc/status"] != 2 {
		t.Fatalf("pages read: %v", cs.hits)
	}
}
