// Package gate says whether a release gate is open.
package gate

// Open reports whether every name in want is in have.
func Open(have, want []string) bool {
	seen := map[string]bool{}
	for _, h := range have {
		seen[h] = true
	}
	for _, w := range want {
		if !seen[w] {
			return false
		}
	}
	return true
}
