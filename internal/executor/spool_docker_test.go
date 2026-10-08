package executor_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/executor"
)

// TestDockerRunSeesOnlyItsOwnSpoolFolder runs a real container: it writes into its own folder,
// which appears on the host, and finds nothing else of the spool beside it.
func TestDockerRunSeesOnlyItsOwnSpoolFolder(t *testing.T) {
	dockerTests(t)
	root, _ := filepath.EvalSymlinks(t.TempDir())
	own, other := filepath.Join(root, "runs", "r1"), filepath.Join(root, "runs", "r2")
	for _, d := range []string{own, other, filepath.Join(root, "manifests"), filepath.Join(root, "factory"), filepath.Join(root, "wt"), filepath.Join(root, "rd")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	out, err := executor.Docker{Bin: "docker"}.Run(context.Background(), executor.Job{
		Argv:     []string{"sh", "-c", `echo "$YNR_SPOOL"; echo '{}' > "$YNR_SPOOL/a.jsonl"; ls -A /run/ynr; ls -A "$YNR_SPOOL"; env | grep -c OTEL_EXPORTER || true`},
		Worktree: filepath.Join(root, "wt"), RunDir: filepath.Join(root, "rd"), Image: "golang:1.26-alpine", Spool: own, NoOTLP: true,
		Env: map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://x"},
	})
	if err != nil || out.Exit != 0 {
		t.Fatalf("%v %+v %s", err, out, out.Stderr)
	}
	if got := strings.Fields(string(out.Stdout)); !slices.Equal(got, []string{executor.SpoolDir, "spool", "a.jsonl", "0"}) {
		t.Errorf("the run saw %q, want its folder at %s with nothing beside it, and no OTLP variable", got, executor.SpoolDir)
	}
	if _, err := os.Stat(filepath.Join(own, "a.jsonl")); err != nil {
		t.Errorf("what the run wrote is not in its folder on the host: %v", err)
	}
	if ents, _ := os.ReadDir(other); len(ents) != 0 {
		t.Errorf("another run's folder was written: %v", ents)
	}
}
