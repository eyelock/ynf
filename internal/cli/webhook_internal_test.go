package cli

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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
	addr, err := (&app{}).listen(ctx, e, "127.0.0.1:0", "HOOK_SECRET")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("%v %v", resp, err)
	}
	_ = resp.Body.Close()
	if _, err := (&app{}).listen(ctx, e, "not-an-addr", "HOOK_SECRET"); err == nil {
		t.Fatal("bad address accepted")
	}
}
