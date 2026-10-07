package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/policy"
)

// fakeTools puts stand-in ynh and docker first on PATH: docker knows no images, and ynh records
// how it was asked to build one.
func fakeTools(t *testing.T, ynhExit int) string {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	_ = os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\necho \"docker $*\" >> "+calls+"\nexit 1\n"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "ynh"), []byte("#!/bin/sh\necho \"ynh $*\" >> "+calls+"\necho building\nexit "+string(rune('0'+ynhExit))+"\n"), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

func repoWithHarness(t *testing.T) string {
	t.Helper()
	wt := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wt, ".agents/harness"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(wt, ".agents/harness/plugin.json"), []byte(`{"name":"x"}`), 0o644)
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "x"}} {
		c := exec.Command("git", args...)
		c.Dir = wt
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	return wt
}

func TestBuildHarnessImage(t *testing.T) {
	wt := repoWithHarness(t)
	calls := fakeTools(t, 0)
	if imageBuilder(true) == nil {
		t.Fatal("ynh is on PATH; the builder should be available")
	}
	tag, err := buildHarnessImage(context.Background(), wt, policy.Ynh{Harness: ".", Base: "my-base:1"})
	if err != nil || !strings.HasPrefix(tag, "ynf-harness:") {
		t.Fatalf("%q %v", tag, err)
	}
	b, _ := os.ReadFile(calls)
	if !strings.Contains(string(b), "ynh image ynf-harness --from "+wt+" --entrypoint agent --tag "+tag+" --base my-base:1") {
		t.Fatalf("calls: %s", b)
	}
	again, _ := buildHarnessImage(context.Background(), wt, policy.Ynh{Harness: ".", Base: "my-base:1"})
	if again != tag {
		t.Fatal("the same commit, harness and base should give the same tag")
	}
	if other, _ := buildHarnessImage(context.Background(), wt, policy.Ynh{Harness: "."}); other == tag {
		t.Fatal("a different base should give a different tag")
	}
}

func TestBuildHarnessImageErrors(t *testing.T) {
	wt := repoWithHarness(t)
	fakeTools(t, 3)
	if _, err := buildHarnessImage(context.Background(), wt, policy.Ynh{Harness: "."}); err == nil || !strings.Contains(err.Error(), "building") {
		t.Fatalf("ynh failure: %v", err)
	}
	if _, err := buildHarnessImage(context.Background(), wt, policy.Ynh{Harness: "eyelock/ynh-lint@1.4"}); err == nil || !strings.Contains(err.Error(), "run.image") {
		t.Fatalf("an installed id: %v", err)
	}
	t.Setenv("PATH", t.TempDir())
	if imageBuilder(true) != nil {
		t.Fatal("no ynh on PATH: no builder")
	}
}

// TestTheBuildTagFollowsTheHarness: a harness in its own folder is built again only when that
// folder changes, not on every commit; a base change is a new image too.
func TestTheBuildTagFollowsTheHarness(t *testing.T) {
	wt := repoWithHarness(t)
	git := func(args ...string) {
		c := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		c.Dir = wt
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	_ = os.MkdirAll(filepath.Join(wt, "h/.agents/harness"), 0o755)
	_ = os.WriteFile(filepath.Join(wt, "h/.agents/harness/plugin.json"), []byte(`{"name":"h"}`), 0o644)
	git("add", "-A")
	git("commit", "-q", "-m", "a harness in its own folder")
	fakeTools(t, 0)
	cfg := policy.Ynh{Harness: "h", Base: "b:1"}
	first, err := buildHarnessImage(context.Background(), wt, cfg)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(wt, "code.go"), []byte("package x\n"), 0o644)
	git("add", "-A")
	git("commit", "-q", "-m", "code, not the harness")
	if again, _ := buildHarnessImage(context.Background(), wt, cfg); again != first {
		t.Fatal("a commit outside the harness changed its tag")
	}
	_ = os.WriteFile(filepath.Join(wt, "h/.agents/harness/plugin.json"), []byte(`{"name":"h","description":"changed"}`), 0o644)
	git("add", "-A")
	git("commit", "-q", "-m", "the harness")
	if changed, _ := buildHarnessImage(context.Background(), wt, cfg); changed == first {
		t.Fatal("a harness change kept its tag")
	}
	if _, err := buildHarnessImage(context.Background(), wt, policy.Ynh{Harness: "nope"}); err == nil {
		t.Fatal("a folder that is not a harness built")
	}
	if imageBuilder(false) != nil {
		t.Fatal("images.build: false still builds")
	}
}

func TestPickHarness(t *testing.T) {
	one := []listed{{ID: "local/ynf-sandbox", Name: "ynf-sandbox"}}
	two := append(one, listed{ID: "github.com/example-org/lint", Name: "lint"})
	for _, c := range []struct {
		hs   []listed
		want string
		id   string
	}{
		{one, "", "local/ynf-sandbox"}, {one, ".", "local/ynf-sandbox"}, {one, "harnesses/lint", ""}, {one, "ynf-sandbox", "local/ynf-sandbox"},
		{two, "lint", "github.com/example-org/lint"}, {two, "local/ynf-sandbox", "local/ynf-sandbox"},
		{two, "", ""}, {two, "nope", ""}, {one, "eyelock/ynh-lint@1.4", ""}, {nil, "", ""},
	} {
		id, err := pickHarness(c.hs, c.want)
		if id != c.id || (c.id == "") != (err != nil) {
			t.Errorf("%d harnesses, %q: %q %v", len(c.hs), c.want, id, err)
		}
	}
}

// TestImageHarness: the harness is read from the image's own ynh, once per image.
func TestImageHarness(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	script := `#!/bin/sh
echo "$*" >> ` + calls + `
case "$*" in
  *broken*" ls "*) echo nope ;;
  *" ls --format json") echo '{"capabilities":"0.9.0","harnesses":[{"id":"local/h","name":"h"}]}' ;;
  *" info local/h --format json") echo '{"harness":{"manifest":{"env_passthrough":["K"],"focuses":{"tidy":{"prompt":"P"}},"agent":{"max_turns":5}}}}' ;;
  *) echo "unexpected: $*" >&2; exit 1 ;;
esac
`
	_ = os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	h, err := imageHarness(context.Background(), "img:1", ".")
	if err != nil || h.Agent.MaxTurns != 5 || h.Focuses["tidy"].Prompt != "P" || len(h.EnvPassthrough) != 1 {
		t.Fatalf("%+v %v", h, err)
	}
	if _, err := imageHarness(context.Background(), "img:1", "."); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(calls); strings.Count(string(b), " ls ") != 1 {
		t.Fatalf("the image was asked again:\n%s", b)
	}
	if _, err := imageHarness(context.Background(), "img:1", "other"); err == nil {
		t.Fatal("a harness the image does not carry")
	}
	if _, err := imageHarness(context.Background(), "broken:1", "."); err == nil {
		t.Fatal("unreadable ynh ls output")
	}
}

// TestImageHarnessInARealImage reads the harness from a real agent image, so ynf's reading of
// ynh's own output is checked against ynh itself. Set YNF_HARNESS_IMAGE to one built with
// `ynh image <harness> --entrypoint agent`, such as the sandbox's.
func TestImageHarnessInARealImage(t *testing.T) {
	img := os.Getenv("YNF_HARNESS_IMAGE")
	if img == "" {
		t.Skip("set YNF_HARNESS_IMAGE to an agent image")
	}
	h, err := imageHarness(context.Background(), img, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Focuses) == 0 || len(h.EnvPassthrough) == 0 {
		t.Fatalf("%s: no focuses or passthrough read: %+v", img, h)
	}
	t.Logf("%s: focuses %d, passthrough %v, agent %+v, sensors %d", img, len(h.Focuses), h.EnvPassthrough, h.Agent, len(h.Sensors))
}
