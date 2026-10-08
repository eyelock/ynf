package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/eyelock/ynf/internal/config"
	"github.com/eyelock/ynf/internal/memory"
)

// memoryFor builds memory from config: ynm when configured, or detected when not; nothing when
// switched off or absent (ADR-008, ADR-012). Over http it is a hosted ynm, the shared store for a
// pool or CI; it also returns the level ynf writes at.
func memoryFor(c *config.Config) (memory.Memory, func(string) string, string, error) {
	enabled, template, cwd := c.MemorySettings()
	transport, endpoint, tokenEnv, level := c.MemoryTransport()
	ns := func(repo string) string { return strings.ReplaceAll(template, "{repo}", repo) }
	switch {
	case enabled != nil && !*enabled:
		return nil, ns, level, nil
	case transport == "http":
		if endpoint == "" {
			return nil, ns, level, fmt.Errorf("memory.transport http needs memory.endpoint")
		}
		token := os.Getenv(tokenEnv)
		if tokenEnv == "" || token == "" {
			return nil, ns, level, fmt.Errorf("memory.transport http needs a token: set memory.token_env and that variable")
		}
		return &memory.YnmHTTP{Endpoint: endpoint, Token: token}, ns, level, nil
	case enabled != nil:
		return memory.Ynm{Cwd: cwd}, ns, level, nil
	}
	m, _ := memory.Detect(cwd)
	return m, ns, level, nil
}
