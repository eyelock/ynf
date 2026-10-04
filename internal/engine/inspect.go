package engine

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/eyelock/ynf/internal/facts"
	"github.com/eyelock/ynf/internal/runner"
	"github.com/eyelock/ynf/internal/tracker"
)

// Connection is a forge or tracker the factory works with, and whether ynf can reach it.
type Connection struct {
	Kind     string `json:"kind"` // forge or tracker
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Host     string `json:"host"`
	OK       bool   `json:"ok"`
	Detail   string `json:"detail"`
}

// Checker is a tracker that can check itself without reading a ticket, such as an MCP tracker
// confirming its server has the tools it is configured to call.
type Checker interface {
	Check(ctx context.Context) error
}

// Connections lists the forges and trackers ynf works with, each checked (ADR-003, ADR-011): a
// forge by reading an enrolled repository on it, a GitHub tracker with its forge, and a tracker
// that can check itself by doing so.
func (e *Engine) Connections(ctx context.Context) ([]Connection, error) {
	fp, err := e.Factory(ctx)
	if err != nil {
		return nil, err
	}
	enrolled, err := e.Enrolled(ctx)
	if err != nil {
		return nil, err
	}
	reach := func(host string) (bool, string) {
		for _, r := range enrolled {
			if h, name := e.splitRepo(r); h == host {
				fg, _, err := e.forgeFor(r)
				if err == nil {
					_, err = fg.DefaultBranch(ctx, name)
				}
				if err != nil {
					return false, fmt.Sprintf("%s: %v", r, err)
				}
				return true, "reached " + r
			}
		}
		return true, "no enrolled repository here to check"
	}
	e.mu.Lock()
	forgeNames := maps.Clone(e.forgeNames)
	trackerHosts := maps.Clone(e.trackerHosts)
	e.mu.Unlock()
	providerOf := func(m map[string]map[string]any, name string) string {
		p, _ := m[name]["provider"].(string)
		return p
	}
	var out []Connection
	forges := map[string]bool{e.forgeHost(): true}
	ok, detail := reach(e.forgeHost())
	out = append(out, Connection{Kind: "forge", Name: "default", Provider: "github", Host: e.forgeHost(), OK: ok, Detail: detail})
	for _, name := range slices.Sorted(maps.Keys(forgeNames)) {
		host := forgeNames[name]
		forges[host] = true
		ok, detail := reach(host)
		out = append(out, Connection{Kind: "forge", Name: name, Provider: providerOf(fp.File.Forges, name), Host: host, OK: ok, Detail: detail})
	}
	// Every forge's issues are a tracker too.
	for _, c := range slices.Clone(out) {
		out = append(out, Connection{Kind: "tracker", Name: c.Name, Provider: "github", Host: c.Host, OK: c.OK, Detail: "its forge's issues"})
	}
	for _, name := range slices.Sorted(maps.Keys(trackerHosts)) {
		host := trackerHosts[name]
		c := Connection{Kind: "tracker", Name: name, Provider: providerOf(fp.File.Trackers, name), Host: host, OK: true, Detail: "configured"}
		tr, err := e.tracker(tracker.Ref{Host: host})
		switch {
		case err != nil:
			c.OK, c.Detail = false, err.Error()
		default:
			if ch, ok := tr.(Checker); ok {
				if err := ch.Check(ctx); err != nil {
					c.OK, c.Detail = false, err.Error()
				} else {
					c.Detail = "its server has the tools it is configured to call"
				}
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// Ticket reads a ticket through its tracker, as start would, without starting anything: for
// checking a tracker is set up before work depends on it.
func (e *Engine) Ticket(ctx context.Context, ref tracker.Ref) (facts.Ticket, tracker.Text, error) {
	tr, err := e.tracker(ref)
	if err != nil {
		return facts.Ticket{}, tracker.Text{}, err
	}
	return tr.Get(ctx, ref.Key)
}

// LaneRun is how a lane's runs are made, and the harness it is held to when ynf can read it.
type LaneRun struct {
	Lane     string          `json:"lane"`
	Kind     string          `json:"kind"`
	On       bool            `json:"on"`
	Runner   string          `json:"runner"`
	Executor string          `json:"executor"`
	Image    string          `json:"image,omitempty"`
	Harness  string          `json:"harness,omitempty"` // what the lane names
	Model    string          `json:"model,omitempty"`   // the model the lane pins; empty is the vendor's default
	Where    string          `json:"where"`             // where the harness comes from
	Read     *runner.Harness `json:"read,omitempty"`    // the harness, when it could be read
	Problem  string          `json:"problem,omitempty"` // why it could not be read, or does not fit the lane
}

// LaneRuns describes how each of a repository's lanes runs (ADR-006, ADR-012). A harness in a
// published image, or installed beside an inline ynf, is read now and checked against the lane:
// its budgets may only be tightened, and its sensors only scoped. A harness carried in the
// repository is read when a run checks it out.
func (e *Engine) LaneRuns(ctx context.Context, repo string) ([]LaneRun, error) {
	rp, err := e.Policy(ctx, repo)
	if err != nil {
		return nil, err
	}
	var out []LaneRun
	for _, name := range rp.File.Names() {
		l := rp.File.Lanes[name]
		lr := LaneRun{Lane: name, Kind: l.Kind, On: l.On(), Runner: l.Run.Runner, Executor: l.Run.Executor, Image: l.Run.Image}
		if lr.Kind == "" {
			lr.Kind = "originate"
		}
		ex, err := e.Executor(l.Run.Executor)
		if err != nil {
			lr.Problem = err.Error()
			out = append(out, lr)
			continue
		}
		lr.Executor = ex.Name()
		if l.Run.Ynh == nil {
			lr.Where = "a command, not a harness"
			out = append(out, lr)
			continue
		}
		lr.Harness, lr.Model = l.Run.Ynh.Harness, l.Run.Ynh.Model
		var image string
		switch {
		case ex.Name() == "inline":
			lr.Where = "installed beside ynf, in this image"
		case l.Run.Image != "":
			lr.Where, image = "in the image "+l.Run.Image, l.Run.Image
		default:
			lr.Where = "carried in the repository at " + lr.Harness + ", read when a run checks it out"
			out = append(out, lr)
			continue
		}
		if e.ImageHarness == nil {
			lr.Problem = "ynf cannot read it here: ynh is not available"
			out = append(out, lr)
			continue
		}
		want := lr.Harness
		if want == "." {
			want = ""
		}
		h, err := e.ImageHarness(ctx, image, want)
		switch {
		case err != nil:
			lr.Problem = err.Error()
		default:
			lr.Read = &h
			if err := h.CheckLane(*l.Run.Ynh); err != nil {
				lr.Problem = err.Error()
			}
		}
		out = append(out, lr)
	}
	return out, nil
}
