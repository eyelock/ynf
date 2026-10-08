package engine_test

import (
	"context"
	"testing"
	"time"

	"github.com/eyelock/ynf/internal/engine"
	"github.com/eyelock/ynf/internal/lease"
	"github.com/eyelock/ynf/internal/telemetry"
	"github.com/eyelock/ynf/internal/tracker"
)

// TestAnExpiredLeaseIsCounted: a step that takes over a lease whose holder died counts it, by lane.
func TestAnExpiredLeaseIsCounted(t *testing.T) {
	h := newHarness(t)
	finish := withSpool(t, h)
	ctx := context.Background()
	h.f.labels[1] = []string{"ynf:fmt", "pkg:internal/format"}
	it, err := h.e.Start(ctx, engine.StartRequest{Ref: tracker.Ref{Host: "github.com", Key: "o/r#1"}, Lane: "fmt", Detach: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lease.Claim(ctx, h.e.Store, it.Key, "dead-worker", "s0", time.Minute, h.e.Now); err != nil {
		t.Fatal(err)
	}
	h.advance(2 * time.Minute)
	if _, err := h.e.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	var expired float64
	for _, p := range finish().Points {
		if p.Metric == telemetry.MetricLeaseExpired && p.Attrs.Str(telemetry.AttrLane) == "github.com/o/r#fmt" {
			expired += p.Value
		}
	}
	if expired != 1 {
		t.Fatalf("ynf.lease.expired = %v, want 1", expired)
	}
}
