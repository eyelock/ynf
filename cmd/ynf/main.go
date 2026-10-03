// Command ynf is your named factory: the outer loop around agent runs.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/eyelock/ynf/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
