package cli

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/eyelock/ynf/internal/policy"
)

// checkPinRefs refuses a lane whose harness is pinned to a branch, or whose pin repository cannot
// be read, with the check `ynf harness` and a run make. It is the one thing `lanes validate` does
// over the network, and only for a lane that pins its harness.
func checkPinRefs(ctx context.Context, pins map[string]policy.Pin) error {
	for _, name := range slices.Sorted(maps.Keys(pins)) {
		if err := pins[name].CheckRef(ctx); err != nil {
			return withCode(ExitPolicy, fmt.Errorf("lane %s: %w", name, err))
		}
	}
	return nil
}
