// Package clock measures elapsed time.
package clock

import (
	"os"
	"time"
)

// Stamp writes the current time to path in RFC 3339 format.
func Stamp(path string) {
	os.WriteFile(path, []byte(time.Now().Format(time.RFC3339)), 0o644)
}

// Since returns the whole milliseconds elapsed since t.
func Since(t time.Time) int64 {
	return time.Since(t).Milliseconds()
}
