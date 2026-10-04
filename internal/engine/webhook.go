package engine

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/eyelock/ynf/internal/event"
	"github.com/eyelock/ynf/internal/item"
)

// VerifyGitHubSignature checks a webhook's X-Hub-Signature-256 against the shared secret. It runs
// before anything else reads the payload (ADR-003: verify, then shrink, at the edge).
func VerifyGitHubSignature(secret, header string, body []byte) error {
	sig, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return errors.New("missing or malformed X-Hub-Signature-256")
	}
	want, err := hex.DecodeString(sig)
	if err != nil {
		return errors.New("malformed X-Hub-Signature-256")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	if !hmac.Equal(mac.Sum(nil), want) {
		return errors.New("webhook signature does not match")
	}
	return nil
}

// githubPayload is the little of a GitHub webhook ynf reads: which repository, and which issue or
// pull request. Everything else is re-probed from the API (ADR-003: webhooks are hints).
type githubPayload struct {
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Issue *struct {
		Number      int  `json:"number"`
		PullRequest *any `json:"pull_request"`
	} `json:"issue"`
	PullRequest *struct {
		Number int `json:"number"`
	} `json:"pull_request"`
	CheckSuite *struct {
		PullRequests []struct {
			Number int `json:"number"`
		} `json:"pull_requests"`
	} `json:"check_suite"`
	CheckRun *struct {
		PullRequests []struct {
			Number int `json:"number"`
		} `json:"pull_requests"`
	} `json:"check_run"`
}

// Touched is what a webhook is about.
type Touched struct {
	Repo   string `json:"repo"`
	Issues []int  `json:"issues,omitempty"`
	PRs    []int  `json:"prs,omitempty"`
}

// ParseGitHubEvent reads which issues and pull requests a GitHub webhook touches.
func ParseGitHubEvent(name string, body []byte) (Touched, error) {
	var p githubPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return Touched{}, fmt.Errorf("%s payload: %w", name, err)
	}
	t := Touched{Repo: p.Repository.FullName}
	if t.Repo == "" {
		return t, fmt.Errorf("%s payload has no repository", name)
	}
	switch {
	case p.Issue != nil && p.Issue.PullRequest != nil:
		t.PRs = append(t.PRs, p.Issue.Number) // a comment on a pull request
	case p.Issue != nil:
		t.Issues = append(t.Issues, p.Issue.Number)
	case p.PullRequest != nil:
		t.PRs = append(t.PRs, p.PullRequest.Number)
	case p.CheckSuite != nil:
		for _, pr := range p.CheckSuite.PullRequests {
			t.PRs = append(t.PRs, pr.Number)
		}
	case p.CheckRun != nil:
		for _, pr := range p.CheckRun.PullRequests {
			t.PRs = append(t.PRs, pr.Number)
		}
	}
	return t, nil
}

// HandleGitHubEvent steps every item a webhook touches. An enrolled repository's new tickets are
// found by sweeping it, since only a lane's own search says whether a ticket belongs to it; a
// tracked item is stepped at once, with facts probed fresh.
func (e *Engine) HandleGitHubEvent(ctx context.Context, name string, body []byte) (Touched, error) {
	t, err := ParseGitHubEvent(name, body)
	if err != nil {
		return t, err
	}
	enrolled, err := e.Enrolled(ctx)
	if err != nil {
		return t, err
	}
	if !slices.Contains(enrolled, t.Repo) {
		return t, fmt.Errorf("%s is not enrolled", t.Repo)
	}
	items, err := e.allItems(ctx)
	if err != nil {
		return t, err
	}
	var keys []string
	for _, it := range items {
		if it.Repo != t.Repo || !e.wantLane(it.Lane) {
			continue
		}
		if slices.Contains(t.Issues, it.Number) && it.Kind != "adopt" || slices.Contains(t.PRs, it.PR) && it.PR > 0 {
			keys = append(keys, it.Key)
		}
	}
	var errs []error
	if len(t.Issues) > 0 || len(t.PRs) > 0 {
		errs = append(errs, e.SweepRepos(ctx, []string{t.Repo}))
	}
	for _, k := range keys {
		it, _, err := loadItem(ctx, e, k)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		ev := event.New(e.NewID(), "github/"+name, event.ForgeChanged, it.Subject(), e.Now(), map[string]any{"github_event": name})
		errs = append(errs, e.Handle(ctx, k, ev))
	}
	return t, errors.Join(errs...)
}

func loadItem(ctx context.Context, e *Engine, key string) (item.Item, string, error) {
	doc, v, err := e.Store.Get(ctx, key)
	if err != nil {
		return item.Item{}, "", err
	}
	var it item.Item
	return it, v, json.Unmarshal(doc, &it)
}
