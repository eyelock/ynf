package engine_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/engine"
	"github.com/eyelock/ynf/internal/item"
	"github.com/eyelock/ynf/internal/runner"
	"github.com/eyelock/ynf/internal/tracker"
)

const fmtPR = `    pr: {allowed_paths: ["**/*.go"]}
`

func withPR(t *testing.T, pr string) string {
	t.Helper()
	lanes := strings.Replace(lanesYAML, fmtPR, "    pr: "+pr+"\n", 1)
	if lanes == lanesYAML {
		t.Fatal("the test lane was not changed")
	}
	return lanes
}

// TestAppendOnlyRefusesAnEditedFile: the fmt lane edits internal/format/f.go, which exists, so an
// append-only match refuses it, escalates, and opens nothing.
func TestAppendOnlyRefusesAnEditedFile(t *testing.T) {
	h := newHarness(t)
	h.f.lanes = withPR(t, `{allowed_paths: ["**/*.go"], append_only_paths: ["internal/format/*.go"]}`)
	h.f.labels[1] = []string{"ynf:fmt", "pkg:internal/format"}
	if err := h.e.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	it := h.item(t, 1)
	if it.State != item.Escalated || !strings.Contains(it.Reason, "diff gate refused: internal/format/f.go (append-only: modified)") {
		t.Fatalf("%s: %s", it.State, it.Reason)
	}
	if len(h.f.opened) != 0 {
		t.Fatalf("opened %d pull requests", len(h.f.opened))
	}
}

// TestAppendOnlyAllowsOtherPaths: an append-only glob that matches nothing the run touched changes
// nothing.
func TestAppendOnlyAllowsOtherPaths(t *testing.T) {
	h := newHarness(t)
	h.f.lanes = withPR(t, `{allowed_paths: ["**/*.go"], append_only_paths: ["**/*_test.go"]}`)
	h.f.labels[1] = []string{"ynf:fmt", "pkg:internal/format"}
	if err := h.e.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if it := h.item(t, 1); it.State != item.Proposed || len(h.f.opened) != 1 {
		t.Fatalf("%s: %s", it.State, it.Reason)
	}
}

// TestReviewPathsLabelAndListTheTouchedPaths: a change under a review path opens the pull request
// as normal, with the label and the body section; one elsewhere gets neither.
func TestReviewPathsLabelAndListTheTouchedPaths(t *testing.T) {
	for _, c := range []struct {
		name, glob string
		touches    bool
	}{{"touched", "internal/format/**", true}, {"untouched", "docs/**", false}} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.f.lanes = withPR(t, `{allowed_paths: ["**/*.go"], review_paths: ["`+c.glob+`"]}`)
			h.f.labels[1] = []string{"ynf:fmt", "pkg:internal/format"}
			if err := h.e.Sweep(context.Background()); err != nil {
				t.Fatal(err)
			}
			if it := h.item(t, 1); it.State != item.Proposed || len(h.f.opened) != 1 {
				t.Fatalf("%s: %s", it.State, it.Reason)
			}
			body := h.f.opened[0].Body
			has := strings.Contains(body, "### Touches review paths\n\n- `internal/format/f.go`\n")
			labelled := slices.Contains(h.f.labels[101], engine.ReviewPathsLabel)
			if has != c.touches || labelled != c.touches {
				t.Fatalf("section %v label %v, want %v\n%s", has, labelled, c.touches, body)
			}
			if engine.ReviewPathsLabel != "ynf:review-paths" {
				t.Fatalf("label %q", engine.ReviewPathsLabel)
			}
		})
	}
}

// TestShadowAgreesWithTheLiveGate: shadow asks the same question of the same change types.
func TestShadowAgreesWithTheLiveGate(t *testing.T) {
	for _, c := range []struct {
		name, pr string
		accepted bool
		detail   string
	}{
		{"append-only match", `{allowed_paths: ["**/*.go"], append_only_paths: ["internal/format/*.go"]}`, false, "internal/format/f.go (append-only: modified)"},
		{"no match", `{allowed_paths: ["**/*.go"], append_only_paths: ["docs/**"]}`, true, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newShadowHarness(t)
			h.closedFixed(1, "ynf:fmt", "pkg:internal/format")
			h.f.lanes = withPR(t, c.pr)
			run, err := h.e.ShadowRun(context.Background(), engine.ShadowRequest{Lane: "fmt", Tickets: []tracker.Ref{{Host: "github.com", Key: "o/r#1"}}})
			if err != nil {
				t.Fatal(err)
			}
			as := h.attempts(t, run)
			if len(as) != 1 || as[0].Outcome != runner.Converged || as[0].GateAccepted != c.accepted || !strings.Contains(as[0].GateDetail, c.detail) {
				t.Fatalf("%+v", as)
			}
		})
	}
}

// TestARunRecordFromBeforeChangeTypesStillLoads: Added and Deleted are new and optional, and a
// record without them reads as every change being a modification.
func TestARunRecordFromBeforeChangeTypesStillLoads(t *testing.T) {
	var old engine.RunRecord
	if err := json.Unmarshal([]byte(`{"run_id":"r1","runner":"command","outcome":"converged","changed":["a_test.go"],"step_dir":"x","duration":"1s"}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.Added != nil || old.Deleted != nil || !slices.Equal(old.Changed, []string{"a_test.go"}) {
		t.Fatalf("%+v", old)
	}
	if b, _ := json.Marshal(old); strings.Contains(string(b), `"added"`) || strings.Contains(string(b), `"deleted"`) {
		t.Fatalf("a record without change types gains none: %s", b)
	}
}
