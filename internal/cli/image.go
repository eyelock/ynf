package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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
	sum := sha256.Sum256([]byte(strings.TrimSpace(head) + "\x00" + cfg.Harness + "\x00" + cfg.Base))
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
