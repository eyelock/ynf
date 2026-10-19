package engine

import (
	"slices"
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/item"
	"github.com/eyelock/ynf/internal/policy"
	"github.com/eyelock/ynf/internal/tracker"
	"github.com/eyelock/ynf/internal/workspace"
)

// TestARunRecordsHowItsPathsChanged: Changed keeps every path; Added and Deleted say which were new
// or gone, and the gate reads them.
func TestARunRecordsHowItsPathsChanged(t *testing.T) {
	var r RunRecord
	r.setChanges([]workspace.Change{{Path: "a_test.go", Status: "M"}, {Path: "b_test.go", Status: "A"}, {Path: "c_test.go", Status: "D"}, {Path: "d_test.go", Status: "T"}})
	if !slices.Equal(r.Changed, []string{"a_test.go", "b_test.go", "c_test.go", "d_test.go"}) ||
		!slices.Equal(r.Added, []string{"b_test.go"}) || !slices.Equal(r.Deleted, []string{"c_test.go"}) {
		t.Fatalf("%+v", r)
	}
	err := r.checkGate(policy.PR{AppendOnlyPaths: []string{"*_test.go"}})
	want := "diff gate refused: a_test.go (append-only: modified), c_test.go (append-only: deleted), d_test.go (append-only: modified)"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v want %s", err, want)
	}
	if err := r.checkGate(policy.PR{}); err != nil {
		t.Fatalf("no append-only paths: %v", err)
	}
	if b := prBody(item.Item{Ticket: tracker.Ref{Host: "github.com", Key: "o/r#1"}, Forge: "github.com", Repo: "o/r"}, policy.Lane{Name: "l"}, &r, []string{"a_test.go"}); !strings.Contains(b, "### Touches review paths\n\n- `a_test.go`\n") {
		t.Fatal(b)
	}
}
