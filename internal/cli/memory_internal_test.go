package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/eyelock/ynf/internal/config"
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
	if m, ns := memoryFor(load("version: 1\nrepos: [o/r]\n")); m != nil || ns("github.com/o/r") != "factory/github.com/o/r" {
		t.Fatal("no ynm on PATH: no memory")
	}
	if m, _ := memoryFor(load("version: 1\nrepos: [o/r]\nmemory: {provider: ynm}\n")); m == nil {
		t.Fatal("explicit ynm")
	}
	bin := t.TempDir()
	_ = os.WriteFile(filepath.Join(bin, "ynm"), []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("PATH", bin)
	if m, _ := memoryFor(load("version: 1\nrepos: [o/r]\n")); m == nil {
		t.Fatal("ynm on PATH should be detected")
	}
	if m, _ := memoryFor(load("version: 1\nrepos: [o/r]\nmemory: {provider: none}\n")); m != nil {
		t.Fatal("provider none wins over detection")
	}
}
