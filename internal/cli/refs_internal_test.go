package cli

import "testing"

// TestReferences: a reference resolves to the system's host and the tracker's own key before
// anything is stored; a shorthand means the configured forge (ADR-002, ADR-011).
func TestReferences(t *testing.T) {
	for in, want := range map[string]string{
		"o/r#5":                            "item/github.acme.internal/o/r/issues/5",
		"github.com/eyelock/ynh#77":        "item/github.com/eyelock/ynh/issues/77",
		"acme.atlassian.net/PLAT-881":      "item/acme.atlassian.net/PLAT-881",
		"item/acme.atlassian.net/PLAT-881": "item/acme.atlassian.net/PLAT-881",
		"item/github.com/o/r/pulls/412":    "item/github.com/o/r/pulls/412",
	} {
		got, err := key(in, "github.acme.internal")
		if err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"o/r", "o#5", "o/r#0", "o/r#x", "/r#5", "a/b/c/d#5", "PLAT-881", "nohost/PLAT-881", "acme.atlassian.net/a/b", "acme.atlassian.net/"} {
		if k, err := key(bad, "github.com"); err == nil {
			t.Errorf("%q accepted as %q", bad, k)
		}
	}
}
