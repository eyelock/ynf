package memory_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/memory"
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

func TestYnmContext(t *testing.T) {
	bin, calls := fakeYnm(t, 0)
	out, err := memory.Ynm{Bin: bin}.Context(context.Background(), "factory/o/r", "item/x sig/y", 800)
	if err != nil || out != "remembered: TestSince is a known flake" {
		t.Fatalf("%q %v", out, err)
	}
	b, _ := os.ReadFile(calls)
	if !strings.Contains(string(b), "--budget-tokens\n800\n--text\nitem/x sig/y\n") {
		t.Fatalf("%s", b)
	}
}

func TestYnmFailure(t *testing.T) {
	bin, _ := fakeYnm(t, 1)
	if err := (memory.Ynm{Bin: bin}).Remember(context.Background(), memory.Record{Type: "episodic", Content: "x", Namespace: "n"}); err == nil {
		t.Fatal("a failed ynm call should be reported")
	}
	if _, err := (memory.Ynm{Bin: bin}).Context(context.Background(), "n", "", 10); err == nil {
		t.Fatal("a failed context call should be reported")
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
