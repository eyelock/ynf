package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/eyelock/ynf/internal/egress"
)

// egressProxy runs the allow-list proxy (ADR-007). The docker executor starts it in its own
// container, attached to the run's internal network; it is not meant to be run by hand.
func (a *app) egressProxy(ctx context.Context, args []string) error {
	fs := a.flags("egress-proxy")
	listen := fs.String("listen", ":3128", "")
	allow := fs.String("allow", "", "")
	logPath := fs.String("log", "", "")
	if err := fs.Parse(args); err != nil {
		return withCode(ExitUsage, err)
	}
	p := &egress.Proxy{}
	for h := range strings.SplitSeq(*allow, ",") {
		if h = strings.TrimSpace(h); h != "" {
			p.Allow = append(p.Allow, h)
		}
	}
	if *logPath != "" {
		f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		p.Log = f
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: p, ReadHeaderTimeout: 30 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	_, _ = fmt.Fprintf(a.stdout, "ynf egress proxy listening on %s, allowing %s\n", ln.Addr(), strings.Join(p.Allow, ", "))
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
