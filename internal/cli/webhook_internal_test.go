package cli

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/eyelock/ynf/internal/forge"
	"github.com/eyelock/ynf/internal/tracker"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eyelock/ynf/internal/engine"
	"github.com/eyelock/ynf/internal/store/sqlite"
)

func TestWebhookReceiver(t *testing.T) {
	st, err := sqlite.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	e := &engine.Engine{Store: st, Repos: []string{"o/r"}, Log: slog.New(slog.DiscardHandler)}
	w := &webhooks{e: e, secret: "s3cret", queue: make(chan delivery, 1)}
	srv := httptest.NewServer(w)
	defer srv.Close()

	body := `{"repository":{"full_name":"o/r"},"issue":{"number":1}}`
	post := func(sig, id string) (int, string) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/webhook/github", strings.NewReader(body))
		req.Header.Set("X-Hub-Signature-256", sig)
		req.Header.Set("X-GitHub-Event", "issues")
		req.Header.Set("X-GitHub-Delivery", id)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	m := hmac.New(sha256.New, []byte("s3cret"))
	m.Write([]byte(body))
	good := "sha256=" + hex.EncodeToString(m.Sum(nil))

	if code, _ := post("sha256=00", "d1"); code != http.StatusUnauthorized {
		t.Fatalf("bad signature: %d", code)
	}
	if code, msg := post(good, "d1"); code != http.StatusAccepted || !strings.Contains(msg, "queued") {
		t.Fatalf("good: %d %s", code, msg)
	}
	if code, msg := post(good, "d1"); code != http.StatusOK || !strings.Contains(msg, "duplicate") {
		t.Fatalf("redelivery: %d %s", code, msg)
	}
	if code, _ := post(good, "d2"); code != http.StatusServiceUnavailable {
		t.Fatalf("a full queue should say so, and the sweep catches up: %d", code)
	}
	if d := <-w.queue; d.name != "issues" || string(d.body) != body {
		t.Fatalf("%+v", d)
	}
	for path, want := range map[string]int{"/healthz": 200, "/nope": 404} {
		resp, _ := http.Get(srv.URL + path)
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s: %d", path, resp.StatusCode)
		}
	}
	resp, _ := http.Get(srv.URL + "/webhook/github")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", resp.StatusCode)
	}

	// The worker hands queued events to the engine and stops with its context.
	ctx, cancel := context.WithCancel(context.Background())
	w.queue <- delivery{"issues", []byte(`{"repository":{"full_name":"x/y"},"issue":{"number":1}}`)}
	done := make(chan struct{})
	go func() { w.work(ctx); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
}

func TestListen(t *testing.T) {
	st, _ := sqlite.Open(filepath.Join(t.TempDir(), "s.db"))
	defer func() { _ = st.Close() }()
	e := &engine.Engine{Store: st, Log: slog.New(slog.DiscardHandler)}
	t.Setenv("HOOK_SECRET", "x")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr, err := (&app{}).listen(ctx, e, "127.0.0.1:0", "HOOK_SECRET", "YNF_START_TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("%v %v", resp, err)
	}
	_ = resp.Body.Close()
	if _, err := (&app{}).listen(ctx, e, "not-an-addr", "HOOK_SECRET", "YNF_START_TOKEN"); err == nil {
		t.Fatal("bad address accepted")
	}
}

// TestPostStart: POST /start exists only with a token, takes only that token, and records the
// instruction for the worker's loop.
func TestPostStart(t *testing.T) {
	lanes := "version: 1\nlanes:\n  fmt:\n    kind: originate\n    intake: [{github.search: \"label:ynf:fmt\", every: 5m}]\n    run: {runner: command, command: {argv: [\"true\"]}}\n    when: {converged: open_pr}\n"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch r.URL.Path {
		case "/repos/o/r":
			reply(map[string]any{"default_branch": "main"})
		case "/repos/o/r/branches/main":
			reply(map[string]any{"name": "main", "commit": map[string]any{"sha": "c0ffee"}})
		case "/repos/o/r/contents/.agents/factory/lanes.yaml":
			reply(map[string]any{"type": "file", "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(lanes))})
		case "/repos/o/r/issues/5":
			reply(map[string]any{"number": 5, "state": "open", "title": "t"})
		default:
			w.WriteHeader(http.StatusNotFound)
			reply(map[string]any{"message": "Not Found"})
		}
	}))
	defer api.Close()
	fg, err := forge.NewGitHub("tok", api.URL)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := sqlite.Open(filepath.Join(t.TempDir(), "s.db"))
	defer func() { _ = st.Close() }()
	var n atomic.Int64
	e := &engine.Engine{
		Store: st, Forge: fg, ForgeHost: fg.Host(), Trackers: map[string]tracker.Tracker{fg.Host(): forge.IssueTracker(fg)},
		Repos: []string{"o/r"}, Log: slog.New(slog.DiscardHandler), Now: time.Now,
		NewID: func() string { return fmt.Sprintf("%026d", n.Add(1)) },
	}
	post := func(w *webhooks, token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/start", strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		w.ServeHTTP(rec, r)
		return rec
	}
	if rec := post(&webhooks{e: e}, "s3cret", `{"ref":"o/r#5"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("without a configured token the endpoint must not exist: %d", rec.Code)
	}
	w := &webhooks{e: e, startToken: "s3cret"}
	for _, c := range []struct {
		token, body string
		code        int
		want        string
	}{
		{"", `{"ref":"o/r#5"}`, http.StatusUnauthorized, "bearer token"},
		{"wrong", `{"ref":"o/r#5"}`, http.StatusUnauthorized, "bearer token"},
		{"s3cret", `{`, http.StatusBadRequest, "start"},
		{"s3cret", `{"ref":"nope"}`, http.StatusBadRequest, "not a reference"},
		{"s3cret", `{"prompt":"p","repo":"a/b/c/d"}`, http.StatusBadRequest, "not owner/name"},
		{"s3cret", `{}`, http.StatusUnprocessableEntity, "a ticket reference or a prompt"},
		{"s3cret", `{"ref":"o/r#5"}`, http.StatusAccepted, `"item":"item/127.0.0.1/o/r/issues/5"`},
		{"s3cret", `{"prompt":"tidy","repo":"o/r"}`, http.StatusAccepted, `"item":"item/adhoc/`},
	} {
		rec := post(w, c.token, c.body)
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%q %s: %d %s", c.token, c.body, rec.Code, rec.Body.String())
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/start", nil)
	rec := httptest.NewRecorder()
	w.ServeHTTP(rec, r)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /start: %d", rec.Code)
	}
	if due, _ := st.Due(context.Background(), time.Now().Add(time.Second), 10); len(due) != 2 {
		t.Fatalf("both instructions should be due for the loop: %v", due)
	}
}
