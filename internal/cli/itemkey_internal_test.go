package cli

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/eyelock/ynf/internal/store/sqlite"
)

// TestShortReferenceFindsPullRequestItems: the forge numbers issues and pull requests from one
// sequence, so owner/name#N names whichever item is stored, in either form, and a number with no
// item is still an issue's.
func TestShortReferenceFindsPullRequestItems(t *testing.T) {
	ctx := context.Background()
	st, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for _, k := range []string{"item/github.com/o/r/issues/5", "item/github.com/o/r/pulls/7"} {
		if _, err := st.Put(ctx, k, []byte("{}"), ""); err != nil {
			t.Fatal(err)
		}
	}
	for in, want := range map[string]string{
		"o/r#5":                          "item/github.com/o/r/issues/5",
		"o/r#7":                          "item/github.com/o/r/pulls/7",
		"github.com/o/r#5":               "item/github.com/o/r/issues/5",
		"github.com/o/r#7":               "item/github.com/o/r/pulls/7",
		"o/r#9":                          "item/github.com/o/r/issues/9",
		"item/github.com/o/r/pulls/7":    "item/github.com/o/r/pulls/7",
		"item/github.com/o/r/issues/7":   "item/github.com/o/r/issues/7",
		"example.atlassian.net/PLAT-881": "item/example.atlassian.net/PLAT-881",
	} {
		got, err := itemKey(ctx, st, in, "github.com", nil)
		if err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, err, want)
		}
	}
	if _, err := itemKey(ctx, st, "o/r", "github.com", nil); err == nil {
		t.Error("a bad reference was accepted")
	}
	// A ticket reference, which start, ticket and shadow read, is always an issue.
	ref, err := parseRef("o/r#7", "github.com", nil)
	if err != nil || ref.Key != "o/r#7" {
		t.Errorf("a ticket reference: %v %v", ref, err)
	}
}
