package executor

import (
	"context"
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/telemetry"
)

// TestARunsProcessGetsTheTraceContext: the process executor starts the run's command with
// TRACEPARENT from the context, so what it starts joins the step's trace (ynr ADR-006, rule 4).
func TestARunsProcessGetsTheTraceContext(t *testing.T) {
	t.Setenv("TRACEPARENT", "")
	tel := telemetry.Setup(context.Background(), telemetry.Options{Environ: []string{"YNR_SPOOL=" + t.TempDir()}})
	defer tel.Shutdown(context.Background())
	ctx, span := tel.Tracer().Start(context.Background(), "run")
	defer span.End()

	dir := t.TempDir()
	job := Job{Argv: []string{"sh", "-c", `printf %s "$TRACEPARENT"`}, Worktree: dir, RunDir: dir, Env: map[string]string{}}
	telemetry.Inject(ctx, job.Env) // as the engine does, which is how a container gets it too
	out, err := Process{}.Run(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	want := "-" + span.SpanContext().SpanID().String() + "-"
	if !strings.Contains(string(out.Stdout), want) {
		t.Fatalf("TRACEPARENT in the run is %q, want the run span %s", out.Stdout, want)
	}

	// For a container the same variable goes in with -e.
	args, err := Docker{}.Args(Job{Image: "img", Argv: []string{"x"}, Worktree: dir, RunDir: dir, Env: job.Env}, "n", "none")
	if err != nil || !strings.Contains(strings.Join(args, " "), "-e TRACEPARENT=00-") {
		t.Fatalf("docker args lack TRACEPARENT: %v %v", args, err)
	}
}
