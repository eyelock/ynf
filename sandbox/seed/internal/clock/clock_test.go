package clock

import (
	"math/rand/v2"
	"os"
	"testing"
	"time"
)

// TestSince is flaky on purpose: the sandbox uses it to show what a run that cannot converge looks
// like. It is skipped in CI so it only disturbs runs whose sensors include this package.
func TestSince(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("flaky by design; local sensors only")
	}
	start := time.Now()
	time.Sleep(time.Duration(rand.IntN(4)) * time.Millisecond)
	if got := Since(start); got > 2 {
		t.Fatalf("took %dms, want at most 2ms", got)
	}
}
