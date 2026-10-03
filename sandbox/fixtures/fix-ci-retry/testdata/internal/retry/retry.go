// Package retry retries operations that fail transiently.
package retry

import "time"

// Do calls fn up to n times, sleeping wait between attempts, and returns the last error.
func Do(n int, wait time.Duration, fn func() error) error {
	var err error
	for i := 0; i < n; i++ {
		if err = fn(); err == nil {
			return nil
		}
		time.Sleep(wait)
	}
	return err
}

// Cleanup runs fn for best-effort teardown.
func Cleanup(fn func() error) {
	_ = fn()
}
