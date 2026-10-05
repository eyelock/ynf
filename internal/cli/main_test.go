package cli

import (
	"os"
	"strings"
	"testing"
)

// TestMain keeps the tests off the real telemetry: a developer's laptop spool, or an OTEL_*
// setting in their shell, must never receive what the tests run. Tests that want a spool set their
// own with t.Setenv.
func TestMain(m *testing.M) {
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); k == "YNR_SPOOL" || strings.HasPrefix(k, "OTEL_") || strings.HasPrefix(k, "TRACE") {
			_ = os.Unsetenv(k)
		}
	}
	dir, err := os.MkdirTemp("", "ynf-cli-test-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("XDG_STATE_HOME", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
