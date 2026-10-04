package mcptracker_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/tracker/mcptracker"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMain doubles as a stdio MCP server, so the command transport is tested against a real
// process: the test binary run again with YNF_FAKE_MCP set.
func TestMain(m *testing.M) {
	if os.Getenv("YNF_FAKE_MCP") == "1" {
		f := &fakeJira{labels: []string{"from-" + os.Getenv("JIRA_TOKEN")}, status: "To Do"}
		// What else of ynf's environment reached the server: nothing should.
		if os.Getenv("GITHUB_TOKEN") != "" {
			f.labels = append(f.labels, "leaked")
		}
		_ = f.server().Run(context.Background(), &mcp.StdioTransport{})
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestAServerStartedByCommand: ynf starts the tracker's server itself, giving it only PATH, HOME
// and the variables the tracker names: never ynf's forge token.
func TestAServerStartedByCommand(t *testing.T) {
	t.Setenv("JIRA_TOKEN", "t0k")
	t.Setenv("GITHUB_TOKEN", "forge-secret")
	c := config(t, map[string]any{"labels": "{labels}"})
	c.Server.Command = []string{os.Args[0], "-test.run=^$"}
	c.Server.Env = []string{"JIRA_TOKEN", "YNF_FAKE_MCP"}
	t.Setenv("YNF_FAKE_MCP", "1")
	tr, err := mcptracker.New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tr.Close() }()
	ft, _, err := tr.Get(context.Background(), "PLAT-1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ft.Labels, ",") != "from-t0k" {
		t.Fatalf("the server's environment: %v", ft.Labels)
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tr.Get(context.Background(), "PLAT-1"); err != nil {
		t.Fatalf("a closed session reconnects: %v", err)
	}
	c.Server.Command = []string{"/no/such/server"}
	bad, _ := mcptracker.New(c)
	if _, _, err := bad.Get(context.Background(), "PLAT-1"); err == nil || !strings.Contains(err.Error(), "connect to its MCP server") {
		t.Fatalf("a server that does not start: %v", err)
	}
}

// TestAServerByURL: a tracker's server over HTTP, with ynf's bearer token.
func TestAServerByURL(t *testing.T) {
	f := &fakeJira{labels: []string{"a"}, status: "To Do"}
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return f.server() }, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer t0k" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	defer srv.Close()
	c := config(t, map[string]any{"labels": "{labels}"})
	c.Server.Command = nil
	c.Server.URL = srv.URL
	c.Server.TokenEnv = "JIRA_TOKEN"
	t.Setenv("JIRA_TOKEN", "t0k")
	tr, err := mcptracker.New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tr.Close() }()
	if ft, _, err := tr.Get(context.Background(), "PLAT-1"); err != nil || strings.Join(ft.Labels, ",") != "a" {
		t.Fatalf("%+v %v", ft, err)
	}
	t.Setenv("JIRA_TOKEN", "")
	unauth, _ := mcptracker.New(c)
	if _, _, err := unauth.Get(context.Background(), "PLAT-1"); err == nil || !strings.Contains(err.Error(), "JIRA_TOKEN is empty") {
		t.Fatalf("no token: %v", err)
	}
	c.Server.TokenEnv = ""
	anon, _ := mcptracker.New(c)
	if _, _, err := anon.Get(context.Background(), "PLAT-1"); err == nil {
		t.Fatal("a server that refuses ynf")
	}
}
