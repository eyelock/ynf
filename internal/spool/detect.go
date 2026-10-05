package spool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/eyelock/ynf/internal/telemetry"
)

// Detection is what asking ynr found (ADR-012). Detecting it starts nothing: only the
// configuration starts ynr serve.
type Detection struct {
	Found        bool   `json:"found"`
	Version      string `json:"version,omitempty"`
	Build        string `json:"build,omitempty"` // full or slim
	Capabilities string `json:"capabilities,omitempty"`
	// Serving is true when a ynr serve holds the spool root ynr reports.
	Serving bool   `json:"serving,omitempty"`
	Detail  string `json:"detail,omitempty"` // why it was not detected
}

// String says what was found, for people: "ynr 0.1.0 (slim)" or "ynr not found".
func (d Detection) String() string {
	if !d.Found {
		return "ynr not found"
	}
	s := "ynr " + d.Version
	if d.Build != "" {
		s += " (" + d.Build + ")"
	}
	return s
}

// Detect asks bin (YnrBin) for `info --format json`. It is detected when that answers with a
// version.
func Detect(ctx context.Context, bin string) Detection {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var out bytes.Buffer
	c := exec.CommandContext(ctx, bin, "info", "--format", "json")
	telemetry.Command(ctx, c)
	c.Stdout = &out
	if err := c.Run(); err != nil {
		return Detection{Detail: fmt.Sprintf("%s: %v", bin, err)}
	}
	var v struct {
		Version      string `json:"version"`
		Build        string `json:"build"`
		Capabilities string `json:"capabilities"`
		Serving      *struct {
			PID int `json:"pid"`
		} `json:"serving"`
	}
	if err := json.Unmarshal(out.Bytes(), &v); err != nil || v.Version == "" {
		return Detection{Detail: bin + " info --format json gave no version"}
	}
	return Detection{Found: true, Version: v.Version, Build: v.Build, Capabilities: v.Capabilities, Serving: v.Serving != nil}
}
