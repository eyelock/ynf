// Package mcptracker is the mcp tracker provider (ADR-003): a tracker whose system has an MCP
// server, such as JIRA Cloud or Data Center, read and written by calling the server's tools
// directly. No model is involved, so what it reads is as deterministic as a REST call, and it
// calls with ynf's own credentials, never the agent's.
package mcptracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"sync"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"github.com/eyelock/ynf/internal/facts"
	"github.com/eyelock/ynf/internal/tracker"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Config is an mcp tracker as factory.yaml declares it.
type Config struct {
	Provider string `json:"provider"`
	// Site is the tracker's web address; its host names the tracker in item keys.
	Site   string `json:"site"`
	Server struct {
		// Command starts a server that speaks MCP on stdin and stdout; it receives only the
		// variables named in Env, and PATH and HOME.
		Command []string `json:"command"`
		Env     []string `json:"env"`
		// URL is a server reached over HTTP; TokenEnv names the variable holding its bearer token.
		URL      string `json:"url"`
		TokenEnv string `json:"token_env"`
	} `json:"server"`
	Get     Call `json:"get"`
	Comment Call `json:"comment"`
	Label   Call `json:"label"`
	// Fields are CEL over the get tool's result, bound to result: title, body, labels (a list of
	// strings), status, repo, the repository the ticket says its code goes to, if any, and
	// comments, the text of each of the ticket's comments, if mapped: it is how a retried step
	// finds the marker of a comment already posted.
	Fields struct {
		Title    string `json:"title"`
		Body     string `json:"body"`
		Labels   string `json:"labels"`
		Status   string `json:"status"`
		Repo     string `json:"repo"`
		Comments string `json:"comments"`
	} `json:"fields"`
	// ClosedWhen is CEL over status and labels; true means the ticket is closed.
	ClosedWhen string `json:"closed_when"`
}

// Call is a tool and its arguments. In arguments, a string that is exactly {add}, {remove} or
// {labels} becomes that list; {key} and {text} are replaced inside any string.
type Call struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args"`
}

// Tracker is an mcp tracker. It connects on first use and keeps the session.
type Tracker struct {
	cfg     Config
	host    string
	connect func(context.Context) (*mcp.ClientSession, error)

	fields     map[string]cel.Program
	closedWhen cel.Program

	mu      sync.Mutex
	session *mcp.ClientSession
}

// Parse reads a factory.yaml tracker entry.
func Parse(m map[string]any) (Config, error) {
	var c Config
	b, err := json.Marshal(m)
	if err != nil {
		return c, err
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, fmt.Errorf("tracker: %w", err)
	}
	return c, nil
}

// New builds the tracker, compiling its mappings so a mistake shows when the factory is read.
func New(c Config) (*Tracker, error) {
	u, err := url.Parse(c.Site)
	if err != nil || u.Hostname() == "" {
		return nil, fmt.Errorf("site %q is not a web address", c.Site)
	}
	if (len(c.Server.Command) == 0) == (c.Server.URL == "") {
		return nil, errors.New("server needs a command or a url, not both")
	}
	for name, call := range map[string]Call{"get": c.Get, "comment": c.Comment, "label": c.Label} {
		if call.Tool == "" {
			return nil, fmt.Errorf("%s: no tool named", name)
		}
	}
	if c.Fields.Labels == "" || c.Fields.Status == "" || c.Fields.Title == "" {
		return nil, errors.New("fields needs title, labels and status")
	}
	t := &Tracker{cfg: c, host: u.Hostname(), fields: map[string]cel.Program{}}
	resultEnv, err := cel.NewEnv(cel.Variable("result", cel.DynType))
	if err != nil {
		return nil, err
	}
	for name, expr := range map[string]string{"title": c.Fields.Title, "body": c.Fields.Body, "labels": c.Fields.Labels, "status": c.Fields.Status, "repo": c.Fields.Repo, "comments": c.Fields.Comments} {
		if expr == "" {
			continue
		}
		if t.fields[name], err = program(resultEnv, expr); err != nil {
			return nil, fmt.Errorf("fields.%s: %w", name, err)
		}
	}
	if c.ClosedWhen != "" {
		env, err := cel.NewEnv(cel.Variable("status", cel.StringType), cel.Variable("labels", cel.ListType(cel.StringType)))
		if err != nil {
			return nil, err
		}
		if t.closedWhen, err = program(env, c.ClosedWhen); err != nil {
			return nil, fmt.Errorf("closed_when: %w", err)
		}
	}
	t.connect = t.dial
	return t, nil
}

// WithTransport makes the tracker connect through the transport a call to dial gives, instead
// of its configured server: for tests.
func (t *Tracker) WithTransport(dial func() mcp.Transport) *Tracker {
	t.connect = func(ctx context.Context) (*mcp.ClientSession, error) {
		return client().Connect(ctx, dial(), nil)
	}
	return t
}

// Host is the tracker's host, as item keys name it.
func (t *Tracker) Host() string { return t.host }

func program(env *cel.Env, expr string) (cel.Program, error) {
	ast, iss := env.Compile(expr)
	if iss.Err() != nil {
		return nil, iss.Err()
	}
	return env.Program(ast)
}

func client() *mcp.Client {
	return mcp.NewClient(&mcp.Implementation{Name: "ynf", Version: "1"}, nil)
}

func (t *Tracker) dial(ctx context.Context) (*mcp.ClientSession, error) {
	s := t.cfg.Server
	if s.URL != "" {
		hc := http.DefaultClient
		if s.TokenEnv != "" {
			tok := os.Getenv(s.TokenEnv)
			if tok == "" {
				return nil, fmt.Errorf("%s is empty", s.TokenEnv)
			}
			hc = &http.Client{Transport: bearer{tok, http.DefaultTransport}}
		}
		return client().Connect(ctx, &mcp.StreamableClientTransport{Endpoint: s.URL, HTTPClient: hc}, nil)
	}
	// The server is started with ynf's credentials for this tracker and nothing else of its
	// environment, so it cannot read ynf's forge token.
	cmd := exec.Command(s.Command[0], s.Command[1:]...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	for _, name := range s.Env {
		if v, ok := os.LookupEnv(name); ok {
			cmd.Env = append(cmd.Env, name+"="+v)
		}
	}
	return client().Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
}

type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}

// Close ends the session, and with a command, the server.
func (t *Tracker) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.session == nil {
		return nil
	}
	err := t.session.Close()
	t.session = nil
	return err
}

// call calls a tool, connecting first if needed, and once more if the session has gone.
func (t *Tracker) call(ctx context.Context, c Call, vars map[string]any) (any, error) {
	args, _ := fill(c.Args, vars).(map[string]any)
	for attempt := 0; ; attempt++ {
		t.mu.Lock()
		if t.session == nil {
			s, err := t.connect(ctx)
			if err != nil {
				t.mu.Unlock()
				return nil, fmt.Errorf("%s: connect to its MCP server: %w", t.host, err)
			}
			t.session = s
		}
		s := t.session
		t.mu.Unlock()
		res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: c.Tool, Arguments: args})
		if err != nil {
			if attempt == 0 && ctx.Err() == nil {
				_ = t.Close() // a server that went away: start again, once
				continue
			}
			return nil, fmt.Errorf("%s: %s: %w", t.host, c.Tool, err)
		}
		if res.IsError {
			return nil, fmt.Errorf("%s: %s: %s", t.host, c.Tool, text(res))
		}
		return value(res), nil
	}
}

// value is a tool result's data: its structured content, or its text read as JSON, or the text.
func value(res *mcp.CallToolResult) any {
	if res.StructuredContent != nil {
		return res.StructuredContent
	}
	s := text(res)
	var v any
	if json.Unmarshal([]byte(s), &v) == nil {
		return v
	}
	return s
}

func text(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// fill substitutes placeholders in a call's arguments.
func fill(v any, vars map[string]any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = fill(e, vars)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = fill(e, vars)
		}
		return out
	case string:
		for _, list := range []string{"add", "remove", "labels"} {
			if x == "{"+list+"}" {
				return vars[list]
			}
		}
		for _, name := range []string{"key", "text"} {
			if s, ok := vars[name].(string); ok {
				x = strings.ReplaceAll(x, "{"+name+"}", s)
			}
		}
		return x
	}
	return v
}

// Get implements tracker.Tracker.
func (t *Tracker) Get(ctx context.Context, key string) (facts.Ticket, tracker.Text, error) {
	res, err := t.call(ctx, t.cfg.Get, map[string]any{"key": key})
	if err != nil {
		return facts.Ticket{}, tracker.Text{}, err
	}
	str := func(name string) (string, error) {
		p, ok := t.fields[name]
		if !ok {
			return "", nil
		}
		out, _, err := p.Eval(map[string]any{"result": res})
		if err != nil {
			return "", fmt.Errorf("fields.%s: %w", name, err)
		}
		s, ok := out.Value().(string)
		if !ok && out.Value() != nil {
			return "", fmt.Errorf("fields.%s is %T, want a string", name, out.Value())
		}
		return s, nil
	}
	ft := facts.Ticket{Key: key, State: "open"}
	var txt tracker.Text
	if txt.Title, err = str("title"); err != nil {
		return ft, txt, err
	}
	if txt.Body, err = str("body"); err != nil {
		return ft, txt, err
	}
	status, err := str("status")
	if err != nil {
		return ft, txt, err
	}
	if ft.Repo, err = str("repo"); err != nil {
		return ft, txt, err
	}
	out, _, err := t.fields["labels"].Eval(map[string]any{"result": res})
	if err != nil {
		return ft, txt, fmt.Errorf("fields.labels: %w", err)
	}
	raw, err := out.ConvertToNative(reflect.TypeFor[[]string]())
	if err != nil {
		return ft, txt, fmt.Errorf("fields.labels is not a list of strings: %w", err)
	}
	ft.Labels = raw.([]string)
	slices.Sort(ft.Labels)
	if t.closedWhen != nil {
		out, _, err := t.closedWhen.Eval(map[string]any{"status": status, "labels": ft.Labels})
		if err != nil {
			return ft, txt, fmt.Errorf("closed_when: %w", err)
		}
		if closed, _ := out.Value().(bool); closed {
			ft.State = "closed"
		}
	}
	txt.URL = strings.TrimSuffix(t.cfg.Site, "/") + "/browse/" + key
	return ft, txt, nil
}

// Comment implements tracker.Tracker. The marker goes in the comment. With fields.comments
// mapped, the comment is posted only if no existing one contains the marker, so a retried step
// does not comment twice; without it there is no way to know, and it is always posted.
func (t *Tracker) Comment(ctx context.Context, key, marker, body string) error {
	if p, ok := t.fields["comments"]; ok {
		res, err := t.call(ctx, t.cfg.Get, map[string]any{"key": key})
		if err != nil {
			return err
		}
		out, _, err := p.Eval(map[string]any{"result": res})
		if err != nil {
			return fmt.Errorf("fields.comments: %w", err)
		}
		// null is a ticket with no comments yet, as it is for the string fields; a key that is
		// missing altogether is still an error, so a mistyped mapping is not taken for "none".
		if out.Type() != types.NullType {
			raw, err := out.ConvertToNative(reflect.TypeFor[[]string]())
			if err != nil {
				return fmt.Errorf("fields.comments is not a list of strings: %w", err)
			}
			if slices.ContainsFunc(raw.([]string), func(c string) bool { return strings.Contains(c, marker) }) {
				return nil
			}
		}
	}
	_, err := t.call(ctx, t.cfg.Comment, map[string]any{"key": key, "text": body + "\n\n" + marker})
	return err
}

// Label implements tracker.Tracker: {labels} is the ticket's labels after the change, for a
// tool that sets them all; {add} and {remove} are for one that changes them.
func (t *Tracker) Label(ctx context.Context, key string, add, remove []string) error {
	ft, _, err := t.Get(ctx, key)
	if err != nil {
		return err
	}
	labels := slices.DeleteFunc(slices.Clone(ft.Labels), func(l string) bool { return slices.Contains(remove, l) })
	for _, l := range add {
		if !slices.Contains(labels, l) {
			labels = append(labels, l)
		}
	}
	_, err = t.call(ctx, t.cfg.Label, map[string]any{"key": key, "add": strs(add), "remove": strs(remove), "labels": strs(labels)})
	return err
}

func strs(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

// Check connects to the tracker's server and confirms it has every tool the tracker is configured
// to call, so a mistyped tool name shows before any ticket is started.
func (t *Tracker) Check(ctx context.Context) error {
	t.mu.Lock()
	if t.session == nil {
		s, err := t.connect(ctx)
		if err != nil {
			t.mu.Unlock()
			return fmt.Errorf("%s: connect to its MCP server: %w", t.host, err)
		}
		t.session = s
	}
	s := t.session
	t.mu.Unlock()
	have := map[string]bool{}
	for tool, err := range s.Tools(ctx, nil) {
		if err != nil {
			return fmt.Errorf("%s: list its tools: %w", t.host, err)
		}
		have[tool.Name] = true
	}
	var missing []string
	for _, c := range []Call{t.cfg.Get, t.cfg.Comment, t.cfg.Label} {
		if !have[c.Tool] && !slices.Contains(missing, c.Tool) {
			missing = append(missing, c.Tool)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s: its server has no tool %s", t.host, strings.Join(missing, ", "))
	}
	return nil
}
