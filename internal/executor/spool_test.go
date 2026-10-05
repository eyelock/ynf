package executor_test

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/executor"
)

func dockerArgs(t *testing.T, j executor.Job) string {
	t.Helper()
	args, err := executor.Docker{}.Args(j, "n", "none")
	if err != nil {
		t.Fatal(err)
	}
	return " " + strings.Join(args, " ") + " "
}

// TestADockerRunMountsOnlyItsOwnSpoolFolder: the run's folder is mounted at a fixed path with
// YNR_SPOOL pointing at it, and nothing else of the spool is (ynr ADR-004). The run has no network
// path to a collector either way.
func TestADockerRunMountsOnlyItsOwnSpoolFolder(t *testing.T) {
	root := t.TempDir()
	own := filepath.Join(root, "runs", "r1")
	a := dockerArgs(t, executor.Job{Image: "img", Argv: []string{"x"}, Worktree: "/wt", RunDir: "/rd", Spool: own, Env: map[string]string{"YNR_SPOOL": "/somewhere/else"}})
	if !strings.Contains(a, " -v "+own+":"+executor.SpoolDir+" ") {
		t.Errorf("its own folder is not mounted at %s: %s", executor.SpoolDir, a)
	}
	if !strings.Contains(a, " -e YNR_SPOOL="+executor.SpoolDir+" ") || strings.Contains(a, "/somewhere/else") {
		t.Errorf("YNR_SPOOL is not the mounted folder: %s", a)
	}
	if n := strings.Count(a, " -v "); n != 3 {
		t.Errorf("%d mounts, want the worktree, the run folder and its spool folder only: %s", n, a)
	}
	for _, leak := range []string{root + ":", root + "/manifests", root + "/factory", "/runs:"} {
		if strings.Contains(a, leak) {
			t.Errorf("the spool root, manifests, factory or another run is mounted (%s): %s", leak, a)
		}
	}
	if !strings.Contains(a, "--network none") {
		t.Errorf("a run with a spool still needs no network: %s", a)
	}

	// Without a spool the command line is what it was.
	b := dockerArgs(t, executor.Job{Image: "img", Argv: []string{"x"}, Worktree: "/wt", RunDir: "/rd"})
	if strings.Contains(b, "YNR_SPOOL") || strings.Count(b, " -v ") != 2 {
		t.Errorf("a run with no spool: %s", b)
	}
}

// TestTheCollectorOnStartsRunsWithoutOTLP: with the collector on, nothing of the operator's
// OTEL_EXPORTER_OTLP_* reaches a run, however it is passed.
func TestTheCollectorOnStartsRunsWithoutOTLP(t *testing.T) {
	j := executor.Job{Image: "img", Argv: []string{"x"}, Worktree: "/wt", RunDir: "/rd", Spool: "/s/runs/r1", NoOTLP: true,
		Env:     map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://op:4318", "OTEL_RESOURCE_ATTRIBUTES": "a=b", "TRACEPARENT": "00-x"},
		Secrets: map[string]string{"OTEL_EXPORTER_OTLP_HEADERS": "k=v", "ANTHROPIC_API_KEY": "k"}}
	a := dockerArgs(t, j)
	if strings.Contains(a, "OTEL_EXPORTER_OTLP_") {
		t.Errorf("a container got the operator's endpoint: %s", a)
	}
	for _, keep := range []string{"-e OTEL_RESOURCE_ATTRIBUTES=a=b", "-e TRACEPARENT=00-x", "-e ANTHROPIC_API_KEY"} {
		if !strings.Contains(a, keep) {
			t.Errorf("lost %s: %s", keep, a)
		}
	}
	// Left on, they pass as they did.
	j.NoOTLP = false
	if a := dockerArgs(t, j); !strings.Contains(a, "OTEL_EXPORTER_OTLP_ENDPOINT=http://op:4318") {
		t.Errorf("collector off: %s", a)
	}
}

func TestAProcessRunGetsItsSpoolAndNotTheOperatorsOTLP(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://op:4318")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_HEADERS", "a=b")
	t.Setenv("YNR_SPOOL", "/the/hosts/own")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "keep=me")
	dir := t.TempDir()
	run := func(noOTLP bool) string {
		out, err := executor.Process{}.Run(context.Background(), executor.Job{
			Argv: []string{"sh", "-c", `env | grep -E '^(OTEL|YNR)' | sort`}, Worktree: dir, RunDir: dir, Spool: "/s/runs/r1", NoOTLP: noOTLP,
			Env: map[string]string{}, Secrets: map[string]string{"OTEL_EXPORTER_OTLP_PROTOCOL": "grpc"}})
		if err != nil {
			t.Fatal(err)
		}
		return string(out.Stdout)
	}
	got := run(true)
	if strings.Contains(got, "OTEL_EXPORTER_OTLP_") || !strings.Contains(got, "YNR_SPOOL=/s/runs/r1") || strings.Contains(got, "/the/hosts/own") || !strings.Contains(got, "OTEL_RESOURCE_ATTRIBUTES=keep=me") {
		t.Errorf("collector on: %s", got)
	}
	got = run(false)
	if !strings.Contains(got, "OTEL_EXPORTER_OTLP_ENDPOINT=http://op:4318") || !strings.Contains(got, "YNR_SPOOL=/s/runs/r1") {
		t.Errorf("collector off: %s", got)
	}
}

// TestAnInlineRunOwnsItsSpoolFolderAndNothingElseOfTheSpool: the run user is handed its own
// folder for the run, and takes nothing else of the spool.
func TestAnInlineRunOwnsItsSpoolFolderAndNothingElseOfTheSpool(t *testing.T) {
	nobody, err := user.Lookup("nobody")
	if err != nil {
		t.Skip("no nobody user here")
	}
	dir := t.TempDir()
	wt, rd := filepath.Join(dir, "wt"), filepath.Join(dir, "run")
	own, other := filepath.Join(dir, "spool", "runs", "r1"), filepath.Join(dir, "spool", "runs", "r2")
	for _, d := range []string{wt, rd, own, other, filepath.Join(dir, "spool", "manifests")} {
		_ = os.MkdirAll(d, 0o755)
	}
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://op:4318")
	var handed []string
	in := executor.Inline{
		User:       "nobody",
		Chown:      func(p string, uid, gid int) error { handed = append(handed, p+"="+itoa(uid)); return nil },
		Credential: func(*exec.Cmd, uint32, uint32) {},
	}
	out, err := in.Run(context.Background(), executor.Job{Argv: []string{"sh", "-c", "env"}, Worktree: wt, RunDir: rd, Spool: own, NoOTLP: true,
		Env: map[string]string{}})
	if err != nil || out.Exit != 0 {
		t.Fatalf("%v %+v", err, out)
	}
	env := string(out.Stdout)
	if !strings.Contains(env, "YNR_SPOOL="+own+"\n") || strings.Contains(env, "OTEL_EXPORTER_OTLP_") {
		t.Errorf("the run's environment: %s", env)
	}
	all := strings.Join(handed, " ")
	if !strings.Contains(all, own+"="+nobody.Uid) || !strings.Contains(all, own+"="+itoa(os.Getuid())) {
		t.Errorf("its own folder is not handed over and back: %s", all)
	}
	for _, not := range []string{other, filepath.Join(dir, "spool", "manifests"), filepath.Join(dir, "spool") + "="} {
		if strings.Contains(all, not) {
			t.Errorf("%s was handed to the run user: %s", not, all)
		}
	}
}
