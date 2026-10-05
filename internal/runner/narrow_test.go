package runner_test

import (
	"os"
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/policy"
	"github.com/eyelock/ynf/internal/runner"
)

// TestAScopeOnlyNarrows: a sensor scope is the declared command narrowed, never anything else
// (ADR-006). The words are compared with shell quoting rules and nothing is evaluated.
func TestAScopeOnlyNarrows(t *testing.T) {
	for _, c := range []struct{ name, declared, scope string }{
		{"unchanged", "golangci-lint run ./...", "golangci-lint run ./..."},
		{"a package pattern narrowed", "golangci-lint run ./...", "golangci-lint run ./x/..."},
		{"a package, not a pattern", "golangci-lint run ./...", "golangci-lint run ./x"},
		{"a nested package", "go test -count=1 ./...", "go test -count=1 ./internal/store/..."},
		{"several packages", "go test -count=1 ./...", "go test -count=1 ./a/... ./b"},
		{"a pattern with no dot slash", "go test ./...", "go test internal/store"},
		{"dot is any relative path", "tool .", "tool cmd/greet"},
		{"dot stays dot", "tool .", "tool ."},
		{"under a pattern", "go vet ./a/...", "go vet ./a/b/..."},
		{"under a pattern, itself", "go vet ./a/...", "go vet ./a"},
		{"a dot slash directory", "lint ./dir", "lint ./dir/sub"},
		{"a directory with a slash", "lint ./dir/", "lint ./dir/sub"},
		{"a flag before the path, in order", "tool --fast ./...", "tool --fast ./x/..."},
		{"a flag with a value", "tool --out=json ./...", "tool --out=json ./x"},
		{"a quoted word the same", `tool --msg "a b" ./...`, "tool --msg 'a b' ./x"},
		{"a quoted path", "tool ./...", `tool "./x"`},
		{"no path word: append", "sh scripts/check-docs.sh", "sh scripts/check-docs.sh internal/status"},
		{"no path word: append several", "make check", "make check a b/c"},
		{"no path word: nothing added", "make check", "make check"},
		{"a file is not a path word", "go run ./cmd/tool.go", "go run ./cmd/tool.go pkg"},
	} {
		if err := runner.Narrows(c.declared, c.scope); err != nil {
			t.Errorf("%s: %q narrows %q, refused: %v", c.name, c.scope, c.declared, err)
		}
	}

	for _, c := range []struct{ name, declared, scope, why string }{
		{"a different program", "golangci-lint run ./...", "true", "the program is"},
		{"another program, same args", "golangci-lint run ./...", "other run ./x", "the program is"},
		{"an extra flag", "golangci-lint run ./...", "golangci-lint run --fast ./x/...", "flag --fast is not in the declared command"},
		{"an extra flag after the path", "golangci-lint run ./...", "golangci-lint run ./x --fast", "allows nothing but a narrower path"},
		{"a missing flag", "go test -count=1 ./...", "go test ./x", "flag -count=1 is missing"},
		{"a flag changed", "go test -count=1 ./...", "go test -count=2 ./x", "flag -count=2 is not in the declared command"},
		{"flags out of order", "tool -a -b ./...", "tool -b -a ./x", "out of order"},
		{"a missing subcommand", "golangci-lint run ./...", "golangci-lint ./x", "found"},
		{"a different subcommand", "golangci-lint run ./...", "golangci-lint linters ./x", "found"},
		{"a missing path", "golangci-lint run ./...", "golangci-lint run", "is missing"},
		{"nothing at all", "golangci-lint run ./...", "", "empty"},
		{"an environment assignment", "golangci-lint run ./...", `GOLANGCI_LINT_CACHE=/tmp/c golangci-lint run ./x`, "the program is"},
		{"a path that is not beneath", "go vet ./a/...", "go vet ./b/...", "or a path beneath it"},
		{"the root of a narrower path", "go vet ./a/...", "go vet ./...", "or a path beneath it"},
		{"a sibling with the same prefix", "lint ./dir", "lint dir2", "or a path beneath it"},
		{"a bare word is a subcommand, not a directory", "lint dir", "lint dir/sub", "found"},
		{"a parent segment", "go test ./...", "go test ./a/../b", "or a path beneath it"},
		{"a parent directory", "go test ./...", "go test ..", "or a path beneath it"},
		{"an absolute path", "go test ./...", "go test /etc", "or a path beneath it"},
		{"a leading dash path", "go test ./...", "go test -x", "flag -x is not in the declared command"},
		{"an absolute path appended", "make check", "make check /etc", "cannot be appended"},
		{"a parent path appended", "make check", "make check ../x", "cannot be appended"},
		{"a flag appended", "make check", "make check -n", "flag -n is not in the declared command"},
		{"a word appended that is not a path", "make check", "make check a=b", "cannot be appended"},
		{"extra after a narrowed path", "go test ./...", "go test ./x extra=1", "allows nothing but a narrower path"},
		{"a semicolon", "go test ./...", "go test ./x; true", `";"`},
		{"a pipe", "go test ./...", "go test ./x | cat", `"|"`},
		{"an ampersand", "go test ./...", "go test ./x && true", `"&"`},
		{"a redirect out", "go test ./...", "go test ./x > /dev/null", `">"`},
		{"a redirect in", "go test ./...", "go test ./x < /dev/null", `"<"`},
		{"a backtick", "go test ./...", "go test `true`", "`"},
		{"a command substitution", "go test ./...", "go test $(true)", `"$"`},
		{"a variable", "go test ./...", "go test $HOME", `"$"`},
		{"a variable in double quotes", "go test ./...", `go test "$HOME"`, "inside double quotes"},
		{"a glob", "go test ./...", "go test ./*", `"*"`},
		{"a comment", "go test ./...", "go test ./x #", "comment"},
		{"a home expansion", "go test ./...", "go test ~", "home expansion"},
		{"a newline", "go test ./...", "go test ./x\ntrue", "newline"},
		{"an unterminated quote", "go test ./...", "go test 'x", "unterminated"},
		{"a trailing backslash", "go test ./...", `go test ./x\`, "backslash"},
	} {
		err := runner.Narrows(c.declared, c.scope)
		if err == nil || !strings.Contains(err.Error(), c.why) {
			t.Errorf("%s: %q vs %q: %v, want it to say %q", c.name, c.scope, c.declared, err, c.why)
		}
	}
	if err := runner.Narrows("a 'b", "a"); err == nil || !strings.Contains(err.Error(), "declared command cannot be read") {
		t.Errorf("a declared command that does not parse: %v", err)
	}
	if err := runner.Narrows("", "a"); err == nil {
		t.Error("a declared command with nothing in it")
	}
}

// TestAHarnessRefusesARelaxingScope: the refusal names the sensor, what the harness declares, the
// scope and why; placeholders are checked as a safe path before there are labels, and as the
// item's own once there are.
func TestAHarnessRefusesARelaxingScope(t *testing.T) {
	h, err := runner.ParseManifest([]byte(`{"sensors":{"lint":{"source":{"command":"golangci-lint run ./..."}},"docs":{"source":{"command":"sh scripts/check-docs.sh"}},"odd":{"source":{"script":"x"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	ok := policy.Ynh{SensorScope: map[string]string{"lint": "golangci-lint run ./{label.pkg}/...", "docs": "sh scripts/check-docs.sh {label.pkg}"}}
	if err := h.CheckLane(ok); err != nil {
		t.Fatalf("a narrowing scope refused: %v", err)
	}
	if err := h.CheckScopes(ok, []string{"pkg:internal/store"}); err != nil {
		t.Fatalf("a narrowing scope refused with labels: %v", err)
	}
	relax := policy.Ynh{SensorScope: map[string]string{"lint": "golangci-lint run --fast ./x/...", "docs": "true", "odd": "true"}}
	err = h.CheckLane(relax)
	for _, want := range []string{
		`sensor_scope.lint: "golangci-lint run --fast ./x/..." is not "golangci-lint run ./..." narrowed: flag --fast is not in the declared command`,
		`sensor_scope.docs: "true" is not "sh scripts/check-docs.sh" narrowed: the program is "true", not "sh"`,
		`sensor_scope.odd: "true" cannot replace a sensor that declares no command`,
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v lacks %q", err, want)
		}
	}
	// A label that is not a path the scope may name is refused with the item's own value.
	if err := h.CheckScopes(ok, []string{"pkg:a;b"}); err == nil || !strings.Contains(err.Error(), "not a safe value") {
		t.Errorf("an unsafe label: %v", err)
	}
	// A placeholder where the declared command has none is caught from its shape alone.
	placed := policy.Ynh{SensorScope: map[string]string{"lint": "golangci-lint {label.flag} ./x"}}
	if err := h.CheckLane(placed); err == nil || !strings.Contains(err.Error(), "narrowed") {
		t.Errorf("a placeholder in a flag's place: %v", err)
	}
}

// TestTheSandboxLanesNarrowItsHarness: every sandbox lane's scopes narrow the sensors of the
// sandbox's own harness, for any package, except `relaxed`, which is wrong on purpose.
func TestTheSandboxLanesNarrowItsHarness(t *testing.T) {
	h, err := runner.ReadHarness("../../sandbox/seed")
	if err != nil {
		t.Fatal(err)
	}
	own, err := os.ReadFile("../../sandbox/seed/.agents/factory/lanes.yaml")
	if err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile("../../sandbox/factory-seed/.agents/factory/lanes.yaml")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := policy.MergeLanes(config, own)
	if err != nil {
		t.Fatal(err)
	}
	f, err := policy.Load(doc)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range f.Names() {
		y := f.Lanes[name].Run.Ynh
		if y == nil {
			continue
		}
		err := h.CheckScopes(*y, []string{"pkg:internal/store"})
		if name == "relaxed" {
			if err == nil || !strings.Contains(err.Error(), `sensor_scope.lint: "true" is not "golangci-lint run ./..." narrowed`) {
				t.Errorf("relaxed: %v", err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if err := h.CheckLane(*y); err != nil {
			t.Errorf("%s without labels: %v", name, err)
		}
	}
}
