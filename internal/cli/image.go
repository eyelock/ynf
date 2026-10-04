package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/eyelock/ynf/internal/policy"
)

// imageBuilder returns the engine's agent image builder when ynh is on PATH (ADR-012: detected,
// never required).
func imageBuilder() func(context.Context, string, policy.Ynh) (string, error) {
	if _, err := exec.LookPath("ynh"); err != nil {
		return nil
	}
	return buildHarnessImage
}

// buildHarnessImage builds a ynh agent image from a harness folder in the worktree with
// `ynh image --entrypoint agent`. The tag names the commit, the harness and the base, so an
// unchanged harness is built once.
func buildHarnessImage(ctx context.Context, wt string, cfg policy.Ynh) (string, error) {
	dir := filepath.Join(wt, filepath.FromSlash(cfg.Harness))
	if !isHarness(dir) {
		return "", fmt.Errorf("harness %q is not a harness folder in the repository; for an installed harness set run.image to an agent image built with `ynh image <id> --entrypoint agent`", cfg.Harness)
	}
	head, err := output(ctx, wt, "git", "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	// The base's image id, not just its name: a rebuilt base (a newer ynh, say) is a new image.
	var baseID string
	if cfg.Base != "" {
		baseID, _ = output(ctx, "", "docker", "image", "inspect", "--format", "{{.Id}}", cfg.Base)
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(head) + "\x00" + cfg.Harness + "\x00" + cfg.Base + "\x00" + strings.TrimSpace(baseID)))
	tag := "ynf-harness:" + hex.EncodeToString(sum[:])[:16]
	if _, err := output(ctx, "", "docker", "image", "inspect", tag); err == nil {
		return tag, nil
	}
	args := []string{"image", "ynf-harness", "--from", dir, "--entrypoint", "agent", "--tag", tag}
	if cfg.Base != "" {
		args = append(args, "--base", cfg.Base)
	}
	if out, err := output(ctx, "", "ynh", args...); err != nil {
		return "", fmt.Errorf("ynh image: %w\n%s", err, tailLines(out, 10))
	}
	return tag, nil
}

var (
	capsMu    sync.Mutex
	capsCache = map[string]string{}
)

// hostCapabilities asks this machine's ynh for its capabilities version.
func hostCapabilities(ctx context.Context) (string, error) {
	out, err := stdoutOf(ctx, "ynh", "version", "--format", "json")
	if err != nil {
		return "", err
	}
	var v struct {
		Capabilities string `json:"capabilities"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || v.Capabilities == "" {
		return "", fmt.Errorf("ynh version: %q", tailLines(out, 3))
	}
	return v.Capabilities, nil
}

// stdoutOf runs a command for its standard output only, so warnings on stderr cannot corrupt it.
func stdoutOf(ctx context.Context, name string, args ...string) (string, error) {
	c := exec.CommandContext(ctx, name, args...)
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, tailLines(errb.String(), 5))
	}
	return out.String(), nil
}

// imageCapabilities asks the ynh inside an agent image for its capabilities version, once per
// image: the image's ynh runs the agent, not the host's.
func imageCapabilities(ctx context.Context, image string) (string, error) {
	capsMu.Lock()
	defer capsMu.Unlock()
	if v, ok := capsCache[image]; ok {
		return v, nil
	}
	out, err := output(ctx, "", "docker", "run", "--rm", "--network", "none", "--entrypoint", "ynh", image, "version", "--format", "json")
	if err != nil {
		return "", err
	}
	var v struct {
		Capabilities string `json:"capabilities"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || v.Capabilities == "" {
		return "", fmt.Errorf("ynh version in %s: %q", image, tailLines(out, 3))
	}
	capsCache[image] = v.Capabilities
	return v.Capabilities, nil
}

func isHarness(dir string) bool {
	for _, m := range []string{".agents/harness/plugin.json", ".ynh-plugin/plugin.json"} {
		if _, err := os.Stat(filepath.Join(dir, m)); err == nil {
			return true
		}
	}
	return false
}

func output(ctx context.Context, dir, name string, args ...string) (string, error) {
	c := exec.CommandContext(ctx, name, args...)
	c.Dir = dir
	var b bytes.Buffer
	c.Stdout, c.Stderr = &b, &b
	err := c.Run()
	return b.String(), err
}

func tailLines(s string, n int) string {
	l := strings.Split(strings.TrimSpace(s), "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, "\n")
}
