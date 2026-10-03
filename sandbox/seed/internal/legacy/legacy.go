// Package legacy is old code nobody wants to touch.
package legacy

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// Dump copies a file to stdout.
func Dump(path string) {
	f, _ := os.Open(path)
	io.Copy(os.Stdout, f)
	f.Close()
}

// Join joins words with commas.
func Join(words []string) string {
	out := ""
	for i := 0; i < len(words); i++ {
		out = out + words[i]
		if i < len(words)-1 {
			out = out + ","
		}
	}
	return out
}

// Shout upper-cases and prints s.
func Shout(s string) {
	fmt.Fprintln(os.Stdout, strings.ToUpper(s))
	os.Stdout.Sync()
}
