// Command fakeclaude is the vendor CLI the full-chain check puts on PATH as claude (.ynr/fullchain).
//
// ynh drives Claude Code as a stream-json session: flags such as --input-format and --verbose, a
// control request or two, then a user message in and events out until a result. ynr's stub vendor
// (v0.2.0) is a one-shot program and does not speak that, so this adapter does, and for each turn
// it runs ynr-stub-vendor, found on PATH, with the environment ynh gave it. The vendor's telemetry
// (spans, metrics and events to the relay, in the trace TRACEPARENT names) is therefore the stub's,
// not invented here; only the session's wire format is. It uses no model and no network.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
)

func main() {
	out := json.NewEncoder(os.Stdout)
	emit := func(v map[string]any) { _ = out.Encode(v) }
	emit(map[string]any{"type": "system", "subtype": "init", "model": "stub-model", "session_id": "stub"})

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var m struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
			Request   struct {
				Subtype string `json:"subtype"`
			} `json:"request"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch m.Type {
		case "control_request":
			emit(map[string]any{"type": "control_response", "response": map[string]any{
				"subtype": "success", "request_id": m.RequestID,
				"response": map[string]any{"applied": map[string]any{}, "session": map[string]any{"total_cost_usd": 0}},
			}})
		case "user":
			// The turn: the stub vendor's own, with its telemetry.
			stub := exec.Command("ynr-stub-vendor")
			stub.Env = os.Environ()
			if b, err := stub.CombinedOutput(); err != nil {
				fmt.Fprintf(os.Stderr, "fakeclaude: ynr-stub-vendor: %v\n%s", err, b)
				os.Exit(1)
			}
			usage := map[string]any{"input_tokens": 100, "output_tokens": 20, "cache_read_input_tokens": 0}
			emit(map[string]any{"type": "assistant", "message": map[string]any{
				"id": "msg_stub", "model": "stub-model", "role": "assistant",
				"content": []any{map[string]any{"type": "text", "text": "done"}}, "usage": usage,
			}})
			emit(map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": "done", "usage": usage, "total_cost_usd": 0})
		}
	}
}
