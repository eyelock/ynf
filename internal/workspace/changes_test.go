package workspace_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/eyelock/ynf/internal/workspace"
)

func TestParseNameStatus(t *testing.T) {
	got := workspace.ParseNameStatus("A\tnew_test.go\nM\told_test.go\nD\tgone_test.go\nT\tlink.go\nD\tfrom.go\nA\tto.go\n")
	want := []workspace.Change{{Path: "new_test.go", Status: "A"}, {Path: "old_test.go", Status: "M"}, {Path: "gone_test.go", Status: "D"},
		{Path: "link.go", Status: "T"}, {Path: "from.go", Status: "D"}, {Path: "to.go", Status: "A"}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if len(workspace.ParseNameStatus("")) != 0 {
		t.Fatal("empty output has no changes")
	}
}

// TestChangesTellAddModifyDeleteTypeChangeAndRename: a rename is not followed, so it is a deletion
// and an addition, and Changed lists the same paths.
func TestChangesTellAddModifyDeleteTypeChangeAndRename(t *testing.T) {
	ctx := context.Background()
	bare := remote(t)
	w := workspace.Workspace{Root: t.TempDir(), RemoteURL: func(string) string { return bare }}
	mirror, err := w.Mirror(ctx, "o/r")
	if err != nil {
		t.Fatal(err)
	}
	wt, err := w.Checkout(ctx, mirror, "main", filepath.Join(t.TempDir(), "step", "wt"))
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(wt, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("b.go", "package a\n")
	write("c.go", "package a\n\nvar C = 1\n")
	write("d.go", "package a\n\nvar D = 2\n")
	run(t, wt, "git", "add", "-A")
	run(t, wt, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "more")

	write("a.go", "package a\n\nvar X = 1\n") // modified
	write("new.go", "package a\n")            // added
	for _, f := range []string{"b.go", "c.go"} {
		if err := os.Remove(filepath.Join(wt, f)); err != nil { // b deleted; c becomes a symlink
			t.Fatal(err)
		}
	}
	if err := os.Symlink("a.go", filepath.Join(wt, "c.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(wt, "d.go"), filepath.Join(wt, "e.go")); err != nil { // renamed
		t.Fatal(err)
	}

	cs, err := w.Changes(ctx, wt)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, c := range cs {
		got[c.Path] = c.Status
	}
	want := map[string]string{"a.go": "M", "new.go": "A", "b.go": "D", "c.go": "T", "d.go": "D", "e.go": "A"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for p, st := range want {
		if got[p] != st {
			t.Errorf("%s: %q, want %q", p, got[p], st)
		}
	}
	if paths, err := w.Changed(ctx, wt); err != nil || len(paths) != len(want) {
		t.Fatalf("changed %v %v", paths, err)
	}
}
