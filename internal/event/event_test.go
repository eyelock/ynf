package event_test

import (
	"testing"
	"time"

	"github.com/eyelock/ynf/internal/event"
)

func TestEvent(t *testing.T) {
	at := time.Date(2026, 10, 3, 12, 0, 0, 0, time.FixedZone("x", 3600))
	e := event.New("id", "src", event.RunFinished, "github.com/o/r#1", at, map[string]any{"outcome": "converged", "ok": true, "n": 3})
	if e.SpecVersion != "1.0" || e.Time.Location() != time.UTC {
		t.Fatalf("%+v", e)
	}
	if e.Str("outcome") != "converged" || e.Str("n") != "" || e.Str("missing") != "" {
		t.Fatal("Str")
	}
	if !e.Bool("ok") || e.Bool("outcome") {
		t.Fatal("Bool")
	}
	if e.String() != "ynf.run.finished github.com/o/r#1" {
		t.Fatal(e.String())
	}
}
