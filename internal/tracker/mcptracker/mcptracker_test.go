package mcptracker_test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/eyelock/ynf/internal/tracker/mcptracker"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeJira is an MCP server shaped like a JIRA one: an issue tool, a comment tool and a tool that
// sets an issue's labels.
type fakeJira struct {
	mu       sync.Mutex
	labels   []string
	status   string
	comments []string
	failing  bool
}

type keyArgs struct {
	IssueKey string `json:"issueKey"`
}

type commentArgs struct {
	IssueKey string `json:"issueKey"`
	Body     string `json:"body"`
}

type labelArgs struct {
	IssueKey string   `json:"issueKey,omitempty"`
	Labels   []string `json:"labels,omitempty"`
	Add      []string `json:"add,omitempty"`
	Remove   []string `json:"remove,omitempty"`
}

func (f *fakeJira) server() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "fake-jira", Version: "1"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "jira_get_issue"}, func(_ context.Context, _ *mcp.CallToolRequest, a keyArgs) (*mcp.CallToolResult, map[string]any, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.failing || a.IssueKey == "NOPE-1" {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "Issue does not exist"}}}, nil, nil
		}
		posted := []any{}
		for _, c := range f.comments {
			posted = append(posted, map[string]any{"body": c})
		}
		return nil, map[string]any{"key": a.IssueKey, "fields": map[string]any{
			"comment": map[string]any{"comments": posted},
			"summary": "Fix the thing", "description": "It is broken.", "labels": slices.Clone(f.labels),
			"status": map[string]any{"name": f.status}, "components": []any{map[string]any{"name": "github.com/acme/x"}},
		}}, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "jira_add_comment"}, func(_ context.Context, _ *mcp.CallToolRequest, a commentArgs) (*mcp.CallToolResult, map[string]any, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.comments = append(f.comments, a.IssueKey+": "+a.Body)
		return nil, map[string]any{"ok": true}, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "jira_set_labels"}, func(_ context.Context, _ *mcp.CallToolRequest, a labelArgs) (*mcp.CallToolResult, map[string]any, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if a.Labels != nil {
			f.labels = a.Labels
		} else {
			f.labels = slices.DeleteFunc(f.labels, func(l string) bool { return slices.Contains(a.Remove, l) })
			f.labels = append(f.labels, a.Add...)
		}
		return nil, map[string]any{"ok": true}, nil
	})
	return s
}

func config(t *testing.T, label map[string]any) mcptracker.Config {
	t.Helper()
	c, err := mcptracker.Parse(map[string]any{
		"provider": "mcp",
		"site":     "https://acme.atlassian.net",
		"server":   map[string]any{"command": []any{"unused"}},
		"get":      map[string]any{"tool": "jira_get_issue", "args": map[string]any{"issueKey": "{key}"}},
		"comment":  map[string]any{"tool": "jira_add_comment", "args": map[string]any{"issueKey": "{key}", "body": "{text}"}},
		"label":    map[string]any{"tool": "jira_set_labels", "args": label},
		"fields": map[string]any{
			"title": "result.fields.summary", "body": "result.fields.description", "labels": "result.fields.labels",
			"status": "result.fields.status.name", "repo": "result.fields.components[0].name",
		},
		"closed_when": `status in ["Done", "Won't Do"]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func connect(t *testing.T, f *fakeJira, c mcptracker.Config) *mcptracker.Tracker {
	t.Helper()
	tr, err := mcptracker.New(c)
	if err != nil {
		t.Fatal(err)
	}
	srv := f.server()
	tr.WithTransport(func() mcp.Transport {
		ct, st := mcp.NewInMemoryTransports()
		go func() { _, _ = srv.Connect(context.Background(), st, nil) }()
		return ct
	})
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

// TestATrackerOverMCP: ynf reads a ticket, comments on it and labels it by calling the tracker's
// MCP tools directly, through the configured mappings.
func TestATrackerOverMCP(t *testing.T) {
	ctx := context.Background()
	f := &fakeJira{labels: []string{"ynf-lint", "keep"}, status: "To Do"}
	tr := connect(t, f, config(t, map[string]any{"issueKey": "{key}", "labels": "{labels}"}))
	if tr.Host() != "acme.atlassian.net" {
		t.Fatal(tr.Host())
	}
	ft, text, err := tr.Get(ctx, "PLAT-881")
	if err != nil {
		t.Fatal(err)
	}
	if ft.Key != "PLAT-881" || ft.State != "open" || strings.Join(ft.Labels, ",") != "keep,ynf-lint" || ft.Repo != "github.com/acme/x" {
		t.Fatalf("%+v", ft)
	}
	if text.Title != "Fix the thing" || text.Body != "It is broken." || text.URL != "https://acme.atlassian.net/browse/PLAT-881" {
		t.Fatalf("%+v", text)
	}
	if err := tr.Comment(ctx, "PLAT-881", "<!-- m -->", "proposed"); err != nil || len(f.comments) != 1 || f.comments[0] != "PLAT-881: proposed\n\n<!-- m -->" {
		t.Fatalf("%v %v", f.comments, err)
	}
	if err := tr.Label(ctx, "PLAT-881", []string{"ynf:working"}, []string{"ynf-lint", "absent"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.labels, ",") != "keep,ynf:working" {
		t.Fatalf("labels set whole: %v", f.labels)
	}
	f.status = "Done"
	if ft, _, _ := tr.Get(ctx, "PLAT-881"); ft.State != "closed" {
		t.Fatalf("closed_when: %s", ft.State)
	}
	if _, _, err := tr.Get(ctx, "NOPE-1"); err == nil || !strings.Contains(err.Error(), "Issue does not exist") {
		t.Fatalf("a tool error: %v", err)
	}
}

func TestLabelsByAddAndRemove(t *testing.T) {
	f := &fakeJira{labels: []string{"a", "b"}, status: "To Do"}
	tr := connect(t, f, config(t, map[string]any{"issueKey": "{key}", "add": "{add}", "remove": "{remove}"}))
	if err := tr.Label(context.Background(), "PLAT-1", []string{"c"}, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.labels, ",") != "b,c" {
		t.Fatalf("%v", f.labels)
	}
	f.failing = true
	if err := tr.Label(context.Background(), "PLAT-1", []string{"d"}, nil); err == nil {
		t.Fatal("a label change on an issue that cannot be read")
	}
}

// TestBadConfigsFailWhenRead: a tracker that cannot work fails when the factory is read, not
// when it is first used.
func TestBadConfigsFailWhenRead(t *testing.T) {
	good := config(t, map[string]any{"labels": "{labels}"})
	for name, change := range map[string]func(*mcptracker.Config){
		"no site":         func(c *mcptracker.Config) { c.Site = "" },
		"no server":       func(c *mcptracker.Config) { c.Server.Command = nil },
		"both servers":    func(c *mcptracker.Config) { c.Server.URL = "https://x" },
		"no get tool":     func(c *mcptracker.Config) { c.Get.Tool = "" },
		"no status":       func(c *mcptracker.Config) { c.Fields.Status = "" },
		"bad mapping":     func(c *mcptracker.Config) { c.Fields.Title = "result.(" },
		"bad comments":    func(c *mcptracker.Config) { c.Fields.Comments = "result.(" },
		"bad closed_when": func(c *mcptracker.Config) { c.ClosedWhen = "status ==" },
	} {
		c := good
		change(&c)
		if _, err := mcptracker.New(c); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := mcptracker.Parse(map[string]any{"provider": "mcp", "surprise": 1}); err == nil {
		t.Error("an unknown key accepted")
	}
}

// TestMappingsThatDoNotFit: a mapping that does not fit the tool's result is an error that says
// which.
func TestMappingsThatDoNotFit(t *testing.T) {
	f := &fakeJira{labels: []string{"a"}, status: "To Do"}
	c := config(t, map[string]any{"labels": "{labels}"})
	c.Fields.Labels = "result.fields.summary"
	if _, _, err := connect(t, f, c).Get(context.Background(), "PLAT-1"); err == nil || !strings.Contains(err.Error(), "fields.labels") {
		t.Fatalf("labels that are not a list: %v", err)
	}
	c = config(t, map[string]any{"labels": "{labels}"})
	c.Fields.Title = "result.fields.labels"
	if _, _, err := connect(t, f, c).Get(context.Background(), "PLAT-1"); err == nil || !strings.Contains(err.Error(), "fields.title") {
		t.Fatalf("a title that is not a string: %v", err)
	}
	c = config(t, map[string]any{"labels": "{labels}"})
	c.Fields.Status = "result.missing.name"
	if _, _, err := connect(t, f, c).Get(context.Background(), "PLAT-1"); err == nil || !strings.Contains(err.Error(), "fields.status") {
		t.Fatalf("a missing field: %v", err)
	}
}

// TestCheck: a tracker checks its server has every tool it is configured to call, before any
// ticket depends on it.
func TestCheck(t *testing.T) {
	ctx := context.Background()
	f := &fakeJira{status: "To Do"}
	if err := connect(t, f, config(t, map[string]any{"issueKey": "{key}", "labels": "{labels}"})).Check(ctx); err != nil {
		t.Fatal(err)
	}
	c := config(t, map[string]any{"issueKey": "{key}", "labels": "{labels}"})
	c.Comment.Tool = "jira_add_coment"
	if err := connect(t, f, c).Check(ctx); err == nil || !strings.Contains(err.Error(), "no tool jira_add_coment") {
		t.Fatalf("a mistyped tool: %v", err)
	}
}

// TestCommentOnceWhenCommentsAreMapped: with fields.comments mapped, a comment whose marker is
// already on the ticket is not posted again, and one with another marker is.
func TestCommentOnceWhenCommentsAreMapped(t *testing.T) {
	ctx := context.Background()
	f := &fakeJira{status: "To Do"}
	c := config(t, map[string]any{"issueKey": "{key}", "labels": "{labels}"})
	c.Fields.Comments = "result.fields.comment.comments.map(c, c.body)"
	tr := connect(t, f, c)
	for _, marker := range []string{"<!-- a -->", "<!-- a -->", "<!-- b -->"} {
		if err := tr.Comment(ctx, "PLAT-881", marker, "proposed"); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"PLAT-881: proposed\n\n<!-- a -->", "PLAT-881: proposed\n\n<!-- b -->"}
	if !slices.Equal(f.comments, want) {
		t.Fatalf("%q", f.comments)
	}
	if err := tr.Comment(ctx, "NOPE-1", "<!-- c -->", "x"); err == nil || !strings.Contains(err.Error(), "Issue does not exist") {
		t.Fatalf("a ticket that cannot be read: %v", err)
	}
}

// TestCommentWithoutCommentsMapped: with no way to read comments, every Comment posts.
func TestCommentWithoutCommentsMapped(t *testing.T) {
	f := &fakeJira{status: "To Do"}
	tr := connect(t, f, config(t, map[string]any{"issueKey": "{key}", "labels": "{labels}"}))
	for range 2 {
		if err := tr.Comment(context.Background(), "PLAT-881", "<!-- a -->", "proposed"); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.comments) != 2 {
		t.Fatalf("%q", f.comments)
	}
}

// TestCommentsThatDoNotFit: a comments mapping that is not a list of strings is an error that
// says so, and nothing is posted.
func TestCommentsThatDoNotFit(t *testing.T) {
	f := &fakeJira{status: "To Do"}
	for name, expr := range map[string]string{"not a list": "result.fields.summary", "missing": "result.missing.x"} {
		c := config(t, map[string]any{"issueKey": "{key}", "labels": "{labels}"})
		c.Fields.Comments = expr
		if err := connect(t, f, c).Comment(context.Background(), "PLAT-881", "<!-- a -->", "x"); err == nil || !strings.Contains(err.Error(), "fields.comments") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(f.comments) != 0 {
		t.Fatalf("%q", f.comments)
	}
}

// TestCommentWhenTheTicketHasNoCommentsYet: a comments mapping that yields null is a ticket with no
// comments yet, so the comment is posted; it is not mistaken for a mapping that doesn't fit.
func TestCommentWhenTheTicketHasNoCommentsYet(t *testing.T) {
	f := &fakeJira{status: "To Do"}
	c := config(t, map[string]any{"issueKey": "{key}", "labels": "{labels}"})
	c.Fields.Comments = "null"
	if err := connect(t, f, c).Comment(context.Background(), "PLAT-881", "<!-- a -->", "proposed"); err != nil {
		t.Fatal(err)
	}
	if len(f.comments) != 1 {
		t.Fatalf("%q", f.comments)
	}
}
