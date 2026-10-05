package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eyelock/ynf/internal/policy"
)

// MinYnhCapabilities is the first ynh capabilities version ynf supports as a detected runner:
// the one with `--auto-approve`, which the lanes ynf ships use (ADR-012).
const MinYnhCapabilities = "0.9.0"

// YnhBin is the ynh binary ynf asks: YNF_YNH_BIN when set, else ynh on PATH.
func YnhBin() string {
	if b := os.Getenv("YNF_YNH_BIN"); b != "" {
		return b
	}
	return "ynh"
}

// Detection is what detecting ynh on this host found (ADR-012).
type Detection struct {
	Found        bool   `json:"found"`
	Version      string `json:"version,omitempty"`      // ynh's own version
	Capabilities string `json:"capabilities,omitempty"` // the capabilities version ynf checks abilities against
	Detail       string `json:"detail,omitempty"`       // why it was not detected
}

// String says what was found, for people: "ynh 0.10.0" or "ynh not found".
func (d Detection) String() string {
	if d.Found {
		return "ynh " + d.Version
	}
	return "ynh not found"
}

// DetectYnh asks the host's ynh for its version. It is detected when the binary answers
// `version --format json` and its capabilities version is one ynf supports.
func DetectYnh(ctx context.Context) Detection {
	bin := YnhBin()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var out, errb bytes.Buffer
	c := exec.CommandContext(ctx, bin, "version", "--format", "json")
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		return Detection{Detail: fmt.Sprintf("%s: %v", bin, err)}
	}
	var v struct {
		Version      string `json:"version"`
		Capabilities string `json:"capabilities"`
	}
	if err := json.Unmarshal(out.Bytes(), &v); err != nil || v.Capabilities == "" {
		return Detection{Detail: bin + " version --format json gave no capabilities"}
	}
	d := Detection{Version: v.Version, Capabilities: v.Capabilities}
	if d.Version == "" {
		d.Version = v.Capabilities
	}
	if !AtLeast(v.Capabilities, MinYnhCapabilities) {
		d.Detail = fmt.Sprintf("%s has capabilities %s; ynf needs %s", bin, v.Capabilities, MinYnhCapabilities)
		return d
	}
	d.Found = true
	return d
}

var (
	detectMu   sync.Mutex
	detectDone bool
	detected   Detection
)

// DetectedYnh is DetectYnh, done once per process: detection is cheap, but a sweep resolves many
// lanes.
func DetectedYnh(ctx context.Context) Detection {
	detectMu.Lock()
	defer detectMu.Unlock()
	if !detectDone {
		detected, detectDone = DetectYnh(ctx), true
	}
	return detected
}

// AtLeast compares dotted versions numerically; anything unparseable is too old.
func AtLeast(have, want string) bool {
	h, w := strings.Split(have, "."), strings.Split(want, ".")
	for i := range w {
		if i >= len(h) {
			return false
		}
		hn, err1 := strconv.Atoi(h[i])
		wn, err2 := strconv.Atoi(w[i])
		if err1 != nil || err2 != nil {
			return false
		}
		if hn != wn {
			return hn > wn
		}
	}
	return true
}

// Resolved is the runner a lane gets and how it got it.
type Resolved struct {
	Runner Runner
	// Detected is true when the lane named no runner and detection chose this one.
	Detected bool
	// Note says what an unnamed lane resolved to on this host, such as "ynh (detected 0.10.0)" or
	// "command (ynh not found)"; empty when the lane named its runner.
	Note string
}

// Resolve is the precedence of ADR-012: the runner a lane names, else ynh when it has a ynh block
// and ynh was detected, else its command. A lane that names a runner never falls back.
func Resolve(lane policy.Lane, ynh Detection) (Resolved, error) {
	if lane.Run.Runner != "" {
		r, err := For(lane)
		return Resolved{Runner: r}, err
	}
	switch {
	case lane.Run.Ynh != nil && ynh.Found:
		return Resolved{Runner: YnhRunner{Cfg: *lane.Run.Ynh}, Detected: true, Note: "ynh (detected " + ynh.Version + ")"}, nil
	case lane.Run.Command != nil:
		why := "no ynh block"
		if lane.Run.Ynh != nil {
			why = ynhMissing(ynh)
		}
		return Resolved{Runner: CommandRunner{Cmd: *lane.Run.Command}, Detected: true, Note: "command (" + why + ")"}, nil
	}
	return Resolved{}, fmt.Errorf("lane %s names no runner: %s, and it has no command block to fall back to", lane.Name, ynhMissing(ynh))
}

// ynhMissing says why ynh is not what runs: not found, or found and not supported.
func ynhMissing(d Detection) string {
	if d.Detail != "" {
		return "ynh was not detected (" + d.Detail + ")"
	}
	return "ynh was not found"
}
