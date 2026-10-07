// Command fakeforge is the offline forge ynr conformance runs ynf against (.ynr/conformance.yaml).
//
//	fakeforge [flags] -- command [args...]
//
// It serves enough of GitHub's API for one sweep, from a lane that is switched off, so ynf tracks
// the one issue and ignores it: no git, no docker, no model. It writes a config in a temporary
// directory of its own, runs the command with YNF_GITHUB_API, GITHUB_TOKEN and YNF_CONFIG pointing
// at it, and exits with the command's exit code.
//
// The issue carries the planted ticket text and the token is the planted secret, so conformance
// can check neither reaches a record. When YNR_STUB_TURN_DELAY is set, as in conformance's kill
// run, the issue takes that long to arrive, so ynf is still at work when it is killed.
package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const lanes = `version: 1
lanes:
  fmt:
    kind: originate
    enabled: false
    intake: [{github.search: "label:ynf:fmt", every: 5m}]
    run: {runner: command, command: {argv: ["true"]}}
    when: {converged: open_pr}
`

func main() {
	os.Exit(run())
}

func run() int {
	fs := flag.NewFlagSet("fakeforge", flag.ContinueOnError)
	title := fs.String("ticket-title", "a ticket", "the issue's title")
	body := fs.String("ticket-body", "", "the issue's body")
	token := fs.String("token", "token", "GITHUB_TOKEN for the command")
	failIssue := fs.Bool("fail-issue", false, "answer the issue with a server error")
	if err := fs.Parse(os.Args[1:]); err != nil || fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: fakeforge [--ticket-title t] [--ticket-body b] [--token t] -- command [args...]")
		return 2
	}
	delay, _ := time.ParseDuration(os.Getenv("YNR_STUB_TURN_DELAY"))

	srv := httptest.NewServer(handler(*title, *body, delay, *failIssue))
	defer srv.Close()
	dir, err := os.MkdirTemp("", "fakeforge-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	defer func() { _ = os.RemoveAll(dir) }()
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("version: 1\nrepos: [o/r]\n"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	cmd := exec.Command(fs.Arg(0), fs.Args()[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), "YNF_GITHUB_API="+srv.URL, "GITHUB_TOKEN="+*token, "YNF_CONFIG="+cfg)
	// Pass a termination on to the command, so a kill never leaves ynf running.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 127
	}
	go func() {
		for s := range sig {
			_ = cmd.Process.Signal(s)
		}
	}()
	err = cmd.Wait()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		return ee.ExitCode()
	}
	return 1
}

func handler(title, body string, delay time.Duration, failIssue bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		file := func(s string) {
			reply(map[string]any{"type": "file", "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(s))})
		}
		base := "http://" + r.Host + "/"
		switch r.URL.Path {
		case "/repos/o/r":
			reply(map[string]any{"default_branch": "main"})
		case "/repos/o/r/branches/main":
			reply(map[string]any{"name": "main", "commit": map[string]any{"sha": "c0ffee"}})
		case "/repos/o/r/contents/.agents/factory/factory.yaml":
			file("version: 1\nrepos: [o/r]\n")
		case "/repos/o/r/contents/.agents/factory/lanes.yaml":
			file(lanes)
		case "/search/issues":
			if strings.Contains(r.URL.Query().Get("q"), "ynf:fmt") {
				reply(map[string]any{"items": []any{map[string]any{"number": 5, "repository_url": base + "repos/o/r"}}})
				return
			}
			reply(map[string]any{"items": []any{}})
		case "/repos/o/r/issues/5":
			time.Sleep(delay)
			if failIssue {
				w.WriteHeader(http.StatusInternalServerError)
				reply(map[string]any{"message": "Server Error"})
				return
			}
			reply(map[string]any{"number": 5, "state": "open", "title": title, "body": body,
				"labels": []any{map[string]any{"name": "ynf:fmt"}}})
		default:
			w.WriteHeader(http.StatusNotFound)
			reply(map[string]any{"message": "Not Found"})
		}
	})
}
