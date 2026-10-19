// Package gate is the diff gate (ADR-007): before ynf pushes anything, the changed paths must be
// inside the lane's allowed paths and outside every protected path. It catches what a runner's own
// tamper checks cannot: changes to the files that judge the change.
package gate

import (
	"fmt"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// Protected are refused in every lane: CI, ownership, and every folder that holds ynf, ynh or ynm
// configuration, including the lane policy itself.
var Protected = []string{
	".github/workflows/**",
	".github/actions/**",
	"**/CODEOWNERS",
	".agents/**",
	".ynh/**",
	".ynm/**",
	".ynf/**",
}

// Kinds says how changed paths changed, beyond that they did. A path in neither list was modified
// (or changed type), so a record that predates change types reads every change as a modification.
type Kinds struct {
	Added   []string
	Deleted []string
}

// Check returns an error naming every path the gate refuses. An empty allowed list allows all.
func Check(changed, allowed, protected []string) error {
	return CheckKinds(changed, Kinds{}, allowed, protected, nil)
}

// CheckKinds is Check with append-only paths: a path matching one may be added, never modified,
// deleted or renamed (a rename is a deletion and an addition). Protected paths and the allowed list
// are judged first, so an append-only path is not a way round either.
func CheckKinds(changed []string, kinds Kinds, allowed, protected, appendOnly []string) error {
	var refused []string
	for _, p := range changed {
		switch {
		case matchAny(Protected, p) || matchAny(protected, p):
			refused = append(refused, p+" (protected)")
		case len(allowed) > 0 && !matchAny(allowed, p):
			refused = append(refused, p+" (outside the lane's allowed paths)")
		case matchAny(appendOnly, p) && !slices.Contains(kinds.Added, p):
			how := "modified"
			if slices.Contains(kinds.Deleted, p) {
				how = "deleted"
			}
			refused = append(refused, p+" (append-only: "+how+")")
		}
	}
	if len(refused) > 0 {
		return fmt.Errorf("diff gate refused: %s", strings.Join(refused, ", "))
	}
	return nil
}

// Touches returns the changed paths that match any of the globs, in order.
func Touches(changed, globs []string) []string {
	var out []string
	for _, p := range changed {
		if matchAny(globs, p) {
			out = append(out, p)
		}
	}
	return out
}

func matchAny(globs []string, p string) bool {
	for _, g := range globs {
		if ok, _ := doublestar.Match(g, p); ok {
			return true
		}
	}
	return false
}
