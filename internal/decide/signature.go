package decide

import (
	"slices"
	"strings"
	"unicode/utf8"
)

// maxSignature is the longest signature ynf writes: a signature is a ynm subject, and ynm's
// subjects stop at 200 characters.
const maxSignature = 200

// signature joins parts into one failure signature, "sig/" first. Free text (a check, a sensor, a
// harness name) is lower-cased and has its whitespace replaced with "-", so the same failure
// spelled twice clusters as one; the result is cut to maxSignature on a character boundary.
func signature(parts ...string) string {
	s := "sig/" + strings.Join(parts, "/")
	s = strings.ToLower(strings.Join(strings.Fields(s), "-"))
	if len(s) <= maxSignature {
		return s
	}
	s = s[:maxSignature]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// sortedUnique is names in a stable order, each once: the same set of failures always yields the
// same signatures in the same order, whatever order the source listed them in.
func sortedUnique(names []string) []string {
	out := slices.Clone(names)
	slices.Sort(out)
	return slices.Compact(out)
}
