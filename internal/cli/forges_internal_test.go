package cli

import (
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/forge"
	"github.com/eyelock/ynf/internal/workspace"
)

// TestNewForge: a declared forge gets a client on its own API, a git workspace on its own host
// with its own token, and refuses to start without the token.
func TestNewForge(t *testing.T) {
	if githubAPI("github.com") != "" || githubAPI("ghe.example.internal") != "https://ghe.example.internal/api/v3/" {
		t.Fatal("API addresses")
	}
	build := newForge(t.TempDir(), workspace.Author{Name: "ynf"}, githubAPI)
	t.Setenv("GHE_TOKEN", "")
	if _, err := build("ghe", map[string]any{"url": "https://ghe.example.internal", "token_env": "GHE_TOKEN"}); err == nil || !strings.Contains(err.Error(), "GHE_TOKEN is empty") {
		t.Fatalf("no token: %v", err)
	}
	if _, err := build("ghe", map[string]any{"url": "::", "token_env": "GHE_TOKEN"}); err == nil {
		t.Fatal("a bad address")
	}
	t.Setenv("GHE_TOKEN", "t0k")
	inst, err := build("ghe", map[string]any{"url": "https://ghe.example.internal/", "token_env": "GHE_TOKEN"})
	if err != nil || inst.Host != "ghe.example.internal" || inst.Tracker == nil {
		t.Fatalf("%+v %v", inst, err)
	}
	ws := inst.Git.(workspace.Workspace)
	if ws.Token != "t0k" || ws.RemoteURL("ghe.example.internal/example-org/x") != "https://ghe.example.internal/example-org/x.git" {
		t.Fatalf("%+v %s", ws, ws.RemoteURL("ghe.example.internal/example-org/x"))
	}
}

// TestForgeWorkspaceFollowsTheAPIHost: with YNF_GITHUB_API on an Enterprise Server, the main
// forge's workspace clones from that host and scopes its token to it; github.com keeps defaults.
func TestForgeWorkspaceFollowsTheAPIHost(t *testing.T) {
	a := workspace.Author{Name: "ynf"}
	pub := forgeWorkspace(t.TempDir(), "t", a, "github.com")
	if pub.Host != "" || pub.RemoteURL != nil {
		t.Fatalf("github.com: %+v", pub)
	}
	fg, err := forge.NewGitHub("t", "https://ghe.example.internal/api/v3/")
	if err != nil {
		t.Fatal(err)
	}
	ghe := forgeWorkspace(t.TempDir(), "t", a, fg.GitHost())
	if ghe.Host != "ghe.example.internal" || ghe.RemoteURL("o/r") != "https://ghe.example.internal/o/r.git" {
		t.Fatalf("ghe: %+v %s", ghe, ghe.RemoteURL("o/r"))
	}
	port, err := forge.NewGitHub("t", "https://ghes.example.internal:8443/api/v3/")
	if err != nil {
		t.Fatal(err)
	}
	pw := forgeWorkspace(t.TempDir(), "t", a, port.GitHost())
	if pw.Host != "ghes.example.internal:8443" || pw.RemoteURL("o/r") != "https://ghes.example.internal:8443/o/r.git" {
		t.Fatalf("ghes port: %+v %s", pw, pw.RemoteURL("o/r"))
	}
	for _, api := range []string{"http://127.0.0.1:4010", "http://localhost:4010", "http://[::1]:4010"} {
		lo, err := forge.NewGitHub("t", api)
		if err != nil {
			t.Fatal(err)
		}
		if w := forgeWorkspace(t.TempDir(), "t", a, lo.GitHost()); w.Host != "" || w.RemoteURL != nil {
			t.Fatalf("a loopback API %s keeps the github.com defaults: %+v", api, w)
		}
	}
}
