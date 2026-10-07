package runner_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/runner"
)

// fakeInstallingYnh is a ynh that keeps its installs in YNH_HOME, as the real one does, and
// refuses to run without it so a test cannot reach a real home.
func fakeInstallingYnh(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
[ -n "$YNH_HOME" ] || { echo "no YNH_HOME" >&2; exit 1; }
case "$1" in
install) mkdir -p "$YNH_HOME/installed" && echo "$2" > "$YNH_HOME/installed/local--chain" ;;
ls) printf '{"harnesses":[{"id":"local/chain","name":"chain"}]}\n' ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "ynh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestInstallFolder: the folder is installed into the home it is given and nowhere else, and the
// id ynh reports for it is what the run uses.
func TestInstallFolder(t *testing.T) {
	fakeInstallingYnh(t)
	operator := t.TempDir()
	t.Setenv("YNH_HOME", operator)
	home := filepath.Join(t.TempDir(), "run", "ynh")
	id, err := runner.InstallFolder(context.Background(), "/the/checkout", home)
	if err != nil || id != "local/chain" {
		t.Fatalf("%q %v", id, err)
	}
	b, err := os.ReadFile(filepath.Join(home, "installed", "local--chain"))
	if err != nil || strings.TrimSpace(string(b)) != "/the/checkout" {
		t.Errorf("the folder was not installed into the run's home: %q %v", b, err)
	}
	if es, _ := os.ReadDir(operator); len(es) != 0 {
		t.Errorf("the operator's ynh home was written to: %v", es)
	}
}

func TestInstallFolderFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ynh"), []byte("#!/bin/sh\necho 'no manifest' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, err := runner.InstallFolder(context.Background(), "/x", filepath.Join(t.TempDir(), "ynh"))
	if err == nil || !strings.Contains(err.Error(), "no manifest") {
		t.Fatalf("%v", err)
	}
}
