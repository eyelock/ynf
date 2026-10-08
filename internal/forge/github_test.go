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
	"github.com/eyelock/ynf/internal/tracker"
)

// fakeGitHub serves the handful of API routes ynf uses.
type fakeGitHub struct {
	t        *testing.T
	mu       sync.Mutex
	comments []string
	opened   map[string]any
	labelled []string
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
	case p == "/repos/o/r/branches/main":
		reply(map[string]any{"name": "main", "commit": map[string]any{"sha": "c0ffee"}})
	case p == "/repos/o/r/issues/1/labels" && r.Method == http.MethodPost:
		var add []string
		_ = json.NewDecoder(r.Body).Decode(&add)
		f.labelled = append(f.labelled, "+"+strings.Join(add, ","))
		reply([]map[string]string{})
	case strings.HasPrefix(p, "/repos/o/r/issues/1/labels/") && r.Method == http.MethodDelete:
		name := strings.TrimPrefix(p, "/repos/o/r/issues/1/labels/")
		if name == "absent" {
			w.WriteHeader(http.StatusNotFound)
			reply(map[string]string{"message": "Label does not exist"})
			return
		}
		f.labelled = append(f.labelled, "-"+name)
		reply([]map[string]string{})
	case strings.HasPrefix(p, "/repos/o/r/issues/2/labels"):
		w.WriteHeader(http.StatusForbidden)
		reply(map[string]string{"message": "no"})
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
	case p == "/graphql":
		var q struct {
			Variables struct {
				Owner, Name string
				Number      int
			}
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &q)
		pr := func(merged bool, parents, commits int, mergeHeadline, lastHeadline string) map[string]any {
			return map[string]any{"__typename": "PullRequest", "number": 5, "merged": merged,
				"mergeCommit": map[string]any{"oid": "m10", "messageHeadline": mergeHeadline, "parents": map[string]any{"totalCount": parents}},
				"commits":     map[string]any{"totalCount": commits, "nodes": []any{map[string]any{"commit": map[string]any{"messageHeadline": lastHeadline}}}}}
		}
		closers := map[int]any{
			10: pr(true, 1, 3, "Fix it (#5)", "wip"),                 // squash merged
			11: nil,                                                  // closed by hand
			12: pr(false, 0, 1, "", ""),                              // a pull request that was not merged
			13: map[string]any{"__typename": "Commit", "oid": "m10"}, // a commit, whose merged pull request is looked up
			14: map[string]any{"__typename": "Commit", "oid": "c12"}, // a commit with no merged pull request
			15: pr(true, 1, 3, "last commit", "last commit"),         // rebase merged
			16: pr(true, 2, 3, "Merge pull request #5", "x"),         // a merge commit
			17: pr(true, 1, 1, "only commit", "only commit"),         // one commit: squash and rebase are the same
		}
		if q.Variables.Number == 404 {
			reply(map[string]any{"data": map[string]any{"repository": map[string]any{"issue": nil}}, "errors": []any{map[string]any{"message": "Could not resolve to an Issue"}}})
			return
		}
		node := map[string]any{"closer": closers[q.Variables.Number]}
		reply(map[string]any{"data": map[string]any{"repository": map[string]any{"issue": map[string]any{
			"timelineItems": map[string]any{"nodes": []any{node}}}}}})
	case p == "/repos/o/r/commits/m10/pulls":
		reply([]any{map[string]any{"number": 5, "merged_at": "2026-09-01T00:00:00Z", "merge_commit_sha": "m10"}})
	case p == "/repos/o/r/commits/c12/pulls":
		reply([]any{map[string]any{"number": 6, "merge_commit_sha": "c12"}})
	case p == "/repos/o/r":
		reply(map[string]any{"default_branch": "main"})
	case p == "/repos/o/r/contents/.agents/factory/lanes.yaml":
		reply(map[string]any{"type": "file", "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte("version: 1"))})
	case p == "/repos/o/r/contents/.github/workflows":
		reply([]any{map[string]any{"type": "file", "name": "ci.yml"}, map[string]any{"type": "file", "name": "README.md"}, map[string]any{"type": "dir", "name": "x.yml"}})
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

// TestWorkflows: only workflow files count, and a repository with no workflows folder has none.
func TestWorkflows(t *testing.T) {
	g, _ := setup(t)
	ctx := context.Background()
	if n, err := g.Workflows(ctx, "o/r", "main"); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	if n, err := g.Workflows(ctx, "o/none", "main"); err != nil || n != 0 {
		t.Fatalf("a repository with no workflows folder has none: %d %v", n, err)
	}
}

// TestIssueTracker: GitHub serves its issues through the tracker port, keyed owner/name#number,
// and labels go on and off idempotently.
func TestIssueTracker(t *testing.T) {
	f := &fakeGitHub{t: t}
	srv := httptest.NewServer(f)
	defer srv.Close()
	g, err := forge.NewGitHub("tok", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if g.Host() != "127.0.0.1" {
		t.Fatalf("host %q", g.Host())
	}
	if pub, _ := forge.NewGitHub("tok", ""); pub.Host() != "github.com" {
		t.Fatalf("the public service is github.com, not %q", pub.Host())
	}
	tr := forge.IssueTracker(g)
	ft, text, err := tr.Get(context.Background(), "o/r#1")
	if err != nil || ft.Key != "o/r#1" || ft.Number != 1 || text.Title == "" {
		t.Fatalf("%+v %+v %v", ft, text, err)
	}
	if _, _, err := tr.Get(context.Background(), "o/r#404"); !errors.Is(err, tracker.ErrNotFound) {
		t.Fatalf("a missing issue: %v", err)
	}
	if err := tr.Label(context.Background(), "o/r#1", []string{"ynf:working"}, []string{"ynf:lint", "absent"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.labelled, " ") != "+ynf:working -ynf:lint" {
		t.Fatalf("labels: %v", f.labelled)
	}
	if err := tr.Label(context.Background(), "o/r#2", []string{"x"}, nil); err == nil {
		t.Fatal("a refused label write was not reported")
	}
	if err := tr.Label(context.Background(), "o/r#2", nil, []string{"x"}); err == nil {
		t.Fatal("a refused label removal was not reported")
	}
	if err := tr.Comment(context.Background(), "o/r#1", "<!-- m -->", "hello"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"o/r", "o#1", "o/r/x#1", "o/r#0", "o/r#x"} {
		if _, _, err := tr.Get(context.Background(), bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
		if tr.Comment(context.Background(), bad, "m", "b") == nil || tr.Label(context.Background(), bad, nil, nil) == nil {
			t.Errorf("%q accepted for writes", bad)
		}
	}
	if repo, n, err := forge.ParseIssueKey(forge.IssueKey("a/b", 7)); err != nil || repo != "a/b" || n != 7 {
		t.Fatal("issue keys do not round-trip")
	}
}

func TestHead(t *testing.T) {
	srv := httptest.NewServer(&fakeGitHub{t: t})
	defer srv.Close()
	g, _ := forge.NewGitHub("tok", srv.URL)
	if sha, err := g.Head(context.Background(), "o/r", "main"); err != nil || sha != "c0ffee" {
		t.Fatalf("%q %v", sha, err)
	}
	if _, err := g.Head(context.Background(), "o/r", "gone"); !errors.Is(err, forge.ErrNotFound) {
		t.Fatalf("a missing branch: %v", err)
	}
}

func TestFixFor(t *testing.T) {
	g, _ := setup(t)
	ctx := context.Background()
	for _, n := range []int{10, 13, 16, 17} {
		fix, err := g.FixFor(ctx, "o/r", n)
		if err != nil || fix.MergeSHA != "m10" || fix.PR != 5 {
			t.Errorf("#%d: %+v %v", n, fix, err)
		}
	}
	for _, n := range []int{11, 12, 14} {
		if _, err := g.FixFor(ctx, "o/r", n); !errors.Is(err, forge.ErrNoFix) {
			t.Errorf("#%d: %v, want ErrNoFix", n, err)
		}
	}
	if _, err := g.FixFor(ctx, "o/r", 15); !errors.Is(err, forge.ErrRebased) {
		t.Errorf("a rebase merge: %v", err)
	}
	if _, err := g.FixFor(ctx, "o/r", 404); err == nil {
		t.Error("missing issue")
	}
}

// TestGraphQLEndpoint: GitHub Enterprise Server's REST base is <host>/api/v3/ and its GraphQL
// endpoint <host>/api/graphql; api.github.com's is /graphql beside the REST base.
func TestGraphQLEndpoint(t *testing.T) {
	for base, want := range map[string]string{
		"https://ghe.example/api/v3/": "https://ghe.example/api/graphql",
		"https://api.example.com/":    "https://api.example.com/graphql",
		"http://127.0.0.1:1234":       "http://127.0.0.1:1234/graphql",
	} {
		g, err := forge.NewGitHub("t", base)
		if err != nil {
			t.Fatal(err)
		}
		if got := g.GraphQLURL(); got != want {
			t.Errorf("%s: %s, want %s", base, got, want)
		}
	}
}
