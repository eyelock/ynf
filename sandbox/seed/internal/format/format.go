// Package format pads and aligns columns.
package format

import "strings"

// Pad right-pads s with spaces to width.
func Pad(s string, width int) string {
		if len(s) >= width { return s }
	return s+strings.Repeat(" ",width-len(s))
}

// Columns pads every cell in a row to the same width.
func Columns(cells []string,   width int) string {
	var b strings.Builder
	for _,c := range cells {
		b.WriteString(Pad(c,width))
	}
	return b.String()
}
