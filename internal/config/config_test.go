package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eyelock/ynf/internal/config"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFindPrefersAgentsAndReportsShadowed(t *testing.T) {
	home := t.TempDir()
	if _, _, err := config.Find(home); err == nil {
		t.Fatal("found a config in an empty home")
	}
	write(t, filepath.Join(home, ".ynf/config.yaml"), "version: 1\nrepos: [o/r]\n")
	write(t, filepath.Join(home, ".agents/factory/config.yaml"), "version: 1\nrepos: [o/r]\n")
	p, shadowed, err := config.Find(home)
	if err != nil || p != filepath.Join(home, ".agents/factory/config.yaml") || len(shadowed) != 1 || !strings.HasSuffix(shadowed[0], ".ynf/config.yaml") {
		t.Fatalf("%s %v %v", p, shadowed, err)
	}
}

func TestLoadDefaults(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	write(t, p, "version: 1\nrepos: [eyelock/ynf-sandbox]\n")
	c, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	db, err := c.SQLitePath()
	if err != nil || db != filepath.Join(dir, "state.db") || c.WorkPath() != filepath.Join(dir, "work") {
		t.Fatalf("%s %s %v", db, c.WorkPath(), err)
	}
	ttl, hb, ci, review, err := c.Durations()
	if err != nil || ttl != 90*time.Second || hb != 30*time.Second || ci != 30*time.Second || review != 5*time.Minute {
		t.Fatalf("%v %v %v %v %v", ttl, hb, ci, review, err)
	}
	if n, e := c.Author(); n != "ynf" || e == "" {
		t.Fatal("default author")
	}
	if !strings.HasPrefix(c.Owner, "ynf@") {
		t.Fatal(c.Owner)
	}
	t.Setenv("GITHUB_TOKEN", "tok")
	if tok, err := c.Token(); err != nil || tok != "tok" {
		t.Fatalf("%q %v", tok, err)
	}
}

func TestLoadRejectsAndOverrides(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.yaml")
	write(t, bad, "version: 1\nrepos: [not a repo]\n")
	if _, err := config.Load(bad); err == nil || !strings.Contains(err.Error(), "schema") {
		t.Fatalf("bad repo accepted: %v", err)
	}
	if _, err := config.Load(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Fatal("missing file loaded")
	}
	p := filepath.Join(dir, "c.yaml")
	write(t, p, `version: 1
repos: [o/r]
store: sqlite:///abs/state.db
lease: {ttl: 10s, heartbeat: 20s}
github: {author: {name: bot, email: bot@x}}
`)
	c, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if db, _ := c.SQLitePath(); db != "/abs/state.db" {
		t.Fatal(db)
	}
	if _, _, _, _, err := c.Durations(); err == nil {
		t.Fatal("heartbeat longer than ttl accepted")
	}
	if n, _ := c.Author(); n != "bot" {
		t.Fatal(n)
	}
	c.Store = "s3://bucket/x"
	if _, err := c.SQLitePath(); err == nil || c.StoreKind() != "s3" {
		t.Fatal("an s3 store has no sqlite path")
	}
}

func TestMemorySettings(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	write(t, p, "version: 1\nrepos: [o/r]\n")
	c, _ := config.Load(p)
	if on, ns, cwd := c.MemorySettings(); on != nil || ns != "factory/{repo}" || cwd != "" {
		t.Fatalf("defaults: %v %s %s", on, ns, cwd)
	}
	write(t, p, "version: 1\nrepos: [o/r]\nmemory: {provider: none}\n")
	c, _ = config.Load(p)
	if on, _, _ := c.MemorySettings(); on == nil || *on {
		t.Fatal("provider none")
	}
	write(t, p, "version: 1\nrepos: [o/r]\nmemory: {provider: ynm, namespace: \"team/{repo}\", cwd: mem}\n")
	c, _ = config.Load(p)
	if on, ns, cwd := c.MemorySettings(); on == nil || !*on || ns != "team/{repo}" || cwd != filepath.Join(dir, "mem") {
		t.Fatalf("explicit: %v %s %s", on, ns, cwd)
	}
}

// TestMemoryTransport: cli by default; over http, the endpoint and token variable, and distributed
// unless a level is given, since a hosted store keeps nothing personal (ADR-008).
func TestMemoryTransport(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	for _, c := range []struct{ yaml, transport, endpoint, tokenEnv, level string }{
		{"version: 1\nrepos: [o/r]\n", "cli", "", "", ""},
		{"version: 1\nrepos: [o/r]\nmemory: {level: distributed}\n", "cli", "", "", "distributed"},
		{"version: 1\nrepos: [o/r]\nmemory: {transport: http, endpoint: \"https://ynm/mcp\", token_env: T}\n", "http", "https://ynm/mcp", "T", "distributed"},
		{"version: 1\nrepos: [o/r]\nmemory: {transport: http, endpoint: \"https://ynm/mcp\", token_env: T, level: personal}\n", "http", "https://ynm/mcp", "T", "personal"},
	} {
		write(t, p, c.yaml)
		cfg, err := config.Load(p)
		if err != nil {
			t.Fatal(err)
		}
		tr, ep, te, lv := cfg.MemoryTransport()
		if tr != c.transport || ep != c.endpoint || te != c.tokenEnv || lv != c.level {
			t.Errorf("%s: %s %s %s %s", c.yaml, tr, ep, te, lv)
		}
	}
	write(t, p, "version: 1\nrepos: [o/r]\nmemory: {transport: ftp}\n")
	if _, err := config.Load(p); err == nil {
		t.Fatal("an unknown transport")
	}
}
