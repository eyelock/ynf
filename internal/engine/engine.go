// Package engine is ynf's outer loop (ADR-001): one idempotent step per event — correlate, claim,
// probe, decide, act, record, release — plus the sweep that turns searches and due timers into
// events. It holds nothing in memory that matters between steps (NFR-2), so any host can run it.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/eyelock/ynf/internal/decide"
	"github.com/eyelock/ynf/internal/event"
	"github.com/eyelock/ynf/internal/executor"
	"github.com/eyelock/ynf/internal/forge"
	"github.com/eyelock/ynf/internal/item"
	"github.com/eyelock/ynf/internal/lease"
	"github.com/eyelock/ynf/internal/memory"
	"github.com/eyelock/ynf/internal/policy"
	"github.com/eyelock/ynf/internal/runner"
	"github.com/eyelock/ynf/internal/store"
	"github.com/eyelock/ynf/internal/tracker"
)

// Git is what the engine needs from the workspace.
type Git interface {
	Mirror(ctx context.Context, repo string) (string, error)
	Checkout(ctx context.Context, mirror, ref, dir string) (string, error)
	RemoveCheckout(dir string) error
	RemoteHas(ctx context.Context, mirror, branch string) bool
	Changed(ctx context.Context, wt string) ([]string, error)
	Commit(ctx context.Context, wt, message string) (string, error)
	Push(ctx context.Context, wt, repo, branch string) error
	Head(ctx context.Context, wt string) (string, error)
	RemoteSHA(ctx context.Context, mirror, branch string) (string, error)
	PushFastForward(ctx context.Context, wt, repo, branch string) error
}

// Engine runs steps.
type Engine struct {
	Store store.Store
	Forge forge.Forge
	// ForgeHost is Forge's host as item keys name it; default github.com.
	ForgeHost string
	// Forges are the forge instances besides Forge, by host, such as a GitHub Enterprise Server
	// (ADR-003). A repository on one is named host/owner/name; one on Forge, owner/name.
	Forges map[string]ForgeInstance
	// NewForge builds a forge instance the configuration repository declares; nil allows none.
	NewForge func(name string, cfg map[string]any) (ForgeInstance, error)
	// NewTracker builds a tracker instance the configuration repository declares, returning its
	// host; nil allows none.
	NewTracker func(name string, cfg map[string]any) (string, tracker.Tracker, error)
	// ConfigRepo is the factory's configuration repository (owner/name on the forge), whose
	// factory.yaml enrols repositories and whose lanes.yaml every enrolled repository's lanes
	// are laid over (ADR-006). Empty: Repos is the enrolment and each repository's lanes stand
	// alone.
	ConfigRepo string
	// Trackers are the tracker instances by host (ADR-003).
	Trackers map[string]tracker.Tracker
	Git      Git
	Executor func(name string) (executor.Executor, error)

	Repos       []string // enrolled repositories
	Lanes       []string // only these lanes; empty means all
	WorkDir     string
	Owner       string
	LeaseTTL    time.Duration
	Heartbeat   time.Duration
	Poll        decide.Poll
	RunTimeout  time.Duration
	Interactive bool // allows the process executor (ADR-007)

	// BuildImage builds a ynh agent image for a harness in a worktree (`ynh image --entrypoint
	// agent`) and returns its tag. Nil means ynh is not available (ADR-012).
	BuildImage func(ctx context.Context, worktree string, cfg policy.Ynh) (string, error)
	// HostAutoApprove is --auto-approve asked for by the person at the terminal (ynf start), for
	// runs on the host only; HostCapabilities reports the host ynh's capabilities to check it.
	HostAutoApprove  string
	HostCapabilities func(ctx context.Context) (string, error)
	// ImageHarness reads what the harness inside an agent image declares, by asking the image's
	// own ynh; harness picks one when the image carries several. nil skips reading it.
	ImageHarness func(ctx context.Context, image, harness string) (runner.Harness, error)
	// ImageCapabilities reports the capabilities version of the ynh inside an agent image; nil
	// skips the check.
	ImageCapabilities func(ctx context.Context, image string) (string, error)
	// Getenv reads the variables a lane passes into its runs (run.env). Default os.Getenv.
	Getenv func(string) string

	// Memory is ynm when it is configured or detected, else nil (ADR-008). It is advisory: what it
	// holds goes into a run's task and to people, never into a decision.
	Memory memory.Memory
	// MemoryNamespace is the namespace for a repository; default factory/<owner>/<name>.
	MemoryNamespace func(repo string) string
	// MemoryLevel is the ynm level ynf writes at: empty or personal for one person's store,
	// distributed for a shared one (ADR-008).
	MemoryLevel string
	// ProgressEvery is how often a run in progress is logged; default 30s, negative for never.
	ProgressEvery time.Duration

	Now   func() time.Time
	NewID func() string
	Log   *slog.Logger

	mu       sync.Mutex
	policies map[string]*RepoPolicy
	factory  *FactoryPolicy
	// trackerHosts maps a configured tracker's name to its host, for shorthand references.
	trackerHosts map[string]string
	// forgeNames maps a declared forge's name to its host, for listing.
	forgeNames map[string]string
}

// RepoPolicy is a repository's lane policy, read from its default branch.
type RepoPolicy struct {
	Repo     string
	Base     string // default branch
	SHA      string // the commit it was read at
	Dir      string // the factory folder it came from; empty when the repository has no lanes of its own
	Shadowed []string
	File     *policy.File
	// Config is the configuration repository's policy the lanes were laid over, if any; Own is
	// the repository's own lanes.yaml, for telling which layer set a value.
	Config *FactoryPolicy
	Own    []byte
}

// FactoryPolicy is the configuration repository's: its factory.yaml and default lanes, read at a
// resolved commit.
type FactoryPolicy struct {
	Repo  string
	SHA   string
	Dir   string
	File  *policy.Factory
	Lanes []byte // its lanes.yaml; empty when it has none
}

func (e *Engine) log() *slog.Logger {
	if e.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return e.Log
}

// ResetPolicies forgets cached policy, so the next step reads each repository's lanes afresh.
func (e *Engine) ResetPolicies() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.policies = nil
	e.factory = nil
}

// Factory reads the configuration repository at its default branch's head, once until
// ResetPolicies; nil when there is none.
func (e *Engine) Factory(ctx context.Context) (*FactoryPolicy, error) {
	if e.ConfigRepo == "" {
		return nil, nil
	}
	e.mu.Lock()
	if e.factory != nil {
		defer e.mu.Unlock()
		return e.factory, nil
	}
	e.mu.Unlock()
	sha, files, dir, _, err := e.readFactoryFolder(ctx, e.ConfigRepo, policy.FactoryFile, policy.LanesFile)
	if err != nil {
		return nil, fmt.Errorf("the configuration repository %s: %w", e.ConfigRepo, err)
	}
	if files[policy.FactoryFile] == nil {
		return nil, fmt.Errorf("the configuration repository %s has no %s in any of %v", e.ConfigRepo, policy.FactoryFile, policy.FactoryDirs)
	}
	f, err := policy.LoadFactory(files[policy.FactoryFile])
	if err != nil {
		return nil, fmt.Errorf("%s/%s: %w", e.ConfigRepo, dir, err)
	}
	if err := e.addForges(f); err != nil {
		return nil, fmt.Errorf("%s: %w", e.ConfigRepo, err)
	}
	if err := e.addTrackers(f); err != nil {
		return nil, fmt.Errorf("%s: %w", e.ConfigRepo, err)
	}
	fp := &FactoryPolicy{Repo: e.ConfigRepo, SHA: sha, Dir: dir, File: f, Lanes: files[policy.LanesFile]}
	e.mu.Lock()
	e.factory = fp
	e.mu.Unlock()
	return fp, nil
}

// Enrolled is the repositories ynf works on: the configuration repository's enrolment, or Repos.
func (e *Engine) Enrolled(ctx context.Context) ([]string, error) {
	fp, err := e.Factory(ctx)
	if err != nil || fp == nil {
		return e.Repos, err
	}
	out := make([]string, 0, len(fp.File.Repos))
	for _, r := range fp.File.Repos {
		host, name := e.splitRepo(r)
		r = e.qualify(host, name)
		if _, _, err := e.forgeFor(r); err != nil {
			return nil, fmt.Errorf("%s enrols %s: %w", e.ConfigRepo, r, err)
		}
		out = append(out, r)
	}
	return out, nil
}

// readFactoryFolder reads names from the first factory folder holding any of them, on repo's
// default branch at its head commit.
func (e *Engine) readFactoryFolder(ctx context.Context, repo string, names ...string) (sha string, files map[string][]byte, dir string, shadowed []string, err error) {
	fg, name, err := e.forgeFor(repo)
	if err != nil {
		return "", nil, "", nil, err
	}
	base, err := fg.DefaultBranch(ctx, name)
	if err != nil {
		return "", nil, "", nil, err
	}
	if sha, err = fg.Head(ctx, name, base); err != nil {
		return "", nil, "", nil, fmt.Errorf("%s's head: %w", base, err)
	}
	found := map[string]map[string][]byte{}
	for _, d := range policy.FactoryDirs {
		for _, n := range names {
			b, err := fg.File(ctx, name, sha, d+"/"+n)
			switch {
			case err == nil:
				if found[d] == nil {
					found[d] = map[string][]byte{}
				}
				found[d][n] = b
			case !errors.Is(err, forge.ErrNotFound):
				return "", nil, "", nil, err
			}
		}
	}
	dir, shadowed = policy.Resolve(func(d string) bool { return found[d] != nil })
	return sha, found[dir], dir, shadowed, nil
}

// Policy reads a repository's lanes from the first factory folder on its default branch, at its
// head commit, laid key by key over the configuration repository's lanes when there is one
// (ADR-006): the repository wins.
func (e *Engine) Policy(ctx context.Context, repo string) (*RepoPolicy, error) {
	e.mu.Lock()
	if rp, ok := e.policies[repo]; ok {
		e.mu.Unlock()
		return rp, nil
	}
	e.mu.Unlock()

	fp, err := e.Factory(ctx)
	if err != nil {
		return nil, err
	}
	sha, files, dir, shadowed, err := e.readFactoryFolder(ctx, repo, policy.LanesFile)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", repo, err)
	}
	own := files[policy.LanesFile]
	doc, err := layered(fp, own)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", repo, err)
	}
	if len(doc) == 0 {
		return nil, fmt.Errorf("%s has no %s in any of %v, and no configuration repository gives it lanes", repo, policy.LanesFile, policy.FactoryDirs)
	}
	f, err := policy.Load(doc)
	if err != nil {
		return nil, fmt.Errorf("%s/%s: %w", repo, dir, err)
	}
	for _, s := range shadowed {
		e.log().Warn("shadowed factory folder", "repo", repo, "used", dir, "shadowed", s)
	}
	fg, name, _ := e.forgeFor(repo)
	base, _ := fg.DefaultBranch(ctx, name)
	rp := &RepoPolicy{Repo: repo, Base: base, SHA: sha, Dir: dir, Shadowed: shadowed, File: f, Config: fp, Own: own}
	e.mu.Lock()
	if e.policies == nil {
		e.policies = map[string]*RepoPolicy{}
	}
	e.policies[repo] = rp
	e.mu.Unlock()
	return rp, nil
}

// layered lays own, a repository's lanes.yaml, over the configuration repository's lanes when
// there are any. The result is not yet validated.
func layered(fp *FactoryPolicy, own []byte) ([]byte, error) {
	if fp == nil || len(fp.Lanes) == 0 {
		return own, nil
	}
	return policy.MergeLanes(fp.Lanes, own)
}

// PolicyWithLayer is Policy for a repository's lanes.yaml given as bytes, such as a local file not
// yet pushed, instead of the one on the forge. It is laid over the configuration repository's
// lanes and validated as Policy does; nothing is cached. SHA and Dir are empty.
func (e *Engine) PolicyWithLayer(ctx context.Context, repo string, own []byte) (*RepoPolicy, error) {
	fp, err := e.Factory(ctx)
	if err != nil {
		return nil, err
	}
	doc, err := layered(fp, own)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", repo, err)
	}
	if len(doc) == 0 {
		return nil, fmt.Errorf("%s: the lanes file is empty, and no configuration repository gives it lanes", repo)
	}
	f, err := policy.Load(doc)
	if err != nil {
		return nil, err
	}
	return &RepoPolicy{Repo: repo, File: f, Config: fp, Own: own}, nil
}

// ItemRepo is the item's code repository, qualified as Policy takes it.
func (e *Engine) ItemRepo(it item.Item) string { return e.repoOf(it) }

func (e *Engine) laneFor(ctx context.Context, it item.Item) (*RepoPolicy, policy.Lane, error) {
	rp, err := e.Policy(ctx, e.repoOf(it))
	if err != nil {
		return nil, policy.Lane{}, err
	}
	l, ok := rp.File.Lanes[it.Lane]
	if !ok {
		off := false // a lane that no longer exists is a lane switched off
		return rp, policy.Lane{Name: it.Lane, Enabled: &off}, nil
	}
	return rp, l, nil
}

func (e *Engine) wantLane(name string) bool {
	return len(e.Lanes) == 0 || slices.Contains(e.Lanes, name)
}

// Sweep runs every enrolled repository's lane searches and starts a step for each new ticket.
func (e *Engine) Sweep(ctx context.Context) error {
	repos, err := e.Enrolled(ctx)
	if err != nil {
		return err
	}
	return e.SweepRepos(ctx, repos)
}

// SweepRepos is Sweep for some of the enrolled repositories.
func (e *Engine) SweepRepos(ctx context.Context, repos []string) error {
	var errs []error
	for _, repo := range repos {
		rp, err := e.Policy(ctx, repo)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		stats, err := e.Stats(ctx)
		if err != nil {
			errs = append(errs, err)
		}
		for _, name := range rp.File.Names() {
			if !e.wantLane(name) {
				continue
			}
			if err := e.checkStops(ctx, repo, rp.File.Lanes[name], stats); err != nil {
				errs = append(errs, err)
			}
			if err := e.sweepLane(ctx, repo, rp.File.Lanes[name]); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (e *Engine) sweepLane(ctx context.Context, repo string, lane policy.Lane) error {
	for _, in := range lane.Intake {
		if in.GitHubSearch == "" {
			e.log().Warn("intake not supported yet", "lane", lane.Name, "intake", in)
			continue
		}
		// The search runs on the repository's own forge.
		fg, name, err := e.forgeFor(repo)
		if err != nil {
			return err
		}
		host, _ := e.splitRepo(repo)
		hits, err := fg.Search(ctx, in.GitHubSearch)
		if err != nil {
			return err
		}
		for _, h := range hits {
			if h.Repo != name || h.IsPR != (lane.Kind == "adopt") {
				continue
			}
			if err := e.track(ctx, lane, host, h); err != nil {
				return err
			}
		}
	}
	return nil
}

// track creates the item for a new ticket and steps it; a ticket already tracked is left alone.
func (e *Engine) track(ctx context.Context, lane policy.Lane, host string, h forge.Hit) error {
	now := e.Now()
	// A GitHub issue's code goes to its own repository; an adopted pull request is its own ticket.
	ref := tracker.Ref{Host: host, Key: forge.IssueKey(h.Repo, h.Number)}
	it := item.Item{
		Key: item.Key(ref), Kind: lane.Kind, Lane: lane.Name, Ticket: ref,
		Forge: host, Repo: h.Repo, Number: h.Number, State: item.Intake, Created: now, Updated: now,
	}
	if lane.Kind == "adopt" {
		it.Key, it.PR = item.PRKey(host, h.Repo, h.Number), h.Number
	}
	switch err := lease.Create(ctx, e.Store, it); {
	case errors.Is(err, store.ErrConflict):
		return nil
	case err != nil:
		return err
	}
	e.log().Info("tracking", "item", it.Key, "lane", lane.Name)
	ev := event.New(e.NewID(), "ynf/search", event.TicketMatched, it.Subject(), now, map[string]any{"lane": lane.Name})
	return e.Handle(ctx, it.Key, ev)
}

// RunDue steps every item whose timer has passed, and returns how many it stepped.
func (e *Engine) RunDue(ctx context.Context) (int, error) {
	keys, err := e.Store.Due(ctx, e.Now(), 100)
	if err != nil {
		return 0, err
	}
	n := 0
	var errs []error
	for _, k := range keys {
		it, _, err := lease.Load(ctx, e.Store, k)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !e.wantLane(it.Lane) {
			continue
		}
		ev := event.New(e.NewID(), "ynf/timer", event.TimerDue, it.Subject(), e.Now(), nil)
		if err := e.Handle(ctx, k, ev); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", k, err))
		}
		n++
	}
	return n, errors.Join(errs...)
}

// Items returns every tracked item in the engine's lanes.
func (e *Engine) Items(ctx context.Context) ([]item.Item, error) {
	keys, err := e.Store.Keys(ctx, "item/")
	if err != nil {
		return nil, err
	}
	var out []item.Item
	for _, k := range keys {
		it, _, err := lease.Load(ctx, e.Store, k)
		if err != nil {
			return nil, err
		}
		if e.wantLane(it.Lane) {
			out = append(out, it)
		}
	}
	return out, nil
}

// Settled reports whether every tracked item is somewhere only a human or the outside world can
// move it from.
func (e *Engine) Settled(ctx context.Context) (bool, error) {
	items, err := e.Items(ctx)
	if err != nil {
		return false, err
	}
	for _, it := range items {
		if !it.State.Settled() {
			return false, nil
		}
	}
	return true, nil
}

func (e *Engine) forgeHost() string {
	if e.ForgeHost == "" {
		return "github.com"
	}
	return e.ForgeHost
}

// tracker is the tracker instance an item's ticket lives on. Ad hoc work's is ynf's own store.
func (e *Engine) tracker(t tracker.Ref) (tracker.Tracker, error) {
	e.mu.Lock()
	tr, ok := e.Trackers[t.Host]
	e.mu.Unlock()
	if ok {
		return tr, nil
	}
	if t.Host == AdhocHost {
		return AdhocTracker(e.Store), nil
	}
	return nil, fmt.Errorf("no tracker is configured for %s (%s)", t.Host, t)
}

// ForgeInstance is a forge besides the default: its client, the git workspace that clones from
// and pushes to it with its own token, and the tracker for its issues.
type ForgeInstance struct {
	Host    string
	Forge   forge.Forge
	Git     Git
	Tracker tracker.Tracker
}

// splitRepo reads a repository name: host/owner/name on another forge, owner/name on the default.
func (e *Engine) splitRepo(repo string) (host, name string) {
	if parts := strings.Split(repo, "/"); len(parts) == 3 {
		return parts[0], parts[1] + "/" + parts[2]
	}
	return e.forgeHost(), repo
}

// qualify names a repository on host: owner/name on the default forge, host/owner/name elsewhere.
func (e *Engine) qualify(host, name string) string {
	if host == "" || host == e.forgeHost() {
		return name
	}
	return host + "/" + name
}

// repoOf is the item's code repository, qualified.
func (e *Engine) repoOf(it item.Item) string { return e.qualify(it.Forge, it.Repo) }

// forgeFor is the forge a repository is on, and its owner/name there.
func (e *Engine) forgeFor(repo string) (forge.Forge, string, error) {
	host, name := e.splitRepo(repo)
	if host == e.forgeHost() {
		return e.Forge, name, nil
	}
	e.mu.Lock()
	inst, ok := e.Forges[host]
	e.mu.Unlock()
	if !ok {
		return nil, "", fmt.Errorf("%s is on %s, which is not a configured forge", repo, host)
	}
	return inst.Forge, name, nil
}

// gitFor is the git workspace for a repository's forge. It takes the qualified name, so mirrors
// of same-named repositories on two forges never share a folder.
func (e *Engine) gitFor(repo string) Git {
	host, _ := e.splitRepo(repo)
	e.mu.Lock()
	defer e.mu.Unlock()
	if inst, ok := e.Forges[host]; ok && host != e.forgeHost() {
		return inst.Git
	}
	return e.Git
}

// addForges registers the forge instances a configuration repository declares.
func (e *Engine) addForges(f *policy.Factory) error {
	for _, name := range slices.Sorted(maps.Keys(f.Forges)) {
		if e.NewForge == nil {
			return fmt.Errorf("forge %s: this ynf cannot add forges", name)
		}
		inst, err := e.NewForge(name, f.Forges[name])
		if err != nil {
			return fmt.Errorf("forge %s: %w", name, err)
		}
		if inst.Host == e.forgeHost() {
			continue // the default forge, declared for completeness
		}
		e.mu.Lock()
		if e.Forges == nil {
			e.Forges = map[string]ForgeInstance{}
		}
		e.Forges[inst.Host] = inst
		if e.forgeNames == nil {
			e.forgeNames = map[string]string{}
		}
		e.forgeNames[name] = inst.Host
		if e.Trackers == nil {
			e.Trackers = map[string]tracker.Tracker{}
		}
		if _, ok := e.Trackers[inst.Host]; !ok && inst.Tracker != nil {
			e.Trackers[inst.Host] = inst.Tracker
		}
		e.mu.Unlock()
	}
	return nil
}

// isForge reports whether host is a forge ynf works with.
func (e *Engine) isForge(host string) bool {
	if host == e.forgeHost() {
		return true
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.Forges[host]
	return ok
}

// addTrackers registers the tracker instances a configuration repository declares (ADR-003).
func (e *Engine) addTrackers(f *policy.Factory) error {
	for _, name := range slices.Sorted(maps.Keys(f.Trackers)) {
		if e.NewTracker == nil {
			return fmt.Errorf("tracker %s: this ynf cannot add trackers", name)
		}
		host, tr, err := e.NewTracker(name, f.Trackers[name])
		if err != nil {
			return fmt.Errorf("tracker %s: %w", name, err)
		}
		e.mu.Lock()
		if e.Trackers == nil {
			e.Trackers = map[string]tracker.Tracker{}
		}
		e.Trackers[host] = tr
		if e.trackerHosts == nil {
			e.trackerHosts = map[string]string{}
		}
		e.trackerHosts[name] = host
		e.mu.Unlock()
	}
	return nil
}

// TrackerHost is the host of the tracker the configuration repository names name, so a
// reference can use the name (jira/PLAT-881) and still be stored by host.
func (e *Engine) TrackerHost(ctx context.Context, name string) (string, error) {
	if _, err := e.Factory(ctx); err != nil {
		return "", err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if host, ok := e.trackerHosts[name]; ok {
		return host, nil
	}
	return "", fmt.Errorf("no tracker is configured as %q", name)
}

// CloseTrackers ends the trackers that hold a session, such as an MCP server ynf started.
func (e *Engine) CloseTrackers() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, tr := range e.Trackers {
		if c, ok := tr.(interface{ Close() error }); ok {
			_ = c.Close()
		}
	}
}
