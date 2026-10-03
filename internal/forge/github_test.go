package forge_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/eyelock/ynf/internal/forge"
)

// fakeGitHub serves the handful of API routes ynf uses.
type fakeGitHub struct {
	t        *testing.T
	mu       sync.Mutex
	comments []string
	opened   map[string]any
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	base := "http://" + r.Host + "/"
	switch p := r.URL.Path; {
	case p == "/search/issues":
		if r.URL.Query().Get("page") == "2" {
			reply(map[string]any{"items": []any{map[string]any{"number": 3, "repository_url": base + "repos/o/r"}}})
			return
		}
		w.Header().Set("Link", fmt.Sprintf(`<%ssearch/issues?page=2>; rel="next"`, base))
		reply(map[string]any{"items": []any{
			map[string]any{"number": 1, "repository_url": base + "repos/o/r"},
			map[string]any{"number": 2, "repository_url": base + "repos/o/r", "pull_request": map[string]any{"url": "x"}},
		}})
	case p == "/repos/o/r/issues/1":
		reply(map[string]any{"number": 1, "state": "open", "title": "T", "body": "B", "html_url": "u",
			"labels": []any{map[string]any{"name": "pkg:x"}, map[string]any{"name": "ynf:fmt"}}})
	case p == "/repos/o/r/issues/404":
		w.WriteHeader(http.StatusNotFound)
		reply(map[string]any{"message": "Not Found"})
	case p == "/repos/o/r/pulls/7":
		reply(map[string]any{"number": 7, "state": "open", "draft": true, "merged": false,
			"head": map[string]any{"sha": "abc", "repo": map[string]any{"full_name": "o/r"}},
			"base": map[string]any{"ref": "main", "repo": map[string]any{"full_name": "o/r"}}})
	case p == "/repos/o/r/branches/main/protection/required_status_checks":
		reply(map[string]any{"contexts": []string{"lint"}})
	case p == "/repos/o/r/commits/abc/check-runs":
		reply(map[string]any{"check_runs": []any{
			map[string]any{"name": "lint", "status": "completed", "conclusion": "success"},
			map[string]any{"name": "test", "status": "in_progress"},
		}})
	case p == "/repos/o/r/commits/abc/status":
		reply(map[string]any{"statuses": []any{
			map[string]any{"context": "legacy", "state": "error"},
			map[string]any{"context": "slow", "state": "pending"},
		}})
	case p == "/repos/o/r/pulls/7/reviews":
		reply([]any{
			map[string]any{"user": map[string]any{"login": "a"}, "state": "CHANGES_REQUESTED"},
			map[string]any{"user": map[string]any{"login": "a"}, "state": "APPROVED"},
			map[string]any{"user": map[string]any{"login": "b"}, "state": "COMMENTED"},
		})
	case p == "/repos/o/r/pulls" && r.Method == http.MethodGet:
		if r.URL.Query().Get("head") == "o:ynf/issue-1" {
			reply([]any{map[string]any{"number": 7}})
			return
		}
		reply([]any{})
	case p == "/repos/o/r/pulls" && r.Method == http.MethodPost:
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &f.opened)
		w.WriteHeader(http.StatusCreated)
		reply(map[string]any{"number": 8})
	case p == "/repos/o/r/issues/1/comments" && r.Method == http.MethodGet:
		out := []any{}
		for _, c := range f.comments {
			out = append(out, map[string]any{"body": c})
		}
		reply(out)
	case p == "/repos/o/r/issues/1/comments" && r.Method == http.MethodPost:
		var c map[string]string
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &c)
		f.comments = append(f.comments, c["body"])
		w.WriteHeader(http.StatusCreated)
		reply(map[string]any{"id": 1})
	case p == "/repos/o/r":
		reply(map[string]any{"default_branch": "main"})
	case p == "/repos/o/r/contents/.agents/factory/lanes.yaml":
		reply(map[string]any{"type": "file", "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte("version: 1"))})
	case p == "/repos/o/r/contents/.agents/factory":
		reply([]any{map[string]any{"type": "file", "name": "lanes.yaml"}})
	default:
		w.WriteHeader(http.StatusNotFound)
		reply(map[string]any{"message": "Not Found"})
	}
}

func setup(t *testing.T) (*forge.GitHub, *fakeGitHub) {
	t.Helper()
	f := &fakeGitHub{t: t}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	g, err := forge.NewGitHub("token", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return g, f
}

func TestSearchPaginates(t *testing.T) {
	g, _ := setup(t)
	hits, err := g.Search(context.Background(), "label:ynf")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 3 || hits[0] != (forge.Hit{Repo: "o/r", Number: 1}) || !hits[1].IsPR || hits[2].Number != 3 {
		t.Fatalf("%+v", hits)
	}
}

func TestTicket(t *testing.T) {
	g, _ := setup(t)
	tk, text, err := g.Ticket(context.Background(), "o/r", 1)
	if err != nil || tk.State != "open" || strings.Join(tk.Labels, ",") != "pkg:x,ynf:fmt" || text.Title != "T" || text.Body != "B" {
		t.Fatalf("%+v %+v %v", tk, text, err)
	}
	if _, _, err := g.Ticket(context.Background(), "o/r", 404); !errors.Is(err, forge.ErrNotFound) {
		t.Fatalf("missing ticket: %v", err)
	}
}

func TestPullRequestFacts(t *testing.T) {
	g, _ := setup(t)
	p, err := g.PullRequest(context.Background(), "o/r", 7)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Draft || p.Fork || p.HeadSHA != "abc" {
		t.Fatalf("%+v", p)
	}
	got := map[string]string{}
	for _, c := range p.Checks {
		got[c.Name] = fmt.Sprintf("%s/%s/%v", c.Status, c.Conclusion, c.Required)
	}
	want := map[string]string{"lint": "completed/success/true", "test": "in_progress//false", "legacy": "completed/failure/false", "slow": "in_progress//false"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("check %s: %s, want %s", k, got[k], v)
		}
	}
	if p.ChangesRequested || !p.Approved {
		t.Fatalf("latest review per reviewer should win: changes=%v approved=%v", p.ChangesRequested, p.Approved)
	}
	if p.CIState() != "success" {
		t.Fatalf("only the required lint check gates, and it passed: got %s", p.CIState())
	}
}

func TestFindAndOpenPR(t *testing.T) {
	g, f := setup(t)
	ctx := context.Background()
	if n, err := g.FindPR(ctx, "o/r", "ynf/issue-1"); err != nil || n != 7 {
		t.Fatalf("%d %v", n, err)
	}
	if n, err := g.FindPR(ctx, "o/r", "ynf/issue-2"); err != nil || n != 0 {
		t.Fatalf("%d %v", n, err)
	}
	n, err := g.OpenPR(ctx, "o/r", forge.NewPR{Head: "h", Base: "main", Title: "t", Body: "b", Draft: true})
	if err != nil || n != 8 || f.opened["draft"] != true || f.opened["head"] != "h" {
		t.Fatalf("%d %v %v", n, err, f.opened)
	}
}

func TestCommentIsIdempotent(t *testing.T) {
	g, f := setup(t)
	ctx := context.Background()
	for range 3 {
		if err := g.Comment(ctx, "o/r", 1, "<!-- ynf:step=S -->", "hello"); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.comments) != 1 || !strings.Contains(f.comments[0], "<!-- ynf:step=S -->") {
		t.Fatalf("%q", f.comments)
	}
}

func TestRepoFiles(t *testing.T) {
	g, _ := setup(t)
	ctx := context.Background()
	if b, err := g.DefaultBranch(ctx, "o/r"); err != nil || b != "main" {
		t.Fatalf("%s %v", b, err)
	}
	if b, err := g.File(ctx, "o/r", "main", ".agents/factory/lanes.yaml"); err != nil || string(b) != "version: 1" {
		t.Fatalf("%q %v", b, err)
	}
	if _, err := g.File(ctx, "o/r", "main", ".ynf/lanes.yaml"); !errors.Is(err, forge.ErrNotFound) {
		t.Fatalf("missing file: %v", err)
	}
	if _, err := g.File(ctx, "o/r", "main", ".agents/factory"); !errors.Is(err, forge.ErrNotFound) {
		t.Fatalf("directory: %v", err)
	}
	if _, err := g.DefaultBranch(ctx, "o/missing"); !errors.Is(err, forge.ErrNotFound) {
		t.Fatalf("missing repo: %v", err)
	}
}
