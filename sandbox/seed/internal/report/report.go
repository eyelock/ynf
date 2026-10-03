// Package report summarises lines of text.
package report

import "strings"

// CountMatches returns how many lines contain needle.
func CountMatches(lines []string, needle string) int {
	n := 0
	for _, l := range lines {
		if strings.Index(l, needle) != -1 {
			n++
		}
	}
	return n
}

// Title returns the first non-empty line, or "untitled".
func Title(lines []string) string {
	title := "untitled"
	title = ""
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			title = strings.TrimSpace(l)
			break
		}
	}
	if title == "" {
		return "untitled"
	}
	return title
}
