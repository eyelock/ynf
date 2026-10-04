// Command ynf-sandbox-tracker is the sandbox's tracker that is not a forge: a JIRA stand-in that
// speaks MCP on stdin and stdout, keeping its tickets in the JSON file YNF_SANDBOX_TRACKER_DATA
// names. ynf reads, comments on and labels its tickets through the mcp tracker provider, as it
// would a real JIRA's MCP server; e2e writes the tickets and reads back what ynf did.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Ticket is one ticket in the data file.
type Ticket struct {
	Title    string   `json:"title"`
	Body     string   `json:"body"`
	Labels   []string `json:"labels"`
	Status   string   `json:"status"`
	Repo     string   `json:"repo,omitempty"`
	Comments []string `json:"comments,omitempty"`
}

type data struct {
	path string
	mu   sync.Mutex
}

func (d *data) update(f func(map[string]*Ticket) error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	b, err := os.ReadFile(d.path)
	if err != nil {
		return err
	}
	tickets := map[string]*Ticket{}
	if err := json.Unmarshal(b, &tickets); err != nil {
		return err
	}
	if err := f(tickets); err != nil {
		return err
	}
	out, _ := json.MarshalIndent(tickets, "", "  ")
	return os.WriteFile(d.path, out, 0o644)
}

type keyArgs struct {
	Key string `json:"key"`
}

type commentArgs struct {
	Key  string `json:"key"`
	Body string `json:"body"`
}

type labelArgs struct {
	Key    string   `json:"key"`
	Labels []string `json:"labels"`
}

func main() {
	d := &data{path: os.Getenv("YNF_SANDBOX_TRACKER_DATA")}
	if d.path == "" {
		fmt.Fprintln(os.Stderr, "ynf-sandbox-tracker: YNF_SANDBOX_TRACKER_DATA is not set")
		os.Exit(2)
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "ynf-sandbox-tracker", Version: "1"}, nil)
	failed := func(err error) *mcp.CallToolResult {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}
	}
	mcp.AddTool(s, &mcp.Tool{Name: "get_ticket"}, func(_ context.Context, _ *mcp.CallToolRequest, a keyArgs) (*mcp.CallToolResult, *Ticket, error) {
		var got *Ticket
		err := d.update(func(ts map[string]*Ticket) error {
			if got = ts[a.Key]; got == nil {
				return fmt.Errorf("no ticket %s", a.Key)
			}
			return nil
		})
		if err != nil {
			return failed(err), nil, nil
		}
		return nil, got, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "add_comment"}, func(_ context.Context, _ *mcp.CallToolRequest, a commentArgs) (*mcp.CallToolResult, map[string]bool, error) {
		err := d.update(func(ts map[string]*Ticket) error {
			if ts[a.Key] == nil {
				return fmt.Errorf("no ticket %s", a.Key)
			}
			ts[a.Key].Comments = append(ts[a.Key].Comments, a.Body)
			return nil
		})
		if err != nil {
			return failed(err), nil, nil
		}
		return nil, map[string]bool{"ok": true}, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "set_labels"}, func(_ context.Context, _ *mcp.CallToolRequest, a labelArgs) (*mcp.CallToolResult, map[string]bool, error) {
		err := d.update(func(ts map[string]*Ticket) error {
			if ts[a.Key] == nil {
				return fmt.Errorf("no ticket %s", a.Key)
			}
			ts[a.Key].Labels = a.Labels
			return nil
		})
		if err != nil {
			return failed(err), nil, nil
		}
		return nil, map[string]bool{"ok": true}, nil
	})
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, "ynf-sandbox-tracker:", err)
		os.Exit(1)
	}
}
