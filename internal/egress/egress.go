// Package egress is ynf's allow-list proxy (ADR-007). A contained run's only route out is through
// it: HTTPS by CONNECT and plain HTTP by forwarding, each to a host the lane allows. Every request,
// allowed or denied, is logged as one JSON line, so a run that needed a host the lane does not
// allow says exactly which, and the denial becomes a failure signature (ADR-008).
package egress

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

// Allowed reports whether host matches the allow list. An entry is a host name, or "*.example.com"
// for any subdomain of example.com (not example.com itself).
func Allowed(allow []string, host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, a := range allow {
		a = strings.ToLower(a)
		if suffix, ok := strings.CutPrefix(a, "*."); ok {
			if strings.HasSuffix(host, "."+suffix) {
				return true
			}
			continue
		}
		if host == a {
			return true
		}
	}
	return false
}

// Entry is one logged request.
type Entry struct {
	Time    time.Time `json:"time"`
	Method  string    `json:"method"`
	Host    string    `json:"host"`
	Port    string    `json:"port"`
	Allowed bool      `json:"allowed"`
	Error   string    `json:"error,omitempty"`
}

// Proxy is the allow-list proxy.
type Proxy struct {
	Allow []string
	Log   io.Writer
	Dial  func(network, addr string) (net.Conn, error) // default net.DialTimeout with 10s

	mu sync.Mutex
}

func (p *Proxy) log(e Entry) {
	if p.Log == nil {
		return
	}
	e.Time = time.Now().UTC()
	b, _ := json.Marshal(e)
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = p.Log.Write(append(b, '\n'))
}

func (p *Proxy) dial(addr string) (net.Conn, error) {
	if p.Dial != nil {
		return p.Dial("tcp", addr)
	}
	return net.DialTimeout("tcp", addr, 10*time.Second)
}

// ServeHTTP implements http.Handler.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	p.forward(w, r)
}

func (p *Proxy) connect(w http.ResponseWriter, r *http.Request) {
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		host, port = r.Host, "443"
	}
	e := Entry{Method: http.MethodConnect, Host: host, Port: port}
	if !Allowed(p.Allow, host) {
		p.log(e)
		http.Error(w, "ynf egress: "+host+" is not in this lane's egress allow list", http.StatusForbidden)
		return
	}
	e.Allowed = true
	upstream, err := p.dial(net.JoinHostPort(host, port))
	if err != nil {
		e.Error = err.Error()
		p.log(e)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	p.log(e)
	_, _ = client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
	go pipe(upstream, buf.Reader, client)
	pipe(client, upstream, upstream)
}

// pipe copies src to dst, then closes both ends it was given.
func pipe(dst net.Conn, src io.Reader, closer io.Closer) {
	_, _ = io.Copy(dst, src)
	_ = dst.Close()
	_ = closer.Close()
}

func (p *Proxy) forward(w http.ResponseWriter, r *http.Request) {
	host := r.URL.Hostname()
	port := r.URL.Port()
	if port == "" {
		port = "80"
	}
	e := Entry{Method: r.Method, Host: host, Port: port}
	if !r.URL.IsAbs() || !Allowed(p.Allow, host) {
		p.log(e)
		http.Error(w, "ynf egress: "+host+" is not in this lane's egress allow list", http.StatusForbidden)
		return
	}
	e.Allowed = true
	out := r.Clone(r.Context())
	out.RequestURI = ""
	tr := &http.Transport{Proxy: nil}
	if p.Dial != nil {
		tr.DialContext = func(_ context.Context, network, addr string) (net.Conn, error) { return p.Dial(network, addr) }
	}
	resp, err := tr.RoundTrip(out)
	if err != nil {
		e.Error = err.Error()
		p.log(e)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	p.log(e)
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// Denied reads a log written by the proxy and returns the hosts it denied, sorted and unique.
// A missing log means the run never used the proxy.
func Denied(path string) ([]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var hosts []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) == nil && !e.Allowed && !slices.Contains(hosts, e.Host) {
			hosts = append(hosts, e.Host)
		}
	}
	slices.Sort(hosts)
	return hosts, sc.Err()
}
