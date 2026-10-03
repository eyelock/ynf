package cli_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eyelock/ynf/internal/cli"
	"github.com/eyelock/ynf/internal/item"
	"github.com/eyelock/ynf/internal/lease"
	"github.com/eyelock/ynf/internal/store"
	"github.com/eyelock/ynf/internal/store/sqlite"
)

const lanes = `version: 1
lanes:
  fmt:
    kind: originate
    intake: [{github.search: "label:ynf:fmt", every: 5m}]
    run: {runner: command, image: golang:1.26-alpine, command: {argv: [gofmt, -w, "./{label.pkg}"]}}
    when: {converged: open_pr}
  off:
    kind: originate
    enabled: false
    intake: [{github.search: "label:ynf:off", every: 5m}]
    run: {runner: command, command: {argv: ["true"]}}
    when: {converged: open_pr}
`

// fakeAPI serves enough of GitHub for the commands that do not run anything: one issue in a
// disabled lane, so a sweep tracks it and ignores it without touching git or docker.
func fakeAPI(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		base := "http://" + r.Host + "/"
		switch r.URL.Path {
		case "/repos/o/r":
			reply(map[string]any{"default_branch": "main"})
		case "/repos/o/r/contents/.agents/factory/lanes.yaml":
			reply(map[string]any{"type": "file", "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(lanes))})
		case "/search/issues":
			if strings.Contains(r.URL.Query().Get("q"), "ynf:off") {
				reply(map[string]any{"items": []any{map[string]any{"number": 5, "repository_url": base + "repos/o/r"}}})
				return
			}
			reply(map[string]any{"items": []any{}})
		case "/repos/o/r/issues/5":
			reply(map[string]any{"number": 5, "state": "open", "title": "t", "labels": []any{map[string]any{"name": "ynf:off"}}})
		default:
			w.WriteHeader(http.StatusNotFound)
			reply(map[string]any{"message": "Not Found"})
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

type env struct {
	t   *testing.T
	cfg string
	dir string
}

func setup(t *testing.T) env {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("version: 1\nrepos: [o/r]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YNF_GITHUB_API", fakeAPI(t))
	t.Setenv("GITHUB_TOKEN", "token")
	t.Setenv("YNF_CONFIG", "")
	t.Setenv("YNF_FORMAT", "")
	return env{t: t, cfg: cfg, dir: dir}
}

func (e env) run(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := cli.Run(context.Background(), append([]string{"--config", e.cfg}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

func TestUsageAndVersion(t *testing.T) {
	e := setup(t)
	if code, _, stderr := e.run(); code != cli.ExitUsage || !strings.Contains(stderr, "Usage:") {
		t.Fatalf("no command: %d %s", code, stderr)
	}
	if code, _, _ := e.run("nope"); code != cli.ExitUsage {
		t.Fatalf("unknown command: %d", code)
	}
	if code, out, _ := e.run("help"); code != 0 || !strings.Contains(out, "ynf sweep") {
		t.Fatalf("help: %d", code)
	}
	if code, out, _ := e.run("--format", "json", "version"); code != 0 || !strings.Contains(out, `"version"`) {
		t.Fatalf("version: %d %s", code, out)
	}
	if code, _, _ := e.run("--bogus"); code != cli.ExitUsage {
		t.Fatalf("bad flag: %d", code)
	}
}

func TestLanesValidate(t *testing.T) {
	e := setup(t)
	good := filepath.Join(e.dir, "lanes.yaml")
	_ = os.WriteFile(good, []byte(lanes), 0o644)
	if code, out, _ := e.run("lanes", "validate", "--file", good); code != 0 || !strings.Contains(out, "valid, 2 lanes") {
		t.Fatalf("%d %s", code, out)
	}
	bad := filepath.Join(e.dir, "bad.yaml")
	_ = os.WriteFile(bad, []byte("version: 1\nlanes:\n  x: {kind: originate}\n"), 0o644)
	if code, out, _ := e.run("--format", "json", "lanes", "validate", "--file", bad); code != cli.ExitPolicy || !strings.Contains(out, `"code":30`) {
		t.Fatalf("%d %s", code, out)
	}
	if code, _, _ := e.run("lanes"); code != cli.ExitUsage {
		t.Fatalf("lanes alone: %d", code)
	}
	if code, _, _ := e.run("lanes", "frobnicate"); code != cli.ExitUsage {
		t.Fatalf("lanes frobnicate: %d", code)
	}
}

func TestLanesShowSweepItemsReplay(t *testing.T) {
	e := setup(t)
	if code, out, stderr := e.run("lanes", "show"); code != 0 || !strings.Contains(out, `"fmt"`) {
		t.Fatalf("show: %d %s %s", code, out, stderr)
	}
	if code, _, _ := e.run("lanes", "show", "--repo", "o/r", "nope"); code != cli.ExitPolicy {
		t.Fatalf("show missing lane: %d", code)
	}

	// One sweep: the disabled lane's issue is tracked and ignored; nothing runs.
	code, out, stderr := e.run("sweep")
	if code != 0 || !strings.Contains(out, "o/r#5") || !strings.Contains(out, "ignored") {
		t.Fatalf("sweep: %d\n%s\n%s", code, out, stderr)
	}
	if code, out, _ := e.run("--format", "json", "items", "ls"); code != 0 || !strings.Contains(out, `"state": "ignored"`) {
		t.Fatalf("ls: %d %s", code, out)
	}
	if code, out, _ := e.run("items", "show", "o/r#5"); code != 0 || !strings.Contains(out, `"lane": "off"`) {
		t.Fatalf("show: %d %s", code, out)
	}
	if code, out, _ := e.run("items", "log", "o/r#5"); code != 0 || !strings.Contains(out, "ynf.ticket.matched -> ignored") {
		t.Fatalf("log: %d %s", code, out)
	}
	if code, out, _ := e.run("replay", "o/r#5"); code != 0 || !strings.Contains(out, "1 decisions, 0 differ") {
		t.Fatalf("replay: %d %s", code, out)
	}
	pol := filepath.Join(e.dir, "on.yaml")
	_ = os.WriteFile(pol, []byte(strings.Replace(lanes, "    enabled: false\n", "", 1)), 0o644)
	if code, out, _ := e.run("replay", "item/github/o/r/issues/5", "--policy", pol); code != cli.ExitDifferences || !strings.Contains(out, "DIFF") {
		t.Fatalf("replay under another policy: %d %s", code, out)
	}
	if code, out, _ := e.run("items", "retry", "o/r#5"); code != 0 || !strings.Contains(out, "back to ready") {
		t.Fatalf("retry: %d %s", code, out)
	}
	if code, out, _ := e.run("items", "release", "o/r#5"); code != 0 || !strings.Contains(out, "lease cleared") {
		t.Fatalf("release: %d %s", code, out)
	}
	if code, _, _ := e.run("items", "show", "not-a-key"); code != cli.ExitUsage {
		t.Fatalf("bad key: %d", code)
	}
	if code, _, _ := e.run("items", "show", "o/r#99"); code != cli.ExitAdapter {
		t.Fatalf("missing item: %d", code)
	}
	for _, args := range [][]string{{"items"}, {"items", "show"}, {"items", "frob", "o/r#5"}, {"replay"}} {
		if code, _, _ := e.run(args...); code != cli.ExitUsage {
			t.Errorf("%v: %d", args, code)
		}
	}
}

func TestSweepUntilSettledAndDoctor(t *testing.T) {
	e := setup(t)
	if code, out, stderr := e.run("sweep", "--until-settled", "--timeout", "1m", "--interval", "10ms"); code != 0 || !strings.Contains(out, "ignored") {
		t.Fatalf("until settled: %d %s %s", code, out, stderr)
	}
	code, out, _ := e.run("--format", "json", "doctor")
	if !strings.Contains(out, `"lanes o/r"`) || !strings.Contains(out, ".agents/factory on main: fmt, off") {
		t.Fatalf("doctor: %d %s", code, out)
	}
}

func TestConfigErrors(t *testing.T) {
	e := setup(t)
	_ = os.WriteFile(e.cfg, []byte("version: 1\nrepos: []\n"), 0o644)
	if code, _, _ := e.run("items", "ls"); code != cli.ExitPolicy {
		t.Fatalf("invalid config: %d", code)
	}
	t.Setenv("HOME", t.TempDir())
	var out, errb bytes.Buffer
	if code := cli.Run(context.Background(), []string{"items", "ls"}, &out, &errb); code != cli.ExitPolicy || !strings.Contains(errb.String(), "no config.yaml") {
		t.Fatalf("no config found: %d %s", code, errb.String())
	}
}

func TestItemsLogSummarisesRunsAndActions(t *testing.T) {
	e := setup(t)
	if code, _, _ := e.run("sweep"); code != 0 {
		t.Fatal("sweep")
	}
	st, err := sqlite.Open(filepath.Join(e.dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	k := item.IssueKey("o/r", 5)
	for i, body := range []string{
		`{"run_id":"R1","runner":"command","executor":"docker","outcome":"converged","changed":["a.go"],"duration":"2s"}`,
		`{"action":"open_pr","ok":false,"detail":"diff gate refused"}`,
		`{"by":"human","action":"retry"}`,
	} {
		kind := []string{"run", "action", "note"}[i]
		if err := st.Append(context.Background(), k, store.LogEntry{ID: fmt.Sprintf("Z%d", i), Time: time.Now(), Kind: kind, Body: json.RawMessage(body)}); err != nil {
			t.Fatal(err)
		}
	}
	_ = st.Close()
	code, out, _ := e.run("items", "log", "o/r#5")
	for _, want := range []string{"R1 command via docker: converged", "1 changed (2s)", "open_pr ok=false diff gate refused", `"by":"human"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q (%d):\n%s", want, code, out)
		}
	}
}

func TestRetryRefusesALiveLease(t *testing.T) {
	e := setup(t)
	if code, _, _ := e.run("sweep"); code != 0 {
		t.Fatal("sweep")
	}
	st, _ := sqlite.Open(filepath.Join(e.dir, "state.db"))
	if _, err := lease.Claim(context.Background(), st, item.IssueKey("o/r", 5), "other", "s", time.Hour, time.Now); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	if code, _, stderr := e.run("items", "retry", "o/r#5"); code != cli.ExitAdapter || !strings.Contains(stderr, "being worked on by other") {
		t.Fatalf("%d %s", code, stderr)
	}
}
