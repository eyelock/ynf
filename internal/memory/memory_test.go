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
	if memory.Detect() == nil {
		t.Fatal("ynm on PATH should be detected")
	}
	t.Setenv("PATH", t.TempDir())
	if memory.Detect() != nil {
		t.Fatal("no ynm, no memory")
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
