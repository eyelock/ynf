package cli

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/eyelock/ynf/internal/policy"
	"github.com/eyelock/ynf/internal/runner"
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

// checkFolderIncludes refuses a lane whose harness is a folder in the working directory that
// includes another repository at anything but a full SHA: ynh's trust gate accepts a folder run's
// remote includes only at full SHAs. A lane whose folder is not here (it lives in the repository
// being enrolled, not in this directory) is left to the run, which makes the same check.
func checkFolderIncludes(f *policy.File) error {
	for _, name := range f.Names() {
		y := f.Lanes[name].Run.Ynh
		if y == nil || y.Harness == "" || strings.Contains(y.Harness, "@") {
			continue
		}
		fi, err := os.Stat(y.Harness)
		if err != nil || !fi.IsDir() {
			continue
		}
		h, err := runner.ReadHarness(y.Harness)
		if err != nil {
			continue
		}
		if err := h.CheckIncludes(); err != nil {
			return withCode(ExitPolicy, fmt.Errorf("lane %s: harness %s: %w", name, y.Harness, err))
		}
	}
	return nil
}
