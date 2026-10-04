package cli

import (
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/workspace"
)

// TestNewForge: a declared forge gets a client on its own API, a git workspace on its own host
// with its own token, and refuses to start without the token.
func TestNewForge(t *testing.T) {
	if githubAPI("github.com") != "" || githubAPI("ghe.acme.internal") != "https://ghe.acme.internal/api/v3/" {
		t.Fatal("API addresses")
	}
	build := newForge(t.TempDir(), workspace.Author{Name: "ynf"}, githubAPI)
	t.Setenv("GHE_TOKEN", "")
	if _, err := build("ghe", map[string]any{"url": "https://ghe.acme.internal", "token_env": "GHE_TOKEN"}); err == nil || !strings.Contains(err.Error(), "GHE_TOKEN is empty") {
		t.Fatalf("no token: %v", err)
	}
	if _, err := build("ghe", map[string]any{"url": "::", "token_env": "GHE_TOKEN"}); err == nil {
		t.Fatal("a bad address")
	}
	t.Setenv("GHE_TOKEN", "t0k")
	inst, err := build("ghe", map[string]any{"url": "https://ghe.acme.internal/", "token_env": "GHE_TOKEN"})
	if err != nil || inst.Host != "ghe.acme.internal" || inst.Tracker == nil {
		t.Fatalf("%+v %v", inst, err)
	}
	ws := inst.Git.(workspace.Workspace)
	if ws.Token != "t0k" || ws.RemoteURL("ghe.acme.internal/acme/x") != "https://ghe.acme.internal/acme/x.git" {
		t.Fatalf("%+v %s", ws, ws.RemoteURL("ghe.acme.internal/acme/x"))
	}
}
