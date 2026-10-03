package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/policy"
)

// fakeTools puts stand-in ynh and docker first on PATH: docker knows no images, and ynh records
// how it was asked to build one.
func fakeTools(t *testing.T, ynhExit int) string {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	_ = os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\necho \"docker $*\" >> "+calls+"\nexit 1\n"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "ynh"), []byte("#!/bin/sh\necho \"ynh $*\" >> "+calls+"\necho building\nexit "+string(rune('0'+ynhExit))+"\n"), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

func repoWithHarness(t *testing.T) string {
	t.Helper()
	wt := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wt, ".agents/harness"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(wt, ".agents/harness/plugin.json"), []byte(`{"name":"x"}`), 0o644)
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "x"}} {
		c := exec.Command("git", args...)
		c.Dir = wt
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	return wt
}

func TestBuildHarnessImage(t *testing.T) {
	wt := repoWithHarness(t)
	calls := fakeTools(t, 0)
	if imageBuilder() == nil {
		t.Fatal("ynh is on PATH; the builder should be available")
	}
	tag, err := buildHarnessImage(context.Background(), wt, policy.Ynh{Harness: ".", Base: "my-base:1"})
	if err != nil || !strings.HasPrefix(tag, "ynf-harness:") {
		t.Fatalf("%q %v", tag, err)
	}
	b, _ := os.ReadFile(calls)
	if !strings.Contains(string(b), "ynh image ynf-harness --from "+wt+" --entrypoint agent --tag "+tag+" --base my-base:1") {
		t.Fatalf("calls: %s", b)
	}
	again, _ := buildHarnessImage(context.Background(), wt, policy.Ynh{Harness: ".", Base: "my-base:1"})
	if again != tag {
		t.Fatal("the same commit, harness and base should give the same tag")
	}
	if other, _ := buildHarnessImage(context.Background(), wt, policy.Ynh{Harness: "."}); other == tag {
		t.Fatal("a different base should give a different tag")
	}
}

func TestBuildHarnessImageErrors(t *testing.T) {
	wt := repoWithHarness(t)
	fakeTools(t, 3)
	if _, err := buildHarnessImage(context.Background(), wt, policy.Ynh{Harness: "."}); err == nil || !strings.Contains(err.Error(), "building") {
		t.Fatalf("ynh failure: %v", err)
	}
	if _, err := buildHarnessImage(context.Background(), wt, policy.Ynh{Harness: "eyelock/ynh-lint@1.4"}); err == nil || !strings.Contains(err.Error(), "run.image") {
		t.Fatalf("an installed id: %v", err)
	}
	t.Setenv("PATH", t.TempDir())
	if imageBuilder() != nil {
		t.Fatal("no ynh on PATH: no builder")
	}
}
