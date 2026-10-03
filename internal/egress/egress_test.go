package egress_test

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/egress"
)

func TestAllowed(t *testing.T) {
	allow := []string{"proxy.golang.org", "*.githubusercontent.com"}
	for host, want := range map[string]bool{
		"proxy.golang.org":                  true,
		"PROXY.golang.org.":                 true,
		"evil-proxy.golang.org":             false,
		"raw.githubusercontent.com":         true,
		"githubusercontent.com":             false,
		"a.b.githubusercontent.com":         true,
		"www.iana.org":                      false,
		"proxy.golang.org.attacker.example": false,
	} {
		if got := egress.Allowed(allow, host); got != want {
			t.Errorf("%s: %v, want %v", host, got, want)
		}
	}
}

// upstream is a target server; the proxy dials it whatever host name the client asked for, so
// tests can use real-looking names.
func upstream(t *testing.T) (*httptest.Server, func(string, string) (net.Conn, error)) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "hello from %s", r.Host)
	}))
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	return srv, func(network, _ string) (net.Conn, error) { return net.Dial(network, addr) }
}

func proxy(t *testing.T, allow ...string) (*httptest.Server, *bytes.Buffer, func(string, string) (net.Conn, error)) {
	t.Helper()
	_, dial := upstream(t)
	var log bytes.Buffer
	p := &egress.Proxy{Allow: allow, Log: &syncWriter{w: &log}, Dial: dial}
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	return srv, &log, dial
}

type syncWriter struct{ w io.Writer }

func (s *syncWriter) Write(b []byte) (int, error) { return s.w.Write(b) }

func client(proxyURL string) *http.Client {
	u, _ := url.Parse(proxyURL)
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u)}}
}

func TestForwardAllowsAndDenies(t *testing.T) {
	srv, log, _ := proxy(t, "proxy.golang.org")
	c := client(srv.URL)

	resp, err := c.Get("http://proxy.golang.org/x")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "hello from proxy.golang.org") {
		t.Fatalf("allowed: %d %s", resp.StatusCode, body)
	}

	resp, err = c.Get("http://www.iana.org/assignments")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "www.iana.org is not in this lane's egress allow list") {
		t.Fatalf("denied: %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(log.String(), `"host":"www.iana.org","port":"80","allowed":false`) {
		t.Fatalf("log: %s", log)
	}
}

func TestConnectTunnelsAllowedHostsOnly(t *testing.T) {
	srv, log, _ := proxy(t, "proxy.golang.org")
	addr := strings.TrimPrefix(srv.URL, "http://")

	tunnel := func(host string) (int, net.Conn) {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fmt.Fprintf(conn, "CONNECT %s:443 HTTP/1.1\r\nHost: %s:443\r\n\r\n", host, host)
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, conn
	}

	code, conn := tunnel("proxy.golang.org")
	if code != 200 {
		t.Fatalf("allowed CONNECT: %d", code)
	}
	// Through the tunnel, speak plain HTTP to the upstream test server.
	_, _ = fmt.Fprint(conn, "GET / HTTP/1.1\r\nHost: proxy.golang.org\r\nConnection: close\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = conn.Close()
	if !strings.Contains(string(body), "hello from proxy.golang.org") {
		t.Fatalf("tunnel: %s", body)
	}

	code, conn = tunnel("www.iana.org")
	_ = conn.Close()
	if code != http.StatusForbidden {
		t.Fatalf("denied CONNECT: %d", code)
	}
	if !strings.Contains(log.String(), `"method":"CONNECT","host":"www.iana.org","port":"443","allowed":false`) {
		t.Fatalf("log: %s", log)
	}
}

func TestUpstreamFailureIsLogged(t *testing.T) {
	var log bytes.Buffer
	p := &egress.Proxy{Allow: []string{"x.example"}, Log: &log, Dial: func(string, string) (net.Conn, error) { return nil, fmt.Errorf("refused") }}
	srv := httptest.NewServer(p)
	defer srv.Close()
	resp, err := client(srv.URL).Get("http://x.example/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(log.String(), `"error":`) {
		t.Fatalf("%d %s", resp.StatusCode, log.String())
	}
	conn, _ := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	_, _ = fmt.Fprint(conn, "CONNECT x.example:443 HTTP/1.1\r\nHost: x.example:443\r\n\r\n")
	r, _ := http.ReadResponse(bufio.NewReader(conn), nil)
	_ = conn.Close()
	if r.StatusCode != http.StatusBadGateway {
		t.Fatalf("CONNECT upstream failure: %d", r.StatusCode)
	}
}

func TestDenied(t *testing.T) {
	dir := t.TempDir()
	if hosts, err := egress.Denied(filepath.Join(dir, "none.jsonl")); err != nil || hosts != nil {
		t.Fatalf("missing log: %v %v", hosts, err)
	}
	p := filepath.Join(dir, "egress.jsonl")
	_ = os.WriteFile(p, []byte(`{"host":"b.example","allowed":false}
{"host":"ok.example","allowed":true}
not json
{"host":"a.example","allowed":false}
{"host":"b.example","allowed":false}
`), 0o644)
	hosts, err := egress.Denied(p)
	if err != nil || strings.Join(hosts, ",") != "a.example,b.example" {
		t.Fatalf("%v %v", hosts, err)
	}
}
