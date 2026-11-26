package runner_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/policy"
	"github.com/eyelock/ynf/internal/runner"
)

// fakeInstallingYnh is a ynh that keeps its installs in YNH_HOME, as the real one does, and
// refuses to run without it so a test cannot reach a real home. A folder is installed as it is; a
// repository is cloned at --ref with the real git, as ynh does, into the home's harnesses.
func fakeInstallingYnh(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
[ -n "$YNH_HOME" ] || { echo "no YNH_HOME" >&2; exit 1; }
case "$1" in
install)
  mkdir -p "$YNH_HOME/harnesses"
  echo "$*" > "$YNH_HOME/install-args"
  if [ "$3" = --ref ]; then
    git clone -q "$2" "$YNH_HOME/harnesses/local--chain" 2>&1 >/dev/null && git -C "$YNH_HOME/harnesses/local--chain" -c advice.detachedHead=false checkout -q "$4" 2>&1 >/dev/null || { rm -rf "$YNH_HOME/harnesses/local--chain"; echo "Remote branch $4 not found in upstream origin" >&2; exit 1; }
    sha=$(git -C "$YNH_HOME/harnesses/local--chain" rev-parse HEAD)
    echo "{\"source\":\"$2\",\"sha\":\"$sha\"}" > "$YNH_HOME/from.json"
  else
    echo "$2" > "$YNH_HOME/harnesses/local--chain"
  fi ;;
ls)
  if [ -f "$YNH_HOME/from.json" ]; then
    sha=$(sed 's/.*"sha":"\([^"]*\)".*/\1/' "$YNH_HOME/from.json")
    printf '{"harnesses":[{"id":"local/chain","name":"chain","version_installed":"1.2.0","path":"%s","installed_from":{"sha":"%s"}}]}\n' "$YNH_HOME/harnesses/local--chain" "$sha"
  else
    printf '{"harnesses":[{"id":"local/chain","name":"chain","version_installed":"1.2.0","path":"%s"}]}\n' "$YNH_HOME/harnesses/local--chain"
  fi ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "ynh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// harnessRepo makes a bare repository holding a harness, tagged v1.2.0, and returns its file:// URL
// and the tagged commit. A branch of the same repository, moving, is called main.
func harnessRepo(t *testing.T) (url, commit string) {
	t.Helper()
	root := t.TempDir()
	src, bare := filepath.Join(root, "src"), filepath.Join(root, "chain.git")
	if err := os.MkdirAll(filepath.Join(src, ".agents", "harness"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, ".agents", "harness", "plugin.json"), []byte(`{"name":"chain","version":"1.2.0","focuses":{"tidy":{"prompt":"p"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	sh := func(dir string, args ...string) string {
		c := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		c.Dir = dir
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	sh(src, "init", "-q", "-b", "main")
	sh(src, "add", "-A")
	sh(src, "commit", "-q", "-m", "chain")
	sh(src, "tag", "v1.2.0")
	commit = sh(src, "rev-parse", "HEAD")
	sh(root, "clone", "-q", "--bare", src, bare)
	return "file://" + bare, commit
}

// TestInstallFolder: the folder is installed into the home it is given and nowhere else, and the
// id ynh reports for it is what the run uses.
func TestInstallFolder(t *testing.T) {
	fakeInstallingYnh(t)
	operator := t.TempDir()
	t.Setenv("YNH_HOME", operator)
	home := filepath.Join(t.TempDir(), "run", "ynh")
	in, err := runner.InstallHarness(context.Background(), runner.HarnessSource{Dir: "/the/checkout"}, home)
	if err != nil || in.ID != "local/chain" || in.Label() != "local/chain@1.2.0" || in.Commit != "" || in.Pin != "" {
		t.Fatalf("%+v %v", in, err)
	}
	b, err := os.ReadFile(filepath.Join(home, "harnesses", "local--chain"))
	if err != nil || strings.TrimSpace(string(b)) != "/the/checkout" {
		t.Errorf("the folder was not installed into the run's home: %q %v", b, err)
	}
	if es, _ := os.ReadDir(operator); len(es) != 0 {
		t.Errorf("the operator's ynh home was written to: %v", es)
	}
}

func TestInstallHarnessFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ynh"), []byte("#!/bin/sh\necho 'no manifest' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, err := runner.InstallHarness(context.Background(), runner.HarnessSource{Dir: "/x"}, filepath.Join(t.TempDir(), "ynh"))
	if err == nil || !strings.Contains(err.Error(), "no manifest") {
		t.Fatalf("%v", err)
	}
	if _, err := runner.InstallHarness(context.Background(), runner.HarnessSource{}, filepath.Join(t.TempDir(), "ynh")); err == nil {
		t.Error("nothing to install should be an error")
	}
}

// TestInstallPinnedHarness: a harness pinned at a tag in a local bare repository is installed into
// the run's own home, at that commit, and reports its name, version and commit. The operator's
// home is left as it was.
func TestInstallPinnedHarness(t *testing.T) {
	fakeInstallingYnh(t)
	operator := t.TempDir()
	t.Setenv("YNH_HOME", operator)
	url, commit := harnessRepo(t)
	home := filepath.Join(t.TempDir(), "run", "ynh")
	pin := policy.Pin{Repo: url, Ref: commit}
	in, err := runner.InstallHarness(context.Background(), runner.HarnessSource{Pin: &pin}, home)
	if err != nil {
		t.Fatal(err)
	}
	if in.ID != "local/chain" || in.Name != "chain" || in.Version != "1.2.0" || in.Commit != commit || in.Pin != url+"@"+commit {
		t.Errorf("%+v, want commit %s", in, commit)
	}
	h, err := runner.ReadHarness(in.Path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Focus("tidy"); err != nil {
		t.Errorf("the focus should be read from what was installed: %v", err)
	}
	if !strings.HasPrefix(in.Path, home) {
		t.Errorf("installed at %s, outside the run's home %s", in.Path, home)
	}
	if es, _ := os.ReadDir(operator); len(es) != 0 {
		t.Errorf("the operator's ynh home was written to: %v", es)
	}
}

// TestInstallPinnedHarnessAtAWrongTag: a commit the repository does not have fails the install with
// ynh's reason, and installs nothing.
func TestInstallPinnedHarnessAtAWrongTag(t *testing.T) {
	fakeInstallingYnh(t)
	url, _ := harnessRepo(t)
	home := filepath.Join(t.TempDir(), "run", "ynh")
	_, err := runner.InstallHarness(context.Background(), runner.HarnessSource{Pin: &policy.Pin{Repo: url, Ref: strings.Repeat("0", 40)}}, home)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "harnesses", "local--chain")); err == nil {
		t.Error("a failed install left a harness behind")
	}
}

// TestInstallPinnedHarnessRefusesABranch: a branch moves, so it is not a pin; the install is
// refused before ynh is asked, and names the full SHA the branch is at.
func TestInstallPinnedHarnessRefusesABranch(t *testing.T) {
	fakeInstallingYnh(t)
	url, _ := harnessRepo(t)
	_, err := runner.InstallHarness(context.Background(), runner.HarnessSource{Pin: &policy.Pin{Repo: url, Ref: "main"}}, filepath.Join(t.TempDir(), "ynh"))
	if err == nil || !strings.Contains(err.Error(), "main is a branch") {
		t.Fatalf("%v", err)
	}
}

// TestInstallPinnedHarnessForms: each documented form of a repository (URL, host/path, scp-style,
// file://) is installed, and the ref check and the install both get a form the clone tools can
// use. The addresses do not exist; the clone tools are told where each really is, by the address
// they are given, so a bare host/path that was not made a URL first would not be found.
func TestInstallPinnedHarnessForms(t *testing.T) {
	fakeInstallingYnh(t)
	t.Setenv("YNH_HOME", t.TempDir())
	file, commit := harnessRepo(t)
	for _, c := range []struct{ name, repo, cloned, pin string }{
		{"url", "https://example.test/org/chain.git", "https://example.test/org/chain.git", "https://example.test/org/chain.git@" + commit},
		{"host/path", "example.test/org/chain", "https://example.test/org/chain", "example.test/org/chain@" + commit},
		{"scp-style", "git@example.test:org/chain.git", "git@example.test:org/chain.git", "git@example.test:org/chain.git@" + commit},
		{"file", file, file, file + "@" + commit},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("GIT_CONFIG_COUNT", "1")
			t.Setenv("GIT_CONFIG_KEY_0", "url."+file+".insteadOf")
			t.Setenv("GIT_CONFIG_VALUE_0", c.cloned)
			pin := policy.Pin{Repo: c.repo, Ref: commit}
			in, err := runner.InstallHarness(context.Background(), runner.HarnessSource{Pin: &pin}, filepath.Join(t.TempDir(), "ynh"))
			if err != nil {
				t.Fatal(err)
			}
			if in.Commit != commit || in.Pin != c.pin || in.PinURL != c.cloned+"@"+commit {
				t.Errorf("%+v, want commit %s, pin %s, url %s", in, commit, c.pin, c.cloned+"@"+commit)
			}
		})
	}
}

// TestInstallPinnedHarnessRefusesATagAndNamesItsSHA: a tag is not a pin, so the install is refused
// before ynh is asked, and the refusal prints the full 40-character SHA the tag points to now.
func TestInstallPinnedHarnessRefusesATagAndNamesItsSHA(t *testing.T) {
	fakeInstallingYnh(t)
	t.Setenv("YNH_HOME", t.TempDir())
	url, commit := harnessRepo(t)
	home := filepath.Join(t.TempDir(), "ynh")
	_, err := runner.InstallHarness(context.Background(), runner.HarnessSource{Pin: &policy.Pin{Repo: url, Ref: "v1.2.0"}, Trust: true}, home)
	if err == nil || !strings.Contains(err.Error(), "v1.2.0 is a tag") || !strings.Contains(err.Error(), url+"@"+commit) {
		t.Fatalf("%v, want a refusal naming %s", err, commit)
	}
	if _, serr := os.Stat(filepath.Join(home, "install-args")); serr == nil {
		t.Error("ynh was asked to install a tag pin")
	}
}

// TestInstallPassesTrustOnlyWhenAsked: --trust goes to ynh install for a pin and for a folder when
// the caller says the ynh has the trust gate, and never otherwise.
func TestInstallPassesTrustOnlyWhenAsked(t *testing.T) {
	fakeInstallingYnh(t)
	url, commit := harnessRepo(t)
	for _, c := range []struct {
		name  string
		src   runner.HarnessSource
		trust bool
		want  string
	}{
		{"pin", runner.HarnessSource{Pin: &policy.Pin{Repo: url, Ref: commit}, Trust: true}, true, "install " + url + " --ref " + commit + " --trust"},
		{"folder", runner.HarnessSource{Dir: "/the/checkout", Trust: true}, true, "install /the/checkout --trust"},
		{"pin without", runner.HarnessSource{Pin: &policy.Pin{Repo: url, Ref: commit}}, false, "install " + url + " --ref " + commit},
		{"folder without", runner.HarnessSource{Dir: "/the/checkout"}, false, "install /the/checkout"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("YNH_HOME", t.TempDir())
			home := filepath.Join(t.TempDir(), "ynh")
			if _, err := runner.InstallHarness(context.Background(), c.src, home); err != nil {
				t.Fatal(err)
			}
			b, _ := os.ReadFile(filepath.Join(home, "install-args"))
			if got := strings.TrimSpace(string(b)); got != c.want {
				t.Errorf("ynh was run as %q, want %q", got, c.want)
			}
		})
	}
}

func TestInstallPinnedHarnessRefusesAFlag(t *testing.T) {
	for _, p := range []policy.Pin{{Repo: "--upload-pack=x", Ref: "v1"}, {Repo: "github.com/o/r", Ref: "--force"}} {
		if _, err := runner.InstallHarness(context.Background(), runner.HarnessSource{Pin: &p}, filepath.Join(t.TempDir(), "ynh")); err == nil || !strings.Contains(err.Error(), "dash") {
			t.Errorf("%v: %v", p, err)
		}
	}
}
