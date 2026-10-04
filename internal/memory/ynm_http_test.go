package memory_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/eyelock/ynf/internal/memory"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// hostedYnm starts a real `ynm serve --http` in front of a fresh bare store, with a static token,
// and returns its MCP endpoint, the store's path and the environment ynm runs in. It skips when
// ynm is not on PATH. Everything lives in a temp dir, with its own YNM_HOME, so the developer's
// own stores are never touched; the server is killed, with its process group, when the test ends.
func hostedYnm(t *testing.T, token string) (endpoint, store string, env []string) {
	t.Helper()
	bin, err := exec.LookPath("ynm")
	if err != nil {
		t.Skip("ynm is not on PATH")
	}
	dir := t.TempDir()
	store = filepath.Join(dir, "store")
	env = append(os.Environ(), "YNM_HOME="+filepath.Join(dir, "home"), "YNM_USER=ynf-test", "YNM_NO_CLAUDE_CLI=1")
	run := func(args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		c := exec.CommandContext(ctx, bin, args...)
		c.Env = env
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("ynm %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "--bare", store)
	run("init", "--cwd", store)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	logf, err := os.Create(filepath.Join(dir, "serve.log"))
	if err != nil {
		t.Fatal(err)
	}
	srv := exec.Command(bin, "serve", "--http", "--port", fmt.Sprint(port), "--token", token, "--no-personal", "--cwd", store)
	srv.Env = env
	srv.Stdout, srv.Stderr = logf, logf
	srv.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = srv.Wait(); close(exited) }()
	t.Cleanup(func() {
		_ = syscall.Kill(-srv.Process.Pid, syscall.SIGKILL)
		<-exited
		_ = logf.Close()
	})

	// ynm listens on localhost, so the name is used, not an address.
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := http.Get(fmt.Sprintf("http://localhost:%d/health", port))
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		select {
		case <-exited:
			b, _ := os.ReadFile(logf.Name())
			t.Fatalf("ynm serve exited early:\n%s", b)
		case <-time.After(200 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			b, _ := os.ReadFile(logf.Name())
			t.Fatalf("ynm serve not healthy within 30s:\n%s", b)
		}
	}
	return fmt.Sprintf("http://localhost:%d/mcp", port), store, env
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// recall asks a hosted ynm for the memories in a namespace through memory_recall, as any agent
// would, and returns the hits as the server reports them.
func recall(t *testing.T, endpoint, token, namespace string) []map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer "+token)
		return http.DefaultTransport.RoundTrip(r)
	})}
	s, err := mcp.NewClient(&mcp.Implementation{Name: "ynf-test", Version: "1"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: hc}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: "memory_recall", Arguments: map[string]any{"namespace": namespace}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("memory_recall: %v: %s", err, b)
	}
	return out.Data
}

// TestYnmHTTPAgainstARealServer: ynf's failure record, written over HTTP to a real `ynm serve
// --http`, is stored as sent: namespace, subject, tags, level, schema, data and source. The
// static token only decides who the writer is; it does not move a record that names a namespace.
// Skipped without ynm on PATH.
func TestYnmHTTPAgainstARealServer(t *testing.T) {
	endpoint, store, env := hostedYnm(t, "demo")
	y := &memory.YnmHTTP{Endpoint: endpoint, Token: "demo"}
	defer y.Close()
	r := memory.Record{Type: "episodic", Namespace: "factory/github.com/o/r", Level: "distributed", Subject: "sig/ci/lint",
		Summary: "sig/ci/lint on o/r, occurrence 1", Content: "lint failed",
		Tags:       []string{"ynf", "ynf.failure.v1", "failure", "occurrence"},
		DataSchema: "ynf.failure.v1", Data: map[string]any{"signature": "sig/ci/lint", "count": 1, "lane": "gofmt"}, Source: "ynf/step/01"}
	if err := y.Remember(context.Background(), r); err != nil {
		t.Fatal(err)
	}

	// memory_recall, the way any agent reads it: one hit, in ynf's namespace.
	hits := recall(t, endpoint, "demo", r.Namespace)
	if len(hits) != 1 {
		t.Fatalf("recall in %s: %v", r.Namespace, hits)
	}
	h := hits[0]
	tags, _ := h["tags"].([]any)
	if h["namespace"] != r.Namespace || h["level"] != "distributed" || h["type"] != "episodic" || h["subject"] != r.Subject || h["summary"] != r.Summary || fmt.Sprint(tags) != fmt.Sprint([]any{"ynf", "ynf.failure.v1", "failure", "occurrence"}) {
		t.Errorf("recalled %v", h)
	}
	if got := recall(t, endpoint, "demo", "common"); len(got) != 0 {
		t.Errorf("nothing belongs in common: %v", got)
	}

	// recall leaves out data, schema and source; the raw record has them.
	c := exec.Command("ynm", "export", "--cwd", store)
	c.Env = env
	out, err := c.Output()
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Namespace  string         `json:"namespace"`
		DataSchema string         `json:"dataSchema"`
		Data       map[string]any `json:"data"`
		Provenance struct {
			Actor  string `json:"actor"`
			Source string `json:"source"`
		} `json:"provenance"`
	}
	if lines := strings.Split(strings.TrimSpace(string(out)), "\n"); len(lines) != 1 {
		t.Fatalf("the store holds %d records, want 1:\n%s", len(lines), out)
	} else if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Namespace != r.Namespace || rec.DataSchema != "ynf.failure.v1" || rec.Provenance.Source != "ynf/step/01" ||
		rec.Data["signature"] != "sig/ci/lint" || rec.Data["lane"] != "gofmt" || rec.Data["count"] != float64(1) {
		t.Errorf("stored %+v", rec)
	}

	// A wrong token is refused, not silently dropped.
	bad := &memory.YnmHTTP{Endpoint: endpoint, Token: "wrong"}
	defer bad.Close()
	if err := bad.Remember(context.Background(), r); err == nil {
		t.Error("a wrong token should be refused")
	}
	if got := recall(t, endpoint, "demo", r.Namespace); len(got) != 1 {
		t.Errorf("a refused write stored something: %v", got)
	}
}
