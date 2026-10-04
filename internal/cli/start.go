package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/eyelock/ynf/internal/engine"
	"github.com/eyelock/ynf/internal/item"
	"github.com/eyelock/ynf/internal/tracker"
)

// start is an instruction (ADR-003, ADR-011): take on one item now. Run from a terminal it is
// attended, so the uncontained process executor is allowed and --auto-approve may switch off the
// agent's approval prompts on this machine; --detach only records it for a running ynf serve.
func (a *app) start(ctx context.Context, args []string) error {
	var refArg string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		refArg, args = args[0], args[1:]
	}
	fs := a.flags("start")
	prompt := fs.String("prompt", "", "")
	var labels multi
	fs.Var(&labels, "label", "")
	repo := fs.String("repo", "", "")
	lane := fs.String("lane", "", "")
	approve := fs.String("auto-approve", "", "")
	detach := fs.Bool("detach", false, "")
	if err := fs.Parse(args); err != nil {
		return withCode(ExitUsage, err)
	}
	if refArg == "" && fs.NArg() == 1 {
		refArg = fs.Arg(0)
	} else if fs.NArg() > 0 {
		return withCode(ExitUsage, fmt.Errorf("start takes one reference, not %v", fs.Args()))
	}
	if (refArg == "") == (*prompt == "") {
		return withCode(ExitUsage, errors.New("start needs a reference or --prompt <text>, not both"))
	}
	switch *approve {
	case "", "edits", "all":
	default:
		return withCode(ExitUsage, fmt.Errorf("--auto-approve %q: want edits or all", *approve))
	}
	if *approve != "" && *detach {
		return withCode(ExitUsage, errors.New("--auto-approve is for work run here, not --detach"))
	}
	if !*detach {
		a.interactive = true // a person at a terminal started it (ADR-007)
	}
	e, err := a.engine()
	if err != nil {
		return err
	}
	e.HostAutoApprove = *approve
	req := engine.StartRequest{Prompt: *prompt, Labels: labels, Lane: *lane, Detach: *detach}
	if req.Repo, err = forgeRepo(*repo, e.ForgeHost); err != nil {
		return withCode(ExitStartRefused, err)
	}
	if refArg != "" {
		if req.Ref, err = parseRef(refArg, e.ForgeHost, trackerNames(ctx, e)); err != nil {
			return withCode(ExitUsage, err)
		}
	}
	it, err := e.Start(ctx, req)
	var refused *engine.RefusedError
	if errors.As(err, &refused) {
		return withCode(ExitStartRefused, err)
	}
	if err != nil {
		return err
	}
	return a.out(it, startSummary(it, *detach))
}

// forgeRepo reads --repo: owner/name on the configured forge, or host/owner/name naming it.
func forgeRepo(s, host string) (string, error) {
	switch parts := strings.Split(s, "/"); {
	case s == "":
		return "", nil
	case len(parts) == 2 && parts[0] != "" && parts[1] != "":
		return s, nil
	case len(parts) == 3 && parts[1] != "" && parts[2] != "":
		if parts[0] != host {
			return "", fmt.Errorf("--repo %s is on %s; this ynf works with the forge at %s", s, parts[0], host)
		}
		return parts[1] + "/" + parts[2], nil
	}
	return "", fmt.Errorf("--repo %q is not owner/name or host/owner/name", s)
}

func startSummary(it item.Item, detached bool) string {
	if detached {
		return fmt.Sprintf("%s: recorded in lane %s (%s); a running ynf serve takes it on", it.Ref(), it.Lane, it.Key)
	}
	s := fmt.Sprintf("%s: %s in lane %s", it.Ref(), it.State, it.Lane)
	if it.Reason != "" {
		s += " (" + it.Reason + ")"
	}
	if it.PR > 0 {
		s += fmt.Sprintf("\npull request: https://%s/%s/pull/%d", it.Forge, it.Repo, it.PR)
	}
	return s
}

// startBody is POST /start's request: the same instruction as the command.
type startBody struct {
	Ref    string   `json:"ref"`
	Prompt string   `json:"prompt"`
	Labels []string `json:"labels"`
	Repo   string   `json:"repo"`
	Lane   string   `json:"lane"`
}

// startRequest turns POST /start's body into an instruction, always detached: the worker's loop
// steps it, and the caller gets the item at once.
func startRequest(e *engine.Engine, body []byte) (engine.StartRequest, error) {
	var b startBody
	if err := json.Unmarshal(body, &b); err != nil {
		return engine.StartRequest{}, fmt.Errorf("start: %w", err)
	}
	req := engine.StartRequest{Prompt: b.Prompt, Labels: b.Labels, Lane: b.Lane, Detach: true}
	var err error
	if req.Repo, err = forgeRepo(b.Repo, e.ForgeHost); err != nil {
		return req, err
	}
	if b.Ref != "" {
		var ref tracker.Ref
		if ref, err = parseRef(b.Ref, e.ForgeHost, trackerNames(context.Background(), e)); err != nil {
			return req, err
		}
		req.Ref = ref
	}
	return req, nil
}
