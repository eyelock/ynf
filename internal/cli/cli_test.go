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
	"os/exec"
	"path/filepath"
	"slices"
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

// apiHook lets a test answer paths fakeAPI does not; it reports whether it did.
var apiHook func(w http.ResponseWriter, path string) bool

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
		case "/repos/o/r/branches/main":
			reply(map[string]any{"name": "main", "commit": map[string]any{"sha": "c0ffee"}})
		case "/repos/o/r/contents/.agents/factory/factory.yaml":
			reply(map[string]any{"type": "file", "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte("version: 1\nrepos: [o/r]\n"))})
		case "/repos/o/r/contents/.agents/factory/lanes.yaml":
			reply(map[string]any{"type": "file", "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(lanes))})
		case "/search/issues":
			if strings.Contains(r.URL.Query().Get("q"), "ynf:off") {
				reply(map[string]any{"items": []any{map[string]any{"number": 5, "repository_url": base + "repos/o/r"}}})
				return
			}
			reply(map[string]any{"items": []any{}})
		case "/repos/o/r/issues/5":
			reply(map[string]any{"number": 5, "state": "open", "title": "t", "labels": []any{map[string]any{"name": "ynf:off"}, map[string]any{"name": "pkg:internal/format"}}})
		default:
			if apiHook != nil && apiHook(w, r.URL.Path) {
				return
			}
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

// setup runs ynf against a fake GitHub. It is a local server, so its host, 127.0.0.1, is the
// forge host in item keys.
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
	_, want, _ := e.run("version")
	if code, out, _ := e.run("--version"); code != 0 || out != want || !strings.HasPrefix(out, "ynf ") {
		t.Fatalf("--version %d %q should print what version prints, %q", code, out, want)
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
	if code, out, stderr := e.run("lanes", "show"); code != 0 || !strings.Contains(out, "\n  fmt:\n") || strings.Contains(out, `"fmt"`) {
		t.Fatalf("show is YAML as text: %d %s %s", code, out, stderr)
	}
	if code, out, stderr := e.run("--format", "json", "lanes", "show"); code != 0 || !strings.Contains(out, `"fmt"`) {
		t.Fatalf("show is JSON as json: %d %s %s", code, out, stderr)
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
	if code, out, _ := e.run("replay", "item/127.0.0.1/o/r/issues/5", "--policy", pol); code != cli.ExitDifferences || !strings.Contains(out, "DIFF") {
		t.Fatalf("replay under another policy: %d %s", code, out)
	}
	setState(t, e.dir, "item/127.0.0.1/o/r/issues/5", item.Escalated)
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
	if !strings.Contains(out, `"lanes o/r"`) || !strings.Contains(out, ".agents/factory on main at c0ffee: fmt, off") {
		t.Fatalf("doctor: %d %s", code, out)
	}
}

// TestDoctorSaysWhenRequiredChecksCannotBeRead: the fake GitHub serves neither branch protection nor
// rulesets, so doctor names the repository and says every check gates. It is a warning, as ynf works.
func TestDoctorSaysWhenRequiredChecksCannotBeRead(t *testing.T) {
	e := setup(t)
	code, out, _ := e.run("--format", "json", "doctor")
	var rep struct {
		Checks []struct {
			Name, Detail string
			OK           bool
		}
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err, out)
	}
	found := false
	for _, c := range rep.Checks {
		if c.Name == "required checks o/r" {
			found = true
			if c.OK || !strings.Contains(c.Detail, "every check gates") || !strings.Contains(c.Detail, "main") {
				t.Errorf("%+v", c)
			}
		}
	}
	if !found {
		t.Fatalf("doctor %d did not report o/r's required checks: %s", code, out)
	}
	if _, human, _ := e.run("doctor"); !strings.Contains(human, "--    required checks o/r") {
		t.Errorf("unreadable required checks are a warning, not a failure: %s", human)
	}
}

// TestDoctorWarnsOfARepositoryWithNoCI: the default branch has no required checks and, depending on
// the workflows, none or some workflow files. With none, doctor warns that no CI will report; it is
// a warning, not a failure, and it is not said when there are workflows.
func TestDoctorWarnsOfARepositoryWithNoCI(t *testing.T) {
	workflows := []any{}
	t.Cleanup(func() { apiHook = nil })
	apiHook = func(w http.ResponseWriter, path string) bool {
		switch path {
		case "/repos/o/r/branches/main/protection/required_status_checks":
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "Branch not protected"})
		case "/repos/o/r/rules/branches/main":
			_, _ = w.Write([]byte("[]"))
		case "/repos/o/r/contents/.github/workflows":
			if len(workflows) == 0 {
				return false
			}
			_ = json.NewEncoder(w).Encode(workflows)
		default:
			return false
		}
		return true
	}
	e := setup(t)
	code, human, _ := e.run("doctor")
	if code != 0 || !strings.Contains(human, "--    ci o/r") || !strings.Contains(human, "no workflow files and no required checks on main") {
		t.Fatalf("doctor %d should warn of no CI, and not fail: %s", code, human)
	}
	workflows = []any{map[string]any{"type": "file", "name": "ci.yml", "path": ".github/workflows/ci.yml"}}
	if _, human, _ := e.run("doctor"); strings.Contains(human, "ci o/r") {
		t.Fatalf("a repository with a workflow has CI: %s", human)
	}
}

// TestDoctorSaysWhyMemoryIsOff: ynm on PATH is not enough (ADR-012): with no store, memory stays off
// and doctor says so as a warning; with one, it is on.
func TestDoctorSaysWhyMemoryIsOff(t *testing.T) {
	e := setup(t)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ynm"), []byte("#!/bin/sh\necho 0.3.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("YNM_HOME", "")
	t.Chdir(t.TempDir())
	_, human, _ := e.run("doctor")
	if !strings.Contains(human, "--    memory") || !strings.Contains(human, "off: ynm is on PATH but has no store") {
		t.Fatalf("doctor should say why memory is off: %s", human)
	}
	ynmHome := t.TempDir()
	t.Setenv("YNM_HOME", ynmHome)
	if _, human, _ := e.run("doctor"); !strings.Contains(human, "ok    memory") || !strings.Contains(human, "on: ynm on PATH, the user store, from YNM_HOME at "+ynmHome) {
		t.Fatalf("a store turns it on, and doctor says which: %s", human)
	}
	// A repository under HOME, a ~/.ynm, and YNM_HOME naming a folder that is not there: ynm
	// writes to YNM_HOME, so ~/.ynm is not the store (#147).
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".ynm"), 0o755); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(home, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	t.Setenv("YNM_HOME", filepath.Join(repo, "no-such-home"))
	if _, human, _ := e.run("doctor"); !strings.Contains(human, "--    memory") || strings.Contains(human, "store at "+filepath.Join(home, ".ynm")) || !strings.Contains(human, "no-such-home") {
		t.Fatalf("a missing YNM_HOME does not fall back to ~/.ynm: %s", human)
	}
}

// TestDoctorReportsQueuedMemoryWrites: nothing is said when the queue is empty; when writes wait
// for ynm, doctor says how many and since when, as a warning, not a failure.
func TestDoctorReportsQueuedMemoryWrites(t *testing.T) {
	e := setup(t)
	if code, out, _ := e.run("doctor"); strings.Contains(out, "memory queue") {
		t.Fatalf("an empty queue is not reported: %d %s", code, out)
	}
	st, err := sqlite.Open(filepath.Join(e.dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	for i, at := range []string{"2026-10-05T09:00:00Z", "2026-10-05T10:00:00Z"} {
		doc, _ := json.Marshal(map[string]any{"record": map[string]any{"Subject": "sig/x"}, "queued": at})
		if _, err := st.Put(context.Background(), fmt.Sprintf("memory/queue/%026d", i), doc, ""); err != nil {
			t.Fatal(err)
		}
	}
	_ = st.Close()
	code, out, _ := e.run("--format", "json", "doctor")
	var rep struct {
		OK     bool
		Checks []struct {
			Name, Detail string
			OK           bool
		}
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err, out)
	}
	found := false
	for _, c := range rep.Checks {
		if c.Name == "memory queue" {
			found = true
			want := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC).Local().Format(time.RFC3339)
			if c.OK || c.Detail != "2 memory writes queued since "+want {
				t.Errorf("memory queue check: %+v", c)
			}
		}
	}
	if !found {
		t.Fatalf("doctor %d did not report the queue: %s", code, out)
	}
	if _, human, _ := e.run("doctor"); !strings.Contains(human, "--    memory queue") {
		t.Errorf("a queue is a warning, not a failure: %s", human)
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
	k := item.IssueKey("127.0.0.1", "o/r", 5)
	for i, body := range []string{
		`{"run_id":"R1","runner":"command","executor":"docker","outcome":"converged","changed":["a.go"],"duration":"2s"}`,
		`{"action":"open_pr","ok":false,"detail":"diff gate refused"}`,
		`{"by":"human","action":"retry"}`,
		`{"run_id":"R2","runner":"ynh","outcome":"operator_error","detail":"no such executor"}`,
	} {
		kind := []string{"run", "action", "note", "run"}[i]
		if err := st.Append(context.Background(), k, store.LogEntry{ID: fmt.Sprintf("Z%d", i), Time: time.Now(), Kind: kind, Body: json.RawMessage(body)}); err != nil {
			t.Fatal(err)
		}
	}
	_ = st.Close()
	code, out, _ := e.run("items", "log", "o/r#5")
	for _, want := range []string{"R1 command via docker: converged", "1 changed (2s)", "R2 ynh: operator_error no such executor", "open_pr ok=false diff gate refused", `"by":"human"`} {
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
	setState(t, e.dir, item.IssueKey("127.0.0.1", "o/r", 5), item.Escalated)
	st, _ := sqlite.Open(filepath.Join(e.dir, "state.db"))
	if _, err := lease.Claim(context.Background(), st, item.IssueKey("127.0.0.1", "o/r", 5), "other", "s", time.Hour, time.Now); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	if code, _, stderr := e.run("items", "retry", "o/r#5"); code != cli.ExitAdapter || !strings.Contains(stderr, "being worked on by other") {
		t.Fatalf("%d %s", code, stderr)
	}
}

// Releasing brings the restart forward for an unsettled item. A settled one has no work to
// restart, so its timer is left alone.
func TestReleaseMakesAnUnsettledItemDueNow(t *testing.T) {
	for state, wantDue := range map[item.State]bool{item.Running: true, item.Done: false} {
		t.Run(string(state), func(t *testing.T) {
			e := setup(t)
			if code, _, _ := e.run("sweep"); code != 0 {
				t.Fatal("sweep")
			}
			ctx := context.Background()
			k := item.IssueKey("127.0.0.1", "o/r", 5)
			setState(t, e.dir, k, state)
			st, _ := sqlite.Open(filepath.Join(e.dir, "state.db"))
			defer func() { _ = st.Close() }()
			if _, err := lease.Claim(ctx, st, k, "other", "s", time.Hour, time.Now); err != nil {
				t.Fatal(err)
			}
			// The dead holder's timer, an hour out.
			if err := st.Schedule(ctx, k, time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			if code, _, stderr := e.run("items", "release", "o/r#5"); code != 0 {
				t.Fatalf("%d %s", code, stderr)
			}
			doc, _, err := st.Get(ctx, k)
			if err != nil {
				t.Fatal(err)
			}
			var it item.Item
			if err := json.Unmarshal(doc, &it); err != nil {
				t.Fatal(err)
			}
			if it.Lease != nil {
				t.Errorf("lease still held: %+v", it.Lease)
			}
			due, err := st.Due(ctx, time.Now(), 10)
			if err != nil {
				t.Fatal(err)
			}
			if got := slices.Contains(due, k); got != wantDue {
				t.Errorf("due now = %v, want %v (%v)", got, wantDue, due)
			}
		})
	}
}

// setState rewrites an item's state and counters in the store, as a run would have left them.
func setState(t *testing.T, dir, k string, state item.State) {
	t.Helper()
	ctx := context.Background()
	st, err := sqlite.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	doc, v, err := st.Get(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	var it item.Item
	if err := json.Unmarshal(doc, &it); err != nil {
		t.Fatal(err)
	}
	it.State, it.Attempts, it.Counters = state, 3, map[string]int{"ci": 2}
	b, _ := json.Marshal(it)
	if _, err := st.Put(ctx, k, b, v); err != nil {
		t.Fatal(err)
	}
}

func TestRetryIsOnlyForEscalatedOrQuarantined(t *testing.T) {
	e := setup(t)
	if code, _, _ := e.run("sweep"); code != 0 {
		t.Fatal("sweep")
	}
	k := item.IssueKey("127.0.0.1", "o/r", 5)
	for _, state := range []item.State{item.Ignored, item.InReview, item.Done, item.Proposed} {
		setState(t, e.dir, k, state)
		code, _, stderr := e.run("items", "retry", "o/r#5")
		if code != cli.ExitUsage || !strings.Contains(stderr, "is "+string(state)+"; retry is only for escalated or quarantined items") {
			t.Fatalf("%s: %d %s", state, code, stderr)
		}
		_, out, _ := e.run("--format", "json", "items", "show", "o/r#5")
		if !strings.Contains(out, `"state": "`+string(state)+`"`) || !strings.Contains(out, `"attempts": 3`) || !strings.Contains(out, `"ci": 2`) {
			t.Fatalf("%s was touched:\n%s", state, out)
		}
	}
	for _, state := range []item.State{item.Escalated, item.Quarantined} {
		setState(t, e.dir, k, state)
		if code, out, stderr := e.run("items", "retry", "o/r#5"); code != 0 || !strings.Contains(out, "back to ready") {
			t.Fatalf("%s: %d %s %s", state, code, out, stderr)
		}
		_, out, _ := e.run("--format", "json", "items", "show", "o/r#5")
		if !strings.Contains(out, `"state": "ready"`) || strings.Contains(out, `"attempts": 3`) {
			t.Fatalf("%s not reset:\n%s", state, out)
		}
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

func TestHandleAGitHubEvent(t *testing.T) {
	e := setup(t)
	ev := filepath.Join(e.dir, "event.json")
	_ = os.WriteFile(ev, []byte(`{"repository":{"full_name":"o/r"},"issue":{"number":5}}`), 0o644)
	if code, out, stderr := e.run("handle", "--github-event", ev, "--github-event-name", "issues"); code != 0 || !strings.Contains(out, "issues on o/r: issues [5]") {
		t.Fatalf("%d %s %s", code, out, stderr)
	}
	if code, out, _ := e.run("items", "ls"); code != 0 || !strings.Contains(out, "ignored") {
		t.Fatalf("the issue should be tracked by the step: %s", out)
	}
	t.Setenv("GITHUB_EVENT_PATH", ev)
	t.Setenv("GITHUB_EVENT_NAME", "schedule")
	if code, out, _ := e.run("handle"); code != 0 || !strings.Contains(out, "o/r#5") {
		t.Fatalf("a scheduled step sweeps: %d %s", code, out)
	}
	t.Setenv("GITHUB_EVENT_NAME", "")
	if code, _, _ := e.run("handle"); code != cli.ExitUsage {
		t.Fatalf("no event name: %d", code)
	}
	if code, _, _ := e.run("handle", "--github-event", "/no/such", "--github-event-name", "issues"); code != cli.ExitUsage {
		t.Fatalf("missing file: %d", code)
	}
	_ = os.WriteFile(ev, []byte(`{"repository":{"full_name":"x/y"},"issue":{"number":1}}`), 0o644)
	if code, _, _ := e.run("handle", "--github-event", ev, "--github-event-name", "issues"); code == 0 {
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
	if err != nil || !strings.Contains(string(b), `"msg":"decided"`) || !strings.Contains(string(b), `"item":"item/127.0.0.1/o/r/issues/5"`) {
		t.Fatalf("the log file should hold the same structured lines: %s %v", b, err)
	}
	if code, _, _ := e.run("--log-format", "yaml", "items", "ls"); code != cli.ExitUsage {
		t.Fatalf("bad format: %d", code)
	}
	if code, _, _ := e.run("--log-file", "/no/such/dir/ynf.log", "items", "ls"); code != cli.ExitUsage {
		t.Fatalf("unwritable log file: %d", code)
	}
}

// TestStart: an instruction from the command line; refusals exit 33 before anything is created.
func TestStart(t *testing.T) {
	e := setup(t)
	code, out, stderr := e.run("--format", "json", "start", "o/r#5", "--detach")
	if code != 0 || !strings.Contains(out, `"key": "item/127.0.0.1/o/r/issues/5"`) || !strings.Contains(out, `"lane": "fmt"`) {
		t.Fatalf("%d %s %s", code, out, stderr)
	}
	if code, out, _ := e.run("start", "127.0.0.1/o/r#5", "--detach"); code != 0 || !strings.Contains(out, "a running ynf serve takes it on") {
		t.Fatalf("a host-qualified reference: %d %s", code, out)
	}
	if code, out, stderr := e.run("start", "--prompt", "tidy", "--label", "pkg:internal/format", "--repo", "o/r", "--lane", "fmt", "--detach"); code != 0 || !strings.Contains(out, "a running ynf serve takes it on") {
		t.Fatalf("a labelled prompt: %d %s %s", code, out, stderr)
	}
	for _, c := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"start", "o/r#5", "--lane", "off", "--detach"}, cli.ExitStartRefused, "switched off"},
		{[]string{"start", "--prompt", "tidy", "--detach"}, cli.ExitStartRefused, "say which repository"},
		{[]string{"start", "--prompt", "tidy", "--repo", "o/r", "--lane", "fmt", "--detach"}, cli.ExitStartRefused, "give it with --label"},
		{[]string{"start", "--prompt", "tidy", "--repo", "github.example/o/r", "--detach"}, cli.ExitStartRefused, "this ynf works with the forge at 127.0.0.1"},
		{[]string{"start", "--prompt", "tidy", "--repo", "a/b/c/d"}, cli.ExitStartRefused, "not owner/name"},
		{[]string{"start"}, cli.ExitUsage, "a reference or --prompt"},
		{[]string{"start", "o/r#5", "--prompt", "x"}, cli.ExitUsage, "not both"},
		{[]string{"start", "o/r#5", "extra"}, cli.ExitUsage, "one reference"},
		{[]string{"start", "o/r#5", "--auto-approve", "everything"}, cli.ExitUsage, "want edits or all"},
		{[]string{"start", "o/r#5", "--auto-approve", "edits", "--detach"}, cli.ExitUsage, "not --detach"},
		{[]string{"shadow", "run", "fmt", "--auto-approve", "everything"}, cli.ExitUsage, "want edits or all"},
		{[]string{"start", "not-a-ref"}, cli.ExitUsage, "is not a reference"},
		{[]string{"start", "o/r#5", "--bogus"}, cli.ExitUsage, ""},
	} {
		code, _, stderr := e.run(c.args...)
		if code != c.code || !strings.Contains(stderr, c.want) {
			t.Errorf("%v: %d %s", c.args, code, stderr)
		}
	}
}

// TestAConfigurationRepository: the config names a configuration repository instead of repos,
// and lanes show says which layer set each value. (The fake forge has one repository, so it is
// its own configuration repository here; the engine's tests cover two.)
func TestAConfigurationRepository(t *testing.T) {
	e := setup(t)
	if err := os.WriteFile(e.cfg, []byte("version: 1\nfactory: {repo: o/r}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := e.run("--format", "json", "lanes", "show", "fmt")
	if code != 0 || !strings.Contains(out, `"config": {`) || !strings.Contains(out, `"run.command.argv": "repo@c0ffee"`) {
		t.Fatalf("%d %s %s", code, out, stderr)
	}
	if code, out, stderr := e.run("lanes", "show", "fmt"); code != 0 || !strings.Contains(out, "run.command.argv: repo@c0ffee") || !strings.Contains(out, "config:\n") {
		t.Fatalf("sources as text: %d %s %s", code, out, stderr)
	}
	if code, out, _ := e.run("--format", "json", "doctor"); !strings.Contains(out, `"factory"`) || !strings.Contains(out, "o/r at c0ffee") {
		t.Fatalf("doctor: %d %s", code, out)
	}
	if err := os.WriteFile(e.cfg, []byte("version: 1\nrepos: [o/r]\nfactory: {repo: o/r}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := e.run("items", "ls"); code != cli.ExitPolicy {
		t.Fatalf("repos and factory together: %d %s", code, stderr)
	}
}

// TestLanesValidateRepo: a repository's lanes.yaml is checked merged over the configuration
// repository's, as ynf reads it, so a layer that fails alone can be valid, and one that is wrong
// merged is still refused. Without --repo the file stands alone, as it always did.
func TestLanesValidateRepo(t *testing.T) {
	e := setup(t)
	layer := filepath.Join(e.dir, "layer.yaml")
	if err := os.WriteFile(layer, []byte("version: 1\nlanes:\n  fmt:\n    enabled: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := e.run("lanes", "validate", "--file", layer); code != cli.ExitPolicy {
		t.Fatalf("a layer alone: %d", code)
	}
	// No configuration repository: the file is the whole policy, with or without --repo.
	if code, _, _ := e.run("lanes", "validate", "--repo", "o/r", "--file", layer); code != cli.ExitPolicy {
		t.Fatalf("a layer with nothing under it: %d", code)
	}
	whole := filepath.Join(e.dir, "whole.yaml")
	_ = os.WriteFile(whole, []byte(lanes), 0o644)
	if code, out, stderr := e.run("lanes", "validate", "--repo", "o/r", "--file", whole); code != 0 || !strings.Contains(out, "valid, 2 lanes") || strings.Contains(out, "merged over") {
		t.Fatalf("a whole file with no configuration repository: %d %s %s", code, out, stderr)
	}
	empty := filepath.Join(e.dir, "empty.yaml")
	_ = os.WriteFile(empty, nil, 0o644)
	if code, _, _ := e.run("lanes", "validate", "--repo", "o/r", "--file", empty); code != cli.ExitPolicy {
		t.Fatalf("an empty file with nothing under it: %d", code)
	}

	if err := os.WriteFile(e.cfg, []byte("version: 1\nfactory: {repo: o/r}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := e.run("lanes", "validate", "--repo", "o/r", "--file", layer)
	if code != 0 || !strings.Contains(out, "valid, 2 lanes (fmt, off)") || !strings.Contains(out, "merged over config@c0ffee of o/r") {
		t.Fatalf("a layer merged: %d %s %s", code, out, stderr)
	}
	if code, out, _ := e.run("--format", "json", "lanes", "validate", "--repo", "o/r", "--file", layer); code != 0 || !strings.Contains(out, `"sha": "c0ffee"`) || !strings.Contains(out, `"valid": true`) {
		t.Fatalf("json: %d %s", code, out)
	}
	if code, _, _ := e.run("lanes", "validate", "--file", layer); code != cli.ExitPolicy {
		t.Fatalf("without --repo the layer still stands alone: %d", code)
	}
	wrong := filepath.Join(e.dir, "wrong.yaml")
	_ = os.WriteFile(wrong, []byte("version: 1\nlanes:\n  fmt:\n    kind: nonsense\n"), 0o644)
	if code, _, stderr := e.run("lanes", "validate", "--repo", "o/r", "--file", wrong); code != cli.ExitPolicy || !strings.Contains(stderr, "/lanes/fmt/kind") {
		t.Fatalf("a layer wrong merged: %d %s", code, stderr)
	}
	unreadable := filepath.Join(e.dir, "tabs.yaml")
	_ = os.WriteFile(unreadable, []byte("lanes: [\n"), 0o644)
	if code, _, _ := e.run("lanes", "validate", "--repo", "o/r", "--file", unreadable); code != cli.ExitPolicy {
		t.Fatalf("a layer that is not YAML: %d", code)
	}

	// replay takes a layer too: it fails alone, so it is merged for the item's repository.
	if code, out, _ := e.run("sweep"); code != 0 {
		t.Fatalf("sweep: %d %s", code, out)
	}
	on := filepath.Join(e.dir, "on-layer.yaml")
	_ = os.WriteFile(on, []byte("version: 1\nlanes:\n  off:\n    enabled: true\n"), 0o644)
	if code, out, stderr := e.run("replay", "o/r#5", "--policy", on); code != cli.ExitDifferences || !strings.Contains(out, "DIFF") {
		t.Fatalf("replay under a layer: %d %s %s", code, out, stderr)
	}
	if code, _, _ := e.run("replay", "o/r#5", "--policy", wrong); code != cli.ExitPolicy {
		t.Fatalf("replay under a layer that is wrong merged: %d", code)
	}
	if code, _, _ := e.run("replay", "o/r#6", "--policy", on); code != cli.ExitPolicy {
		t.Fatalf("replay of an item that is not there: %d", code)
	}
}

// TestInspect: forges and trackers are listed and checked, a ticket reads without starting
// anything, and harness says how each lane runs.
func TestInspect(t *testing.T) {
	e := setup(t)
	if code, out, stderr := e.run("forges"); code != 0 || !strings.Contains(out, "reached o/r") {
		t.Fatalf("forges: %d %s %s", code, out, stderr)
	}
	if code, out, _ := e.run("--format", "json", "trackers"); code != 0 || !strings.Contains(out, `"its forge's issues"`) {
		t.Fatalf("trackers: %d %s", code, out)
	}
	if code, out, stderr := e.run("ticket", "o/r#5"); code != 0 || !strings.Contains(out, "state   open") || !strings.Contains(out, "labels  pkg:internal/format, ynf:off") {
		t.Fatalf("ticket: %d %s %s", code, out, stderr)
	}
	if code, _, _ := e.run("ticket", "o/r#9"); code == 0 {
		t.Fatal("a ticket that does not exist")
	}
	if code, _, _ := e.run("ticket"); code != cli.ExitUsage {
		t.Fatal("ticket needs a reference")
	}
	if code, out, stderr := e.run("harness"); code != 0 || !strings.Contains(out, "o/r\n") || !strings.Contains(out, "fmt: originate lane, command on") {
		t.Fatalf("harness: %d %s %s", code, out, stderr)
	}
	if code, out, _ := e.run("--format", "json", "doctor"); !strings.Contains(out, `"forge default"`) {
		t.Fatalf("doctor: %d %s", code, out)
	}
}

// TestDoctorNeedsDockerOnlyForDockerLanes: inside the factory image every run is inline, so a
// missing docker is not a problem there; where a lane runs in docker, it is.
func TestDoctorNeedsDockerOnlyForDockerLanes(t *testing.T) {
	e := setup(t)
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	bin := t.TempDir()
	if err := os.Symlink(gitBin, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if code, out, _ := e.run("doctor"); code == 0 || !strings.Contains(out, "FAIL  docker") {
		t.Fatalf("docker lanes with no docker: %d %s", code, out)
	}
	if err := os.WriteFile(e.cfg, []byte("version: 1\nrepos: [o/r]\nexecutor: inline\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := e.run("doctor"); code != 0 || !strings.Contains(out, "--    docker                   not needed") {
		t.Fatalf("inline: %d %s", code, out)
	}
}
