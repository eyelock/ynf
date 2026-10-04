package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/eyelock/ynf/internal/config"
	"github.com/eyelock/ynf/internal/executor"
)

// TestAnInlineInstance: an instance that declares it runs inside containment the operator
// provides runs every lane inline, as the configured user (ADR-007).
func TestAnInlineInstance(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte("version: 1\nrepos: [o/r]\nexecutor: inline\ninline_user: runner\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	a := &app{cfg: c}
	for _, lane := range []string{"docker", "process"} {
		ex, err := a.executor(lane)
		in, ok := ex.(executor.Inline)
		if err != nil || !ok || in.User != "runner" {
			t.Fatalf("lane executor %s: %T %v", lane, ex, err)
		}
	}
	if err := os.WriteFile(p, []byte("version: 1\nrepos: [o/r]\nexecutor: docker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(p); err == nil {
		t.Fatal("only inline can be declared for the instance")
	}
}

// TestTheHarnessInstalledHere: in the factory image, the harness is read from the ynh installed
// beside ynf, not through docker.
func TestTheHarnessInstalledHere(t *testing.T) {
	bin := t.TempDir()
	script := `#!/bin/sh
case "$*" in
  "ls --format json") echo '{"harnesses":[{"id":"local/h","name":"h"}]}' ;;
  "info local/h --format json") echo '{"harness":{"manifest":{"focuses":{"tidy":{"prompt":"P"}}}}}' ;;
  *) exit 1 ;;
esac
`
	_ = os.WriteFile(filepath.Join(bin, "ynh"), []byte(script), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	h, err := imageHarness(context.Background(), "", "")
	if err != nil || h.ID != "local/h" || h.Focuses["tidy"].Prompt != "P" {
		t.Fatalf("%+v %v", h, err)
	}
}
