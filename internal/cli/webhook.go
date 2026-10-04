package cli

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/eyelock/ynf/internal/engine"
	"github.com/eyelock/ynf/internal/store"
)

// handle handles one GitHub webhook event from a file: the CI-native host (ADR-009), where the
// workflow's own trigger is the intake. GITHUB_EVENT_PATH and GITHUB_EVENT_NAME are the fallbacks.
func (a *app) handle(ctx context.Context, args []string) error {
	fs := a.flags("handle")
	path := fs.String("github-event", os.Getenv("GITHUB_EVENT_PATH"), "")
	name := fs.String("github-event-name", os.Getenv("GITHUB_EVENT_NAME"), "")
	if err := fs.Parse(args); err != nil {
		return withCode(ExitUsage, err)
	}
	if *path == "" || *name == "" {
		return withCode(ExitUsage, errors.New("handle needs --github-event <file> and --github-event-name <name> (or GITHUB_EVENT_PATH and GITHUB_EVENT_NAME)"))
	}
	body, err := os.ReadFile(*path)
	if err != nil {
		return withCode(ExitUsage, err)
	}
	e, err := a.engine()
	if err != nil {
		return err
	}
	if *name == "schedule" || *name == "workflow_dispatch" {
		// A scheduled run is the reconciliation sweep (ADR-003).
		if err := e.Sweep(ctx); err != nil {
			e.Log.Error("sweep", "err", err)
		}
		if _, err := e.RunDue(ctx); err != nil {
			return err
		}
		return a.summary(ctx)
	}
	touched, err := e.HandleGitHubEvent(ctx, *name, body)
	if err != nil {
		return err
	}
	return a.out(touched, fmt.Sprintf("%s on %s: issues %v, pull requests %v", *name, touched.Repo, touched.Issues, touched.PRs))
}

// webhooks receives GitHub webhooks for serve: verified with the shared secret before anything
// reads them, de-duplicated by delivery id, answered at once and handled in order by a worker.
type webhooks struct {
	e      *engine.Engine
	secret string
	// startToken enables POST /start, which starts work, for callers that present it; without
	// one the endpoint does not exist.
	startToken string
	queue      chan delivery
}

type delivery struct {
	name string
	body []byte
}

func (w *webhooks) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/healthz":
		_, _ = io.WriteString(rw, "ok\n")
		return
	case "/webhook/github":
	case "/start":
		w.start(rw, r)
		return
	default:
		http.NotFound(rw, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(rw, "POST only", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(rw, r.Body, 25<<20))
	if err != nil {
		http.Error(rw, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	if err := engine.VerifyGitHubSignature(w.secret, r.Header.Get("X-Hub-Signature-256"), body); err != nil {
		http.Error(rw, err.Error(), http.StatusUnauthorized)
		return
	}
	name, id := r.Header.Get("X-GitHub-Event"), r.Header.Get("X-GitHub-Delivery")
	if id != "" {
		if _, err := w.e.Store.Put(r.Context(), "seen/github/"+id, []byte("{}"), ""); errors.Is(err, store.ErrConflict) {
			_, _ = io.WriteString(rw, "duplicate\n")
			return
		}
	}
	select {
	case w.queue <- delivery{name, body}:
		rw.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(rw, "queued\n")
	default:
		http.Error(rw, "busy; the next sweep will catch up", http.StatusServiceUnavailable)
	}
}

// start is POST /start: an instruction, recorded for this worker's loop to step (ADR-003).
func (w *webhooks) start(rw http.ResponseWriter, r *http.Request) {
	if w.startToken == "" {
		http.NotFound(rw, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(rw, "POST only", http.StatusMethodNotAllowed)
		return
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(got), []byte(w.startToken)) != 1 {
		http.Error(rw, "a valid bearer token is required", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(rw, r.Body, 1<<20))
	if err != nil {
		http.Error(rw, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	req, err := startRequest(w.e, body)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	it, err := w.e.Start(r.Context(), req)
	var refused *engine.RefusedError
	switch {
	case errors.As(err, &refused):
		http.Error(rw, err.Error(), http.StatusUnprocessableEntity)
		return
	case err != nil:
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(rw).Encode(map[string]string{"item": it.Key, "ref": it.Ref(), "lane": it.Lane})
}

func (w *webhooks) work(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case d := <-w.queue:
			if _, err := w.e.HandleGitHubEvent(ctx, d.name, d.body); err != nil {
				w.e.Log.Warn("webhook", "event", d.name, "err", err)
			}
		}
	}
}

// listen starts the webhook receiver. It refuses to start without a secret: an endpoint that
// accepts unsigned events would let anyone drive the factory.
func (a *app) listen(ctx context.Context, e *engine.Engine, addr, secretEnv, startTokenEnv string) (string, error) {
	secret := os.Getenv(secretEnv)
	if secret == "" {
		return "", withCode(ExitPolicy, fmt.Errorf("--listen needs a webhook secret in %s", secretEnv))
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}
	w := &webhooks{e: e, secret: secret, startToken: os.Getenv(startTokenEnv), queue: make(chan delivery, 256)}
	srv := &http.Server{Handler: w, ReadHeaderTimeout: 10 * time.Second}
	go w.work(ctx)
	go func() { _ = srv.Serve(ln) }()
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	return ln.Addr().String(), nil
}
