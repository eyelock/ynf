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

func TestPauseResumeStats(t *testing.T) {
	e := setup(t)
	if code, _, _ := e.run("sweep"); code != 0 {
		t.Fatal("sweep")
	}
	for _, args := range [][]string{{"pause"}, {"pause", "fmt"}, {"pause", "--reason", "x"}} {
		if code, _, _ := e.run(args...); code != cli.ExitUsage {
			t.Errorf("%v: %d", args, code)
		}
	}
	if code, _, _ := e.run("pause", "nope", "--reason", "x"); code != cli.ExitPolicy {
		t.Fatalf("unknown lane: %d", code)
	}
	if code, out, stderr := e.run("pause", "fmt", "--reason", "release freeze"); code != 0 || !strings.Contains(out, "o/r/fmt: paused (release freeze)") {
		t.Fatalf("pause: %d %s %s", code, out, stderr)
	}
	code, out, _ := e.run("stats")
	if code != 0 || !strings.Contains(out, "paused: release freeze") || !strings.Contains(out, "off") {
		t.Fatalf("stats: %d\n%s", code, out)
	}
	if code, out, _ := e.run("--format", "json", "resume", "fmt", "--reason", "freeze over"); code != 0 || !strings.Contains(out, `"paused": false`) {
		t.Fatalf("resume: %d %s", code, out)
	}
	if code, out, _ := e.run("--format", "json", "stats", "--lane", "off"); code != 0 || !strings.Contains(out, `"lane": "off"`) || strings.Contains(out, `"lane": "fmt"`) {
		t.Fatalf("stats --lane: %d %s", code, out)
	}
}

func TestStepFromAGitHubEvent(t *testing.T) {
	e := setup(t)
	ev := filepath.Join(e.dir, "event.json")
	_ = os.WriteFile(ev, []byte(`{"repository":{"full_name":"o/r"},"issue":{"number":5}}`), 0o644)
	if code, out, stderr := e.run("step", "--github-event", ev, "--github-event-name", "issues"); code != 0 || !strings.Contains(out, "issues on o/r: issues [5]") {
		t.Fatalf("%d %s %s", code, out, stderr)
	}
	if code, out, _ := e.run("items", "ls"); code != 0 || !strings.Contains(out, "ignored") {
		t.Fatalf("the issue should be tracked by the step: %s", out)
	}
	t.Setenv("GITHUB_EVENT_PATH", ev)
	t.Setenv("GITHUB_EVENT_NAME", "schedule")
	if code, out, _ := e.run("step"); code != 0 || !strings.Contains(out, "o/r#5") {
		t.Fatalf("a scheduled step sweeps: %d %s", code, out)
	}
	t.Setenv("GITHUB_EVENT_NAME", "")
	if code, _, _ := e.run("step"); code != cli.ExitUsage {
		t.Fatalf("no event name: %d", code)
	}
	if code, _, _ := e.run("step", "--github-event", "/no/such", "--github-event-name", "issues"); code != cli.ExitUsage {
		t.Fatalf("missing file: %d", code)
	}
	_ = os.WriteFile(ev, []byte(`{"repository":{"full_name":"x/y"},"issue":{"number":1}}`), 0o644)
	if code, _, _ := e.run("step", "--github-event", ev, "--github-event-name", "issues"); code == 0 {
		t.Fatal("an unenrolled repository's event was handled")
	}
}

func TestServeListenNeedsASecret(t *testing.T) {
	e := setup(t)
	t.Setenv("YNF_WEBHOOK_SECRET", "")
	if code, _, stderr := e.run("serve", "--listen", "127.0.0.1:0"); code != cli.ExitPolicy || !strings.Contains(stderr, "needs a webhook secret") {
		t.Fatalf("%d %s", code, stderr)
	}
	if code, _, _ := e.run("sweep", "--listen", "127.0.0.1:0"); code != cli.ExitUsage {
		t.Fatalf("--listen on sweep: %d", code)
	}
}

func TestStoreSchemes(t *testing.T) {
	e := setup(t)
	_ = os.WriteFile(e.cfg, []byte("version: 1\nrepos: [o/r]\nstore: dynamodb://table\n"), 0o644)
	if code, _, stderr := e.run("items", "ls"); code != cli.ExitPolicy || !strings.Contains(stderr, "dynamodb:// is not built yet") {
		t.Fatalf("%d %s", code, stderr)
	}
	_ = os.WriteFile(e.cfg, []byte("version: 1\nrepos: [o/r]\nstore: s3://bucket/ynf?region=us-east-1\n"), 0o644)
	t.Setenv("AWS_ACCESS_KEY_ID", "x")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "y")
	if code, _, _ := e.run("version"); code != 0 {
		t.Fatal("version needs no store")
	}
}

func TestLogFileAndFormat(t *testing.T) {
	e := setup(t)
	logPath := filepath.Join(e.dir, "ynf.log")
	if code, _, stderr := e.run("--log-file", logPath, "--log-format", "json", "sweep"); code != 0 || !strings.Contains(stderr, `"msg":"decided"`) {
		t.Fatalf("json to stderr: %d %s", code, stderr)
	}
	b, err := os.ReadFile(logPath)
	if err != nil || !strings.Contains(string(b), `"msg":"decided"`) || !strings.Contains(string(b), `"item":"item/github/o/r/issues/5"`) {
		t.Fatalf("the log file should hold the same structured lines: %s %v", b, err)
	}
	if code, _, _ := e.run("--log-format", "yaml", "items", "ls"); code != cli.ExitUsage {
		t.Fatalf("bad format: %d", code)
	}
	if code, _, _ := e.run("--log-file", "/no/such/dir/ynf.log", "items", "ls"); code != cli.ExitUsage {
		t.Fatalf("unwritable log file: %d", code)
	}
}
