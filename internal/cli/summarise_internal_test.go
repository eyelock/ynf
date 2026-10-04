package cli

import (
	"encoding/json"
	"testing"

	"github.com/eyelock/ynf/internal/engine"
	"github.com/eyelock/ynf/internal/store"
)

// TestSummariseRun: the run line has no stray space where a run has no detail.
func TestSummariseRun(t *testing.T) {
	for detail, want := range map[string]string{
		"":          "r1 gofmt via host: converged, 1 changed (2s)",
		"no change": "r1 gofmt via host: converged no change, 1 changed (2s)",
	} {
		body, _ := json.Marshal(engine.RunRecord{RunID: "r1", Runner: "gofmt", Executor: "host", Outcome: "converged",
			Detail: detail, Changed: []string{"a.go"}, Duration: "2s"})
		if got := summarise(store.LogEntry{Kind: "run", Body: body}); got != want {
			t.Errorf("detail %q: got %q, want %q", detail, got, want)
		}
	}
}
