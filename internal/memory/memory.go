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
	"os/exec"
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

// Detect returns ynm when it is on PATH, or nil (ADR-012: detected, never required).
func Detect() Memory {
	if _, err := exec.LookPath("ynm"); err != nil {
		return nil
	}
	return Ynm{}
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
