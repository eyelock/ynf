package forge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/eyelock/ynf/internal/facts"
	"github.com/google/go-github/v84/github"
)

// GitHub is the GitHub provider.
type GitHub struct{ c *github.Client }

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

// PullRequest implements Forge: state, checks on the head (check runs and commit statuses, with
// the base branch's required checks marked), and the latest review per reviewer.
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
	required := g.required(ctx, o, r, pr.GetBase().GetRef())

	runs, _, err := g.c.Checks.ListCheckRunsForRef(ctx, o, r, p.HeadSHA, &github.ListCheckRunsOptions{ListOptions: github.ListOptions{PerPage: 100}})
	if err != nil {
		return nil, fmt.Errorf("check runs: %w", err)
	}
	for _, cr := range runs.CheckRuns {
		p.Checks = append(p.Checks, facts.Check{Name: cr.GetName(), Status: cr.GetStatus(), Conclusion: cr.GetConclusion(), Required: required[cr.GetName()]})
	}
	st, _, err := g.c.Repositories.GetCombinedStatus(ctx, o, r, p.HeadSHA, nil)
	if err != nil {
		return nil, fmt.Errorf("statuses: %w", err)
	}
	for _, s := range st.Statuses {
		c := facts.Check{Name: s.GetContext(), Status: "completed", Conclusion: s.GetState(), Required: required[s.GetContext()]}
		switch s.GetState() {
		case "pending":
			c.Status, c.Conclusion = "in_progress", ""
		case "error":
			c.Conclusion = "failure"
		}
		p.Checks = append(p.Checks, c)
	}
	slices.SortFunc(p.Checks, func(a, b facts.Check) int { return strings.Compare(a.Name, b.Name) })

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

// required returns the base branch's required checks, or none if it is unprotected or the token
// cannot read its protection.
func (g *GitHub) required(ctx context.Context, o, r, branch string) map[string]bool {
	out := map[string]bool{}
	rs, _, err := g.c.Repositories.GetRequiredStatusChecks(ctx, o, r, branch)
	if err != nil {
		return out
	}
	for _, c := range rs.GetContexts() {
		out[c] = true
	}
	for _, c := range rs.GetChecks() {
		out[c.Context] = true
	}
	return out
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
