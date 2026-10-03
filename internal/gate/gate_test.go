package gate_test

import (
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/gate"
)

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
