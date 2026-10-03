package cli_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/eyelock/ynf/internal/cli"
)

type lockedBuffer struct {
	buf bytes.Buffer
	mu  chan struct{}
}

func newLocked() *lockedBuffer { return &lockedBuffer{mu: make(chan struct{}, 1)} }
func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu <- struct{}{}
	defer func() { <-b.mu }()
	return b.buf.Write(p)
}
func (b *lockedBuffer) String() string {
	b.mu <- struct{}{}
	defer func() { <-b.mu }()
	return b.buf.String()
}

func TestEgressProxyCommand(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer target.Close()
	allowed := strings.TrimPrefix(target.URL, "http://")
	host := allowed[:strings.LastIndex(allowed, ":")]

	logPath := filepath.Join(t.TempDir(), "egress.jsonl")
	ctx, cancel := context.WithCancel(context.Background())
	stdout := newLocked()
	done := make(chan int, 1)
	go func() {
		done <- cli.Run(ctx, []string{"egress-proxy", "--listen", "127.0.0.1:0", "--allow", host, "--log", logPath}, stdout, io.Discard)
	}()
	var addr string
	for range 100 {
		if m := regexp.MustCompile(`listening on (\S+),`).FindStringSubmatch(stdout.String()); m != nil {
			addr = m[1]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if addr == "" {
		t.Fatal("proxy never said it was listening")
	}
	u, _ := url.Parse("http://" + addr)
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u)}}
	resp, err := c.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("allowed host: %d", resp.StatusCode)
	}
	resp, err = c.Get("http://denied.example/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("denied host: %d", resp.StatusCode)
	}
	cancel()
	if code := <-done; code != 0 {
		t.Fatalf("exit %d", code)
	}
	b, _ := os.ReadFile(logPath)
	if !strings.Contains(string(b), `"host":"denied.example"`) {
		t.Fatalf("log: %s", b)
	}
	if code := cli.Run(context.Background(), []string{"egress-proxy", "--listen", "not-an-address"}, io.Discard, io.Discard); code == 0 {
		t.Fatal("bad listen address accepted")
	}
	if code := cli.Run(context.Background(), []string{"egress-proxy", "--log", "/no/such/dir/x"}, io.Discard, io.Discard); code == 0 {
		t.Fatal("unwritable log accepted")
	}
}
