package gate_test

import (
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/gate"
)

func TestAppendOnly(t *testing.T) {
	appendOnly := []string{"**/*_test.go"}
	cases := []struct {
		name    string
		changed []string
		kinds   gate.Kinds
		allowed []string
		prot    []string
		refuses string // "" = passes
	}{
		{"a new file is added", []string{"a/x_test.go"}, gate.Kinds{Added: []string{"a/x_test.go"}}, nil, nil, ""},
		{"a modified file is refused", []string{"a/x_test.go"}, gate.Kinds{}, nil, nil, "diff gate refused: a/x_test.go (append-only: modified)"},
		{"a deleted file is refused", []string{"a/x_test.go"}, gate.Kinds{Deleted: []string{"a/x_test.go"}}, nil, nil, "diff gate refused: a/x_test.go (append-only: deleted)"},
		{"a type change reads as modified", []string{"a/x_test.go"}, gate.Kinds{}, nil, nil, "(append-only: modified)"},
		{"a rename is a deletion and an addition", []string{"a/x_test.go", "a/y_test.go"},
			gate.Kinds{Added: []string{"a/y_test.go"}, Deleted: []string{"a/x_test.go"}}, nil, nil, "diff gate refused: a/x_test.go (append-only: deleted)"},
		{"a file outside the glob is free", []string{"a/x.go"}, gate.Kinds{}, nil, nil, ""},
		{"names every refusal", []string{"a/x_test.go", "a/y_test.go", "a/z_test.go"}, gate.Kinds{Deleted: []string{"a/y_test.go"}, Added: []string{"a/z_test.go"}}, nil, nil,
			"diff gate refused: a/x_test.go (append-only: modified), a/y_test.go (append-only: deleted)"},
		{"built-in protected paths win", []string{".github/workflows/x_test.go"}, gate.Kinds{Added: []string{".github/workflows/x_test.go"}}, nil, nil, "(protected)"},
		{"protected_paths win over an addition", []string{"a/x_test.go"}, gate.Kinds{Added: []string{"a/x_test.go"}}, nil, []string{"a/**"}, "a/x_test.go (protected)"},
		{"the allowed list still applies to an addition", []string{"a/x_test.go"}, gate.Kinds{Added: []string{"a/x_test.go"}}, []string{"b/**"}, nil, "(outside the lane's allowed paths)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := gate.CheckKinds(c.changed, c.kinds, c.allowed, c.prot, appendOnly)
			switch {
			case c.refuses == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case c.refuses != "" && (err == nil || !strings.Contains(err.Error(), c.refuses)):
				t.Fatalf("got %v, want a refusal mentioning %q", err, c.refuses)
			}
		})
	}
}

func TestTouches(t *testing.T) {
	got := gate.Touches([]string{"a.go", "db/m.sql", "docs/x.md", "db/n.sql"}, []string{"db/**", "docs/*.md"})
	if len(got) != 3 || got[0] != "db/m.sql" || got[2] != "db/n.sql" {
		t.Fatal(got)
	}
	if gate.Touches([]string{"a.go"}, nil) != nil {
		t.Fatal("no globs touch nothing")
	}
}

func TestGate(t *testing.T) {
	cases := []struct {
		name      string
		changed   []string
		allowed   []string
		protected []string
		refuses   string // "" = passes
	}{
		{"inside allowed", []string{"internal/format/format.go"}, []string{"**/*.go"}, nil, ""},
		{"no allow list allows all", []string{"README.md"}, nil, nil, ""},
		{"outside allowed", []string{"README.md"}, []string{"internal/**"}, nil, "README.md (outside"},
		{"ci workflow always refused", []string{".github/workflows/ci.yml"}, nil, nil, "ci.yml (protected)"},
		{"lane policy always refused", []string{".agents/factory/lanes.yaml"}, []string{"**"}, nil, "lanes.yaml (protected)"},
		{"harness manifest refused", []string{".agents/harness/plugin.json"}, nil, nil, "plugin.json (protected)"},
		{"nested codeowners refused", []string{"docs/CODEOWNERS"}, nil, nil, "CODEOWNERS (protected)"},
		{"lane protected path", []string{".golangci.yml"}, nil, []string{".golangci*.yml"}, ".golangci.yml (protected)"},
		{"names every refusal", []string{"a.go", ".ynh/baseline/x.json", "README.md"}, []string{"*.go"}, nil, "baseline/x.json (protected), README.md (outside"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := gate.Check(c.changed, c.allowed, c.protected)
			switch {
			case c.refuses == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case c.refuses != "" && (err == nil || !strings.Contains(err.Error(), c.refuses)):
				t.Fatalf("got %v, want a refusal mentioning %q", err, c.refuses)
			}
		})
	}
}
