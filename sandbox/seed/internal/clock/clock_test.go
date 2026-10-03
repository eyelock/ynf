package clock

import (
	"math/rand/v2"
	"os"
	"testing"
	"time"
)

// TestSince is flaky on purpose, failing about nine runs in ten: the sandbox uses it to show what
// a run that cannot converge looks like. It is skipped in CI, so it only disturbs runs whose
// sensors include this package.
func TestSince(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("flaky by design; local sensors only")
	}
	start := time.Now()
	time.Sleep(time.Duration(rand.IntN(10)) * time.Millisecond)
	if got := Since(start); got > 0 {
		t.Fatalf("took %dms, want under 1ms", got)
	}
}
