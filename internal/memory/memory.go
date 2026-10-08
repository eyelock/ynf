// Package memory is ynf's memory port (ADR-008, ADR-012): ynm when it is installed, nothing when
// it is not. ynf writes what only it can see, outcomes and failures recurring across runs; it
// never decides anything with memory, and never puts memory into an agent's task: a harness that
// wants memory connects its agent to ynm itself.
package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/eyelock/ynf/internal/telemetry"
)

// Record is one memory ynf writes.
type Record struct {
	Type       string // episodic
	Namespace  string // factory/<host>/<owner>/<repo>
	Level      string // personal or distributed; empty is ynm's default, personal
	Subject    string // the item key, or a failure signature
	Summary    string // one line
	Content    string // markdown
	Tags       []string
	DataSchema string // ynf.step.v1, ynf.failure.v1: ynf owns these schemas
	Data       map[string]any
	Source     string // the step that wrote it
}

// Memory is the port.
type Memory interface {
	Remember(ctx context.Context, r Record) error
}

// Ynm talks to ynm through its CLI, which works against whatever store ynm is configured for.
type Ynm struct {
	Bin string // default "ynm"
	Cwd string // the directory ynm runs as if from; default the process's
}

func (y Ynm) bin() string {
	if y.Bin == "" {
		return "ynm"
	}
	return y.Bin
}

// Remember implements Memory.
func (y Ynm) Remember(ctx context.Context, r Record) error {
	args := []string{"remember", "--json", "--type", r.Type, "--content", r.Content, "--namespace", r.Namespace}
	if r.Level != "" {
		args = append(args, "--level", r.Level)
	}
	if r.Subject != "" {
		args = append(args, "--subject", r.Subject)
	}
	if r.Summary != "" {
		args = append(args, "--summary", r.Summary)
	}
	if len(r.Tags) > 0 {
		args = append(args, "--tags", strings.Join(r.Tags, ","))
	}
	if r.Data != nil {
		b, err := json.Marshal(r.Data)
		if err != nil {
			return err
		}
		args = append(args, "--data", string(b))
	}
	if r.DataSchema != "" {
		args = append(args, "--data-schema", r.DataSchema)
	}
	if r.Source != "" {
		args = append(args, "--source", r.Source)
	}
	_, err := y.run(ctx, args...)
	return err
}

func (y Ynm) run(ctx context.Context, args ...string) (string, error) {
	if y.Cwd != "" {
		args = append(args, "--cwd", y.Cwd)
	}
	c := exec.CommandContext(ctx, y.bin(), args...)
	telemetry.Command(ctx, c) // ynm joins the trace through TRACEPARENT
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("ynm %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// Detect returns ynm when it is on PATH and a store is present, or nil and the reason it is not
// (ADR-012: detected, never required). It looks where ynm itself writes: a repository's own
// `.ynm/` (at the root of the git repository cwd is in, which for a linked worktree is the main
// repository's too; cwd is the directory ynm runs from, the working directory when empty), then
// the user's store, which is $YNM_HOME when that is set and only then ~/.ynm. Nothing is found by
// walking up past the repository root, so ~/.ynm is never taken for a project's store.
func Detect(cwd string) (Memory, string) {
	if _, err := exec.LookPath("ynm"); err != nil {
		return nil, "ynm is not on PATH"
	}
	if where, why := StoreAt(cwd); where != "" {
		return Ynm{Cwd: cwd}, "ynm on PATH, " + why + " at " + where
	}
	return nil, "ynm is on PATH but has no store: no .ynm/ at the root of " + describeRepo(cwd) + ", and no user store at " + userStore() + " (ynm init sets one up)"
}

func dirOrWd(cwd string) string {
	if cwd != "" {
		return cwd
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// userStore is where ynm keeps the user's own store: $YNM_HOME when set, else ~/.ynm.
func userStore() string {
	if h := os.Getenv("YNM_HOME"); h != "" {
		return h
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".ynm")
	}
	return "~/.ynm"
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// repoRoots returns the git repository root that dir is in, and the main repository's root when
// dir is in a linked worktree (a `.git` file naming `<main>/.git/worktrees/<name>`); both are
// empty outside a repository.
func repoRoots(dir string) (root, main string) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", ""
	}
	for {
		fi, err := os.Stat(filepath.Join(dir, ".git"))
		if err == nil && fi.IsDir() {
			return dir, ""
		}
		if err == nil {
			b, _ := os.ReadFile(filepath.Join(dir, ".git"))
			if g, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:"); ok {
				g = strings.TrimSpace(g)
				if !filepath.IsAbs(g) {
					g = filepath.Join(dir, g)
				}
				if wt := filepath.Dir(g); filepath.Base(wt) == "worktrees" {
					return dir, filepath.Dir(filepath.Dir(wt))
				}
			}
			return dir, ""
		}
		up := filepath.Dir(dir)
		if up == dir {
			return "", ""
		}
		dir = up
	}
}

func describeRepo(cwd string) string {
	if root, _ := repoRoots(dirOrWd(cwd)); root != "" {
		return "the repository " + root
	}
	return dirOrWd(cwd) + ", which is not in a git repository"
}

// StoreAt names the ynm store ynf finds and why it is the one, or "" when there is none: the
// repository's own `.ynm/`, else the user's store.
func StoreAt(cwd string) (path, why string) {
	root, main := repoRoots(dirOrWd(cwd))
	for _, r := range []string{root, main} {
		if r != "" && isDir(filepath.Join(r, ".ynm")) {
			return filepath.Join(r, ".ynm"), "the project store"
		}
	}
	if u := userStore(); isDir(u) {
		if os.Getenv("YNM_HOME") != "" {
			return u, "the user store, from YNM_HOME"
		}
		return u, "the user store, ~/.ynm"
	}
	return "", ""
}

// YnmHTTP talks to a hosted ynm over its MCP HTTP endpoint (ADR-008): the shared store for a pool
// of workers or CI, where many writers go through one server. It authenticates with a bearer token,
// such as a machine token from the identity provider's client-credentials grant, whose subject is
// the writer in ynm's audit log.
type YnmHTTP struct {
	Endpoint string
	Token    string
	// Transport overrides the connection, for tests.
	Transport func() mcp.Transport

	mu      sync.Mutex
	session *mcp.ClientSession
}

// Remember implements Memory, through ynm's memory_remember tool.
func (y *YnmHTTP) Remember(ctx context.Context, r Record) error {
	args := map[string]any{"type": r.Type, "content": r.Content, "namespace": r.Namespace}
	for k, v := range map[string]string{"level": r.Level, "subject": r.Subject, "summary": r.Summary, "dataSchema": r.DataSchema, "source": r.Source} {
		if v != "" {
			args[k] = v
		}
	}
	if len(r.Tags) > 0 {
		args["tags"] = r.Tags
	}
	if r.Data != nil {
		args["data"] = r.Data
	}
	for attempt := 0; ; attempt++ {
		s, err := y.connect(ctx)
		if err != nil {
			return fmt.Errorf("ynm at %s: %w", y.Endpoint, err)
		}
		res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: "memory_remember", Arguments: args})
		if err != nil {
			y.Close()
			if attempt == 0 && ctx.Err() == nil {
				continue // a session that went away: once more, fresh
			}
			return fmt.Errorf("ynm memory_remember: %w", err)
		}
		if res.IsError {
			var msg []string
			for _, c := range res.Content {
				if t, ok := c.(*mcp.TextContent); ok {
					msg = append(msg, t.Text)
				}
			}
			return fmt.Errorf("ynm memory_remember: %s", strings.Join(msg, " "))
		}
		return nil
	}
}

func (y *YnmHTTP) connect(ctx context.Context) (*mcp.ClientSession, error) {
	y.mu.Lock()
	defer y.mu.Unlock()
	if y.session != nil {
		return y.session, nil
	}
	var t mcp.Transport
	if y.Transport != nil {
		t = y.Transport()
	} else {
		hc := &http.Client{Transport: telemetry.Transport(bearer{y.Token, http.DefaultTransport})}
		t = &mcp.StreamableClientTransport{Endpoint: y.Endpoint, HTTPClient: hc}
	}
	s, err := mcp.NewClient(&mcp.Implementation{Name: "ynf", Version: "1"}, nil).Connect(ctx, t, nil)
	if err != nil {
		return nil, err
	}
	y.session = s
	return s, nil
}

// Close ends the session.
func (y *YnmHTTP) Close() {
	y.mu.Lock()
	defer y.mu.Unlock()
	if y.session != nil {
		_ = y.session.Close()
		y.session = nil
	}
}

type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return b.next.RoundTrip(r)
}
