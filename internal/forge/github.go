package forge

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/eyelock/ynf/internal/facts"
	"github.com/google/go-github/v84/github"
)

// GitHub is the GitHub provider.
type GitHub struct {
	c *github.Client
	// Log receives the warning that a repository's required checks could not be read; the default
	// logger when nil.
	Log    *slog.Logger
	warned sync.Map // repositories already warned about
}

// NewGitHub returns a GitHub forge authenticated with token. baseURL is for tests; "" is api.github.com.
func NewGitHub(token, baseURL string) (*GitHub, error) {
	c := github.NewClient(nil).WithAuthToken(token)
	if baseURL != "" {
		u, err := url.Parse(strings.TrimSuffix(baseURL, "/") + "/")
		if err != nil {
			return nil, err
		}
		c.BaseURL = u
	}
	return &GitHub{c: c}, nil
}

func split(repo string) (string, string) {
	o, r, _ := strings.Cut(repo, "/")
	return o, r
}

func notFound(err error) error {
	var e *github.ErrorResponse
	if errors.As(err, &e) && e.Response != nil && e.Response.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	return err
}

// Search implements Forge.
func (g *GitHub) Search(ctx context.Context, query string) ([]Hit, error) {
	var hits []Hit
	opt := &github.SearchOptions{Sort: "created", Order: "asc", ListOptions: github.ListOptions{PerPage: 100}}
	for {
		res, resp, err := g.c.Search.Issues(ctx, query, opt)
		if err != nil {
			return nil, fmt.Errorf("search %q: %w", query, err)
		}
		for _, is := range res.Issues {
			repo := strings.TrimPrefix(is.GetRepositoryURL(), g.c.BaseURL.String()+"repos/")
			hits = append(hits, Hit{Repo: repo, Number: is.GetNumber(), IsPR: is.IsPullRequest()})
		}
		if resp.NextPage == 0 {
			return hits, nil
		}
		opt.Page = resp.NextPage
	}
}

// Host is the forge instance's host, as item keys name it: github.com for the public service.
func (g *GitHub) Host() string {
	if h := g.c.BaseURL.Hostname(); h != "api.github.com" {
		return h
	}
	return "github.com"
}

// SetLabels implements Issues: labels already present or already absent are left alone.
func (g *GitHub) SetLabels(ctx context.Context, repo string, number int, add, remove []string) error {
	o, r := split(repo)
	if len(add) > 0 {
		if _, _, err := g.c.Issues.AddLabelsToIssue(ctx, o, r, number, add); err != nil {
			return err
		}
	}
	for _, l := range remove {
		if _, err := g.c.Issues.RemoveLabelForIssue(ctx, o, r, number, l); err != nil && !errors.Is(notFound(err), ErrNotFound) {
			return err
		}
	}
	return nil
}

// Ticket implements Issues.
func (g *GitHub) Ticket(ctx context.Context, repo string, number int) (facts.Ticket, Text, error) {
	o, r := split(repo)
	is, _, err := g.c.Issues.Get(ctx, o, r, number)
	if err != nil {
		return facts.Ticket{}, Text{}, notFound(err)
	}
	labels := make([]string, 0, len(is.Labels))
	for _, l := range is.Labels {
		labels = append(labels, l.GetName())
	}
	slices.Sort(labels)
	return facts.Ticket{Number: number, State: is.GetState(), Labels: labels},
		Text{Title: is.GetTitle(), Body: is.GetBody(), URL: is.GetHTMLURL()}, nil
}

// PullRequest implements Forge: state, checks on the head (check runs and commit statuses, every
// page of each), the base branch's required checks marked, and the latest review per reviewer.
// A required check that has not reported is listed too, with status "expected", so the pull
// request cannot read as green while it is missing.
func (g *GitHub) PullRequest(ctx context.Context, repo string, number int) (*facts.PR, error) {
	o, r := split(repo)
	pr, _, err := g.c.PullRequests.Get(ctx, o, r, number)
	if err != nil {
		return nil, notFound(err)
	}
	p := &facts.PR{
		Number: number, State: pr.GetState(), Merged: pr.GetMerged(), Draft: pr.GetDraft(),
		Fork:    pr.GetHead().GetRepo().GetFullName() != pr.GetBase().GetRepo().GetFullName(),
		HeadSHA: pr.GetHead().GetSHA(), HeadRef: pr.GetHead().GetRef(),
	}
	req := g.RequiredChecks(ctx, repo, pr.GetBase().GetRef())
	if !req.Known {
		p.RequiredUnknown = true
		g.warnUnknown(repo, pr.GetBase().GetRef(), req.Detail)
	}
	met := make([]bool, len(req.Checks))
	// mark sets a check's Required if a requirement names it. A requirement bound to an App is met
	// only by a check run from that App; a commit status has no App.
	mark := func(c *facts.Check, isRun bool) {
		for i, rq := range req.Checks {
			bound := rq.AppID > 0
			if rq.Context == c.Name && (!bound || isRun && rq.AppID == c.AppID) {
				c.Required, met[i] = true, true
			}
		}
	}

	runOpt := &github.ListCheckRunsOptions{ListOptions: github.ListOptions{PerPage: 100}}
	for {
		runs, resp, err := g.c.Checks.ListCheckRunsForRef(ctx, o, r, p.HeadSHA, runOpt)
		if err != nil {
			return nil, fmt.Errorf("check runs: %w", err)
		}
		for _, cr := range runs.CheckRuns {
			c := facts.Check{Name: cr.GetName(), Status: cr.GetStatus(), Conclusion: cr.GetConclusion(), App: cr.GetApp().GetSlug(), AppID: cr.GetApp().GetID()}
			mark(&c, true)
			p.Checks = append(p.Checks, c)
		}
		if resp.NextPage == 0 {
			break
		}
		runOpt.Page = resp.NextPage
	}
	stOpt := &github.ListOptions{PerPage: 100}
	for {
		st, resp, err := g.c.Repositories.GetCombinedStatus(ctx, o, r, p.HeadSHA, stOpt)
		if err != nil {
			return nil, fmt.Errorf("statuses: %w", err)
		}
		for _, s := range st.Statuses {
			c := facts.Check{Name: s.GetContext(), Status: "completed", Conclusion: s.GetState()}
			switch s.GetState() {
			case "pending":
				c.Status, c.Conclusion = "in_progress", ""
			case "error":
				c.Conclusion = "failure"
			}
			mark(&c, false)
			p.Checks = append(p.Checks, c)
		}
		if resp.NextPage == 0 {
			break
		}
		stOpt.Page = resp.NextPage
	}
	for i, rq := range req.Checks {
		if !met[i] {
			p.Checks = append(p.Checks, facts.Check{Name: rq.Context, Status: facts.StatusExpected, Required: true, AppID: rq.AppID})
		}
	}
	slices.SortStableFunc(p.Checks, func(a, b facts.Check) int {
		return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.App, b.App), cmp.Compare(a.AppID, b.AppID))
	})

	reviews, _, err := g.c.PullRequests.ListReviews(ctx, o, r, number, &github.ListOptions{PerPage: 100})
	if err != nil {
		return nil, fmt.Errorf("reviews: %w", err)
	}
	latest := map[string]string{}
	for _, rv := range reviews {
		if s := rv.GetState(); s == "APPROVED" || s == "CHANGES_REQUESTED" || s == "DISMISSED" {
			latest[rv.GetUser().GetLogin()] = s
		}
	}
	for _, s := range latest {
		p.ChangesRequested = p.ChangesRequested || s == "CHANGES_REQUESTED"
		p.Approved = p.Approved || s == "APPROVED"
	}
	return p, nil
}

// RequiredCheck is a check a branch requires: its name, and the GitHub App it must come from, or 0
// for any source.
type RequiredCheck struct {
	Context string
	AppID   int64
}

// Required is what a branch requires before it merges, from classic branch protection and from the
// repository rulesets that apply to it, together.
type Required struct {
	Checks []RequiredCheck
	// Known is false when neither source could be read. Checks is then empty, which says nothing
	// about what the branch requires.
	Known bool
	// Detail says how it was read, or why it could not be.
	Detail string
}

// RequiredChecks reads the checks branch requires: the union of classic branch protection and the
// rulesets that apply to it. An unprotected branch is known to require none. Only when both
// sources fail to be read is the answer unknown.
func (g *GitHub) RequiredChecks(ctx context.Context, repo, branch string) Required {
	o, r := split(repo)
	var out Required
	seen := map[RequiredCheck]bool{}
	add := func(context string, appID *int64) {
		rc := RequiredCheck{Context: context}
		if appID != nil && *appID > 0 {
			rc.AppID = *appID
		}
		if !seen[rc] {
			seen[rc] = true
			out.Checks = append(out.Checks, rc)
		}
	}

	var problems []string
	rs, resp, err := g.c.Repositories.GetRequiredStatusChecks(ctx, o, r, branch)
	switch {
	case err == nil:
		out.Known = true
		// contexts lists every name, whatever its App; checks says which App each is bound to.
		bound := map[string]bool{}
		for _, c := range rs.GetChecks() {
			bound[c.Context] = true
			add(c.Context, c.AppID)
		}
		for _, c := range rs.GetContexts() {
			if !bound[c] {
				add(c, nil)
			}
		}
	case resp != nil && resp.StatusCode == http.StatusNotFound && strings.Contains(err.Error(), "not protected"):
		out.Known = true // readable, and the branch has no classic protection
	default:
		problems = append(problems, "branch protection: "+err.Error())
	}

	opt := &github.ListOptions{PerPage: 100}
	rulesRead := true
	for {
		br, resp, err := g.c.Repositories.GetRulesForBranch(ctx, o, r, branch, opt)
		if err != nil {
			rulesRead = false
			problems = append(problems, "rulesets: "+err.Error())
			break
		}
		for _, rule := range br.RequiredStatusChecks {
			for _, c := range rule.Parameters.RequiredStatusChecks {
				add(c.Context, c.IntegrationID)
			}
		}
		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}
	out.Known = out.Known || rulesRead
	if !out.Known {
		out.Checks = nil
		out.Detail = strings.Join(problems, "; ")
		return out
	}
	slices.SortFunc(out.Checks, func(a, b RequiredCheck) int {
		return cmp.Or(strings.Compare(a.Context, b.Context), cmp.Compare(a.AppID, b.AppID))
	})
	out.Detail = fmt.Sprintf("%d required checks on %s", len(out.Checks), branch)
	if len(problems) > 0 {
		out.Detail += " (" + strings.Join(problems, "; ") + ")"
	}
	return out
}

// warnUnknown logs once per repository, for the life of the process, that its required checks
// could not be read and every check gates instead.
func (g *GitHub) warnUnknown(repo, branch, detail string) {
	if _, dup := g.warned.LoadOrStore(repo, true); dup {
		return
	}
	log := g.Log
	if log == nil {
		log = slog.Default()
	}
	log.Warn("required checks unreadable; gating on every check", "repo", repo, "branch", branch, "detail", detail)
}

// FindPR implements Forge.
func (g *GitHub) FindPR(ctx context.Context, repo, branch string) (int, error) {
	o, r := split(repo)
	prs, _, err := g.c.PullRequests.List(ctx, o, r, &github.PullRequestListOptions{State: "open", Head: o + ":" + branch})
	if err != nil {
		return 0, err
	}
	if len(prs) == 0 {
		return 0, nil
	}
	return prs[0].GetNumber(), nil
}

// OpenPR implements Forge.
func (g *GitHub) OpenPR(ctx context.Context, repo string, n NewPR) (int, error) {
	o, r := split(repo)
	pr, _, err := g.c.PullRequests.Create(ctx, o, r, &github.NewPullRequest{
		Title: &n.Title, Head: &n.Head, Base: &n.Base, Body: &n.Body, Draft: &n.Draft,
	})
	if err != nil {
		return 0, err
	}
	return pr.GetNumber(), nil
}

// Comment implements Forge and Issues.
func (g *GitHub) Comment(ctx context.Context, repo string, number int, marker, body string) error {
	o, r := split(repo)
	opt := &github.IssueListCommentsOptions{ListOptions: github.ListOptions{PerPage: 100}}
	for {
		cs, resp, err := g.c.Issues.ListComments(ctx, o, r, number, opt)
		if err != nil {
			return err
		}
		for _, c := range cs {
			if strings.Contains(c.GetBody(), marker) {
				return nil
			}
		}
		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}
	text := body + "\n\n" + marker
	_, _, err := g.c.Issues.CreateComment(ctx, o, r, number, &github.IssueComment{Body: &text})
	return err
}

// Head implements Forge.
func (g *GitHub) Head(ctx context.Context, repo, branch string) (string, error) {
	o, r := split(repo)
	b, resp, err := g.c.Repositories.GetBranch(ctx, o, r, branch, 0)
	if err != nil {
		// GetBranch reports a status it does not expect as a plain error, with the response.
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return "", ErrNotFound
		}
		return "", notFound(err)
	}
	return b.GetCommit().GetSHA(), nil
}

// DefaultBranch implements Forge.
func (g *GitHub) DefaultBranch(ctx context.Context, repo string) (string, error) {
	o, r := split(repo)
	rp, _, err := g.c.Repositories.Get(ctx, o, r)
	if err != nil {
		return "", notFound(err)
	}
	return rp.GetDefaultBranch(), nil
}

// File implements Forge.
func (g *GitHub) File(ctx context.Context, repo, ref, path string) ([]byte, error) {
	o, r := split(repo)
	fc, _, _, err := g.c.Repositories.GetContents(ctx, o, r, path, &github.RepositoryContentGetOptions{Ref: ref})
	if err != nil {
		return nil, notFound(err)
	}
	if fc == nil {
		return nil, ErrNotFound // a directory
	}
	s, err := fc.GetContent()
	return []byte(s), err
}

// GraphQLURL is the GraphQL endpoint beside the REST base: <base>graphql on api.github.com (and a
// test server), <host>/api/graphql on GitHub Enterprise Server, whose REST base ends /api/v3/.
func (g *GitHub) GraphQLURL() string {
	u := *g.c.BaseURL
	if p, ok := strings.CutSuffix(u.Path, "/api/v3/"); ok {
		u.Path = p + "/api/graphql"
	} else {
		u.Path = strings.TrimSuffix(u.Path, "/") + "/graphql"
	}
	return u.String()
}

const closerQuery = `query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    issue(number: $number) {
      timelineItems(itemTypes: [CLOSED_EVENT], last: 1) {
        nodes {
          ... on ClosedEvent {
            closer {
              __typename
              ... on PullRequest {
                number merged
                mergeCommit { oid messageHeadline parents { totalCount } }
                commits(last: 1) { totalCount nodes { commit { messageHeadline } } }
              }
              ... on Commit { oid }
            }
          }
        }
      }
    }
  }
}`

type closer struct {
	Typename    string `json:"__typename"`
	Number      int
	Merged      bool
	OID         string
	MergeCommit *struct {
		OID             string
		MessageHeadline string
		Parents         struct{ TotalCount int }
	}
	Commits struct {
		TotalCount int
		Nodes      []struct {
			Commit struct{ MessageHeadline string }
		}
	}
}

// FixFor implements Fixes. GitHub's GraphQL ClosedEvent names what closed the issue, which the REST
// timeline does not: a merged pull request gives the fix with its merge commit; a commit is looked
// up for the merged pull request that made it; an issue closed by hand, or by a pull request that
// was not merged, is ErrNoFix. A pull request that was rebase merged has no merge commit to take a
// base from, and is ErrRebased.
func (g *GitHub) FixFor(ctx context.Context, repo string, number int) (Fix, error) {
	o, r := split(repo)
	body := map[string]any{"query": closerQuery, "variables": map[string]any{"owner": o, "name": r, "number": number}}
	req, err := g.c.NewRequest(http.MethodPost, g.GraphQLURL(), body)
	if err != nil {
		return Fix{}, err
	}
	var res struct {
		Data struct {
			Repository *struct {
				Issue *struct {
					TimelineItems struct {
						Nodes []struct{ Closer *closer }
					}
				}
			}
		}
		Errors []struct{ Message string }
	}
	if _, err := g.c.Do(ctx, req, &res); err != nil {
		return Fix{}, notFound(err)
	}
	if res.Data.Repository == nil || res.Data.Repository.Issue == nil {
		if len(res.Errors) > 0 {
			return Fix{}, fmt.Errorf("graphql: %s", res.Errors[0].Message)
		}
		return Fix{}, ErrNotFound
	}
	nodes := res.Data.Repository.Issue.TimelineItems.Nodes
	if len(nodes) == 0 || nodes[0].Closer == nil {
		return Fix{}, ErrNoFix
	}
	c := nodes[0].Closer
	switch c.Typename {
	case "PullRequest":
		if !c.Merged || c.MergeCommit == nil {
			return Fix{}, ErrNoFix
		}
		return fixOf(c)
	case "Commit":
		prs, _, err := g.c.PullRequests.ListPullRequestsWithCommit(ctx, o, r, c.OID, &github.ListOptions{PerPage: 100})
		if err != nil {
			return Fix{}, notFound(err)
		}
		for _, pr := range prs {
			if !pr.GetMergedAt().IsZero() && pr.GetMergeCommitSHA() == c.OID {
				return Fix{PR: pr.GetNumber(), MergeSHA: c.OID}, nil
			}
		}
	}
	return Fix{}, ErrNoFix
}

// fixOf reads a merged pull request's fix. One parent and several commits is a squash or a rebase:
// a squash's commit is titled for the pull request, a rebase's is a copy of the last commit, so
// the same headline gives it away. Without the headlines it cannot be told, and is ErrRebased.
func fixOf(c *closer) (Fix, error) {
	if c.MergeCommit.Parents.TotalCount == 1 && c.Commits.TotalCount > 1 {
		if len(c.Commits.Nodes) == 0 || c.MergeCommit.MessageHeadline == "" || c.MergeCommit.MessageHeadline == c.Commits.Nodes[0].Commit.MessageHeadline {
			return Fix{}, ErrRebased
		}
	}
	return Fix{PR: c.Number, MergeSHA: c.MergeCommit.OID}, nil
}
