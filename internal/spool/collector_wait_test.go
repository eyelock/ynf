package spool

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestStopWaitsForClosedFilesToBeShipped: ynr serve deletes a closed file once it is shipped, so
// the last file ynf closed is waited for before ynr serve is told to stop; an open file is not.
func TestStopWaitsForClosedFilesToBeShipped(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"factory", "runs/r1"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	closed := filepath.Join(root, "runs", "r1", "ynh-1.jsonl")
	for _, f := range []string{closed, filepath.Join(root, "factory", "ynf-1.open.jsonl")} {
		if err := os.WriteFile(f, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if !closedFiles(root) {
		t.Fatal("a closed file in runs/ was not seen")
	}
	s := &Serve{root: root, done: make(chan struct{})}
	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = os.Remove(closed)
	}()
	start := time.Now()
	s.waitShipped(time.Now().Add(5 * time.Second))
	if d := time.Since(start); d < 150*time.Millisecond || d > 3*time.Second {
		t.Errorf("waited %s for a file that was shipped after 200ms", d)
	}
	if closedFiles(root) {
		t.Error("an open file counts as closed")
	}

	// A file that is never shipped costs the deadline, no more.
	stuck := filepath.Join(root, "factory", "ynf-2.jsonl")
	if err := os.WriteFile(stuck, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	s.waitShipped(time.Now().Add(300 * time.Millisecond))
	if d := time.Since(start); d < 250*time.Millisecond || d > 3*time.Second {
		t.Errorf("waited %s, want the 300ms deadline", d)
	}
	// And a ynr serve that has ended is not waited for.
	close(s.done)
	start = time.Now()
	s.waitShipped(time.Now().Add(5 * time.Second))
	if time.Since(start) > time.Second {
		t.Error("waited for a ynr serve that had ended")
	}
}
