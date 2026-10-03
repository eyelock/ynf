// Package gate is the diff gate (ADR-007): before ynf pushes anything, the changed paths must be
// inside the lane's allowed paths and outside every protected path. It catches what a runner's own
// tamper checks cannot: changes to the files that judge the change.
package gate

import (
	"fmt"
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

// Check returns an error naming every path the gate refuses. An empty allowed list allows all.
func Check(changed, allowed, protected []string) error {
	var refused []string
	for _, p := range changed {
		switch {
		case matchAny(Protected, p) || matchAny(protected, p):
			refused = append(refused, p+" (protected)")
		case len(allowed) > 0 && !matchAny(allowed, p):
			refused = append(refused, p+" (outside the lane's allowed paths)")
		}
	}
	if len(refused) > 0 {
		return fmt.Errorf("diff gate refused: %s", strings.Join(refused, ", "))
	}
	return nil
}

func matchAny(globs []string, p string) bool {
	for _, g := range globs {
		if ok, _ := doublestar.Match(g, p); ok {
			return true
		}
	}
	return false
}
