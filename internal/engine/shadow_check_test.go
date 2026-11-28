package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ynf #257: shadowCheck reads ynh check's JSON and the baseline's, and keeps an error rather than
// a clean result when ynh cannot be read.
func TestShadowCheckReadsYnh(t *testing.T) {
	old := ynhOutput
	t.Cleanup(func() { ynhOutput = old })
	var calls []string
	ynhOutput = func(_ context.Context, dir string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		if args[0] == "check" {
			return []byte(`{"verdict":"blocked","sensors":[{"name":"vet","status":"fail","new_count":2,"known_count":1}],"baseline":{"known":1,"fixed":0}}`), nil
		}
		return []byte(`{"sensors":[{"name":"vet","findings":["x.go:1: old"]}]}`), nil
	}
	c := shadowCheck(context.Background(), t.TempDir(), "acme@1")
	if c.Error != "" || c.Verdict != "blocked" || c.NewFindings() != 2 || c.ForgivenFindings() != 1 {
		t.Fatalf("%+v", c)
	}
	if len(calls) != 2 || calls[0] != "check acme@1 --format json" || calls[1] != "baseline acme@1 --explain --format json" {
		t.Errorf("calls %q", calls)
	}

	ynhOutput = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("no ynh") }
	if c := shadowCheck(context.Background(), t.TempDir(), "acme@1"); c.Error == "" || c.Verdict != "" {
		t.Errorf("an unreadable check must be an error, not a pass: %+v", c)
	}
}

// A folder lane: ynh baseline takes an installed id and not a folder, so its explain is not asked
// for, the check is still read, and the grader is told why no forgiven findings are shown.
func TestShadowCheckFolderLane(t *testing.T) {
	old := ynhOutput
	t.Cleanup(func() { ynhOutput = old })
	wt := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wt, "harness"), 0o755); err != nil {
		t.Fatal(err)
	}
	var calls []string
	ynhOutput = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		return []byte(`{"verdict":"pass","sensors":[],"baseline":{"known":3,"fixed":0}}`), nil
	}
	c := shadowCheck(context.Background(), wt, "harness")
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "check "+filepath.Join(wt, "harness")) {
		t.Fatalf("a folder lane runs ynh check on the folder and no baseline: %q", calls)
	}
	if c.Error != "" || c.Verdict != "pass" || c.Known != 3 || !strings.Contains(c.ExplainError, "folder") {
		t.Errorf("%+v", c)
	}

	// An installed harness whose explain fails keeps the check and says so apart from it.
	ynhOutput = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "baseline" {
			return nil, errors.New("exit status 1")
		}
		return []byte(`{"verdict":"pass","sensors":[]}`), nil
	}
	c = shadowCheck(context.Background(), wt, "acme@1")
	if c.Error != "" || c.Verdict != "pass" || !strings.Contains(c.ExplainError, "ynh baseline --explain") {
		t.Errorf("%+v", c)
	}
}
