package memory_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/memory"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeYnm records its arguments, one per line, and answers context with a fixed block.
func fakeYnm(t *testing.T, exit int) (string, string) {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "ynm")
	script := `#!/bin/sh
for a in "$@"; do printf '%s\n' "$a" >> "` + calls + `"; done
echo --- >> "` + calls + `"
[ "$1" = context ] && echo "remembered: TestSince is a known flake"
exit ` + string(rune('0'+exit)) + `
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, calls
}

func TestYnmRemember(t *testing.T) {
	bin, calls := fakeYnm(t, 0)
	y := memory.Ynm{Bin: bin, Cwd: "/somewhere"}
	err := y.Remember(context.Background(), memory.Record{
		Type: "episodic", Namespace: "factory/o/r", Subject: "sig/stuck/test", Summary: "stuck", Content: "the run was stuck",
		Tags: []string{"ynf", "lint"}, DataSchema: "ynf.failure.v1", Data: map[string]any{"count": 2}, Source: "ynf/step/01",
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(calls)
	got := string(b)
	for _, want := range []string{"remember\n--json\n--type\nepisodic\n", "--subject\nsig/stuck/test\n", "--tags\nynf,lint\n", `--data` + "\n" + `{"count":2}` + "\n", "--data-schema\nynf.failure.v1\n", "--source\nynf/step/01\n", "--cwd\n/somewhere\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("args lack %q:\n%s", want, got)
		}
	}
}

func TestYnmFailure(t *testing.T) {
	bin, _ := fakeYnm(t, 1)
	if err := (memory.Ynm{Bin: bin}).Remember(context.Background(), memory.Record{Type: "episodic", Content: "x", Namespace: "n"}); err == nil {
		t.Fatal("a failed ynm call should be reported")
	}
}

func TestDetect(t *testing.T) {
	bin, _ := fakeYnm(t, 0)
	t.Setenv("PATH", filepath.Dir(bin))
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("YNM_HOME", "")
	mk := func(p string) string {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	repo := mk(filepath.Join(home, "work", "repo"))
	mk(filepath.Join(repo, ".git"))
	deep := mk(filepath.Join(repo, "sub", "dir"))

	if m, why := memory.Detect(deep); m != nil || !strings.Contains(why, "no store") || !strings.Contains(why, repo) || !strings.Contains(why, filepath.Join(home, ".ynm")) {
		t.Fatalf("ynm without a store stays off, and says where it looked: %v %q", m, why)
	}
	mk(filepath.Join(home, ".ynm"))
	if m, why := memory.Detect(deep); m == nil || !strings.Contains(why, "the user store, ~/.ynm") {
		t.Fatalf("ynm on PATH and ~/.ynm should be detected as the user store: %v %q", m, why)
	}
	// The repro from #147: YNM_HOME names a folder that is not there. ynm writes there, so
	// ~/.ynm, which the repository sits under, is not the store.
	t.Setenv("YNM_HOME", filepath.Join(home, "no-such-home"))
	if m, why := memory.Detect(deep); m != nil || strings.Contains(why, "store at "+filepath.Join(home, ".ynm")) || !strings.Contains(why, "no-such-home") {
		t.Fatalf("YNM_HOME wins over ~/.ynm, and it has no store: %v %q", m, why)
	}
	ynmHome := mk(filepath.Join(home, "elsewhere"))
	t.Setenv("YNM_HOME", ynmHome)
	if m, why := memory.Detect(deep); m == nil || !strings.Contains(why, "from YNM_HOME") || !strings.Contains(why, ynmHome) {
		t.Fatalf("YNM_HOME is the user's store when set: %v %q", m, why)
	}
	t.Setenv("YNM_HOME", "")
	if err := os.RemoveAll(filepath.Join(home, ".ynm")); err != nil {
		t.Fatal(err)
	}

	// A repository's own .ynm/ is found from anywhere inside it.
	mk(filepath.Join(repo, ".ynm"))
	if m, why := memory.Detect(deep); m == nil || !strings.Contains(why, "the project store") || !strings.Contains(why, filepath.Join(repo, ".ynm")) {
		t.Fatalf("a .ynm/ at the repository root counts: %v %q", m, why)
	}
	// The project store is named before the user's.
	mk(filepath.Join(home, ".ynm"))
	if _, why := memory.Detect(deep); !strings.Contains(why, "the project store") {
		t.Fatalf("the project store is the repository's: %q", why)
	}
	if err := os.RemoveAll(filepath.Join(repo, ".ynm")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(home, ".ynm")); err != nil {
		t.Fatal(err)
	}

	// The walk stops at the repository root: a .ynm/ above it is not a project store.
	mk(filepath.Join(home, "work", ".ynm"))
	if m, _ := memory.Detect(deep); m != nil {
		t.Fatal("a .ynm/ above the repository root is not found")
	}
	// Outside any repository there is no project store, so a .ynm/ there is not found either.
	loose := mk(filepath.Join(t.TempDir(), "loose"))
	mk(filepath.Join(loose, ".ynm"))
	if m, why := memory.Detect(loose); m != nil || !strings.Contains(why, "not in a git repository") {
		t.Fatalf("no repository, no project store: %v %q", m, why)
	}

	// A linked worktree uses the main repository's store, as ynm does.
	main := mk(filepath.Join(home, "main"))
	mk(filepath.Join(main, ".git", "worktrees", "wt"))
	mk(filepath.Join(main, ".ynm"))
	wt := mk(filepath.Join(home, "wt"))
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+filepath.Join(main, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if m, why := memory.Detect(wt); m == nil || !strings.Contains(why, filepath.Join(main, ".ynm")) {
		t.Fatalf("a worktree's project store is the main repository's: %v %q", m, why)
	}
	// A .git file that names no worktree is still the repository root.
	odd := mk(filepath.Join(home, "odd"))
	if err := os.WriteFile(filepath.Join(odd, ".git"), []byte("nonsense"), 0o644); err != nil {
		t.Fatal(err)
	}
	mk(filepath.Join(odd, ".ynm"))
	if m, _ := memory.Detect(odd); m == nil {
		t.Fatal("a .git file is a repository root")
	}

	t.Setenv("PATH", t.TempDir())
	if m, why := memory.Detect(wt); m != nil || !strings.Contains(why, "not on PATH") {
		t.Fatalf("no ynm, no memory: %q", why)
	}
	if (memory.Ynm{}).Remember(context.Background(), memory.Record{Data: map[string]any{"bad": make(chan int)}}) == nil {
		t.Fatal("unencodable data should be an error")
	}
}

// TestYnmHTTP: a hosted ynm is written through its memory_remember tool, with the record's level
// and tags, and a tool error is reported.
func TestYnmHTTP(t *testing.T) {
	var got []map[string]any
	srv := mcp.NewServer(&mcp.Implementation{Name: "fake-ynm", Version: "1"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "memory_remember"}, func(_ context.Context, _ *mcp.CallToolRequest, in map[string]any) (*mcp.CallToolResult, map[string]any, error) {
		if in["content"] == "refuse" {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "no personal level here"}}}, nil, nil
		}
		got = append(got, in)
		return nil, map[string]any{"memoryId": "01M"}, nil
	})
	y := &memory.YnmHTTP{Endpoint: "in-memory", Transport: func() mcp.Transport {
		ct, st := mcp.NewInMemoryTransports()
		go func() { _, _ = srv.Connect(context.Background(), st, nil) }()
		return ct
	}}
	defer y.Close()
	r := memory.Record{Type: "episodic", Namespace: "factory/github.com/o/r", Level: "distributed", Subject: "sig/ci/lint",
		Summary: "s", Content: "c", Tags: []string{"ynf.failure.v1"}, DataSchema: "ynf.failure.v1", Data: map[string]any{"count": 1}, Source: "ynf/step/1"}
	if err := y.Remember(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0]["level"] != "distributed" || got[0]["dataSchema"] != "ynf.failure.v1" || got[0]["namespace"] != "factory/github.com/o/r" {
		t.Fatalf("%+v", got)
	}
	r.Content = "refuse"
	if err := y.Remember(context.Background(), r); err == nil || !strings.Contains(err.Error(), "no personal level here") {
		t.Fatalf("a refused write: %v", err)
	}
	dead := &memory.YnmHTTP{Endpoint: "http://127.0.0.1:1/mcp", Token: "t"}
	if err := dead.Remember(context.Background(), r); err == nil {
		t.Fatal("an unreachable ynm")
	}
}

func TestYnmLevel(t *testing.T) {
	bin, calls := fakeYnm(t, 0)
	if err := (memory.Ynm{Bin: bin}).Remember(context.Background(), memory.Record{Type: "episodic", Content: "x", Namespace: "n", Level: "distributed"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(calls); !strings.Contains(string(b), "--level\ndistributed\n") {
		t.Fatalf("%s", b)
	}
}
