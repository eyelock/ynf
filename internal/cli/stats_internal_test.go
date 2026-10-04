package cli

import (
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/engine"
)

// TestModelCells: what a runner reported is averaged per run; what it never reported, such as a
// command's turns and tokens, is "-", not a measured zero.
func TestModelCells(t *testing.T) {
	cmd := strings.Join(modelCells(engine.ModelStats{Model: "none (command)", Runs: 1, Converged: 1, Proposed: 1}), " ")
	if cmd != "none (command) - 1 1 - - - - 1 0 0" {
		t.Errorf("a command: %q", cmd)
	}
	agent := strings.Join(modelCells(engine.ModelStats{Model: "claude (model not reported)", Effort: "medium", Runs: 2, Converged: 1,
		Turns: 3, Tokens: 7860, CacheRead: 462592, CostUSD: 0.46, Proposed: 1}), " ")
	if agent != "claude (model not reported) medium 2 1 1.5 3930 231296 $0.46 1 0 0" {
		t.Errorf("an agent: %q", agent)
	}
}
