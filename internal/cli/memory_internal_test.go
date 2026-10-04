package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/eyelock/ynf/internal/config"
	"github.com/eyelock/ynf/internal/memory"
)

func TestMemoryFor(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	load := func(body string) *config.Config {
		_ = os.WriteFile(p, []byte(body), 0o644)
		c, err := config.Load(p)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	t.Setenv("PATH", t.TempDir())
	if m, ns, _, _ := memoryFor(load("version: 1\nrepos: [o/r]\n")); m != nil || ns("github.com/o/r") != "factory/github.com/o/r" {
		t.Fatal("no ynm on PATH: no memory")
	}
	if m, _, _, _ := memoryFor(load("version: 1\nrepos: [o/r]\nmemory: {provider: ynm}\n")); m == nil {
		t.Fatal("explicit ynm")
	}
	bin := t.TempDir()
	_ = os.WriteFile(filepath.Join(bin, "ynm"), []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("PATH", bin)
	if m, _, _, _ := memoryFor(load("version: 1\nrepos: [o/r]\n")); m == nil {
		t.Fatal("ynm on PATH should be detected")
	}
	if m, _, _, _ := memoryFor(load("version: 1\nrepos: [o/r]\nmemory: {provider: none}\n")); m != nil {
		t.Fatal("provider none wins over detection")
	}
	t.Setenv("YNM_TOKEN", "t0k")
	m, _, level, err := memoryFor(load("version: 1\nrepos: [o/r]\nmemory: {transport: http, endpoint: \"https://ynm.example/mcp\", token_env: YNM_TOKEN}\n"))
	if h, ok := m.(*memory.YnmHTTP); err != nil || !ok || h.Token != "t0k" || level != "distributed" {
		t.Fatalf("hosted ynm: %T %q %v", m, level, err)
	}
	if _, _, level, _ := memoryFor(load("version: 1\nrepos: [o/r]\nmemory: {provider: ynm, level: distributed}\n")); level != "distributed" {
		t.Fatal("an explicit level")
	}
	for _, bad := range []string{
		"version: 1\nrepos: [o/r]\nmemory: {transport: http, token_env: YNM_TOKEN}\n",
		"version: 1\nrepos: [o/r]\nmemory: {transport: http, endpoint: \"https://ynm.example/mcp\"}\n",
		"version: 1\nrepos: [o/r]\nmemory: {transport: http, endpoint: \"https://ynm.example/mcp\", token_env: NO_SUCH_TOKEN}\n",
	} {
		if _, _, _, err := memoryFor(load(bad)); err == nil {
			t.Errorf("accepted: %s", bad)
		}
	}
}
