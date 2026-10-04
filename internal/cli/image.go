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
	"github.com/eyelock/ynf/internal/runner"
)

// imageBuilder returns the engine's agent image builder when building is allowed (images.build)
// and ynh is on PATH (ADR-012: detected, never required).
func imageBuilder(allowed bool) func(context.Context, string, policy.Ynh) (string, error) {
	if !allowed {
		return nil
	}
	if _, err := exec.LookPath("ynh"); err != nil {
		return nil
	}
	return buildHarnessImage
}

// buildHarnessImage builds a ynh agent image from a harness folder in the worktree with
// `ynh image --entrypoint agent`, for a lane that names no published image (ADR-007). The tag
// names the harness folder's contents (its git tree, which is everything ynh image copies) and
// the base by name and image id, so a harness is built again only when one of those changes.
func buildHarnessImage(ctx context.Context, wt string, cfg policy.Ynh) (string, error) {
	dir := filepath.Join(wt, filepath.FromSlash(cfg.Harness))
	if !isHarness(dir) {
		return "", fmt.Errorf("harness %q is not a harness folder in the repository; for an installed harness set run.image to an agent image built with `ynh image <id> --entrypoint agent`", cfg.Harness)
	}
	tree, err := output(ctx, wt, "git", "rev-parse", "HEAD:"+strings.TrimPrefix(filepath.ToSlash(filepath.Clean(cfg.Harness)), "."))
	if err != nil {
		return "", fmt.Errorf("harness %q: %w", cfg.Harness, err)
	}
	// The base's image id, not just its name: a rebuilt base (a newer ynh, say) is a new image.
	var baseID string
	if cfg.Base != "" {
		baseID, _ = output(ctx, "", "docker", "image", "inspect", "--format", "{{.Id}}", cfg.Base)
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(tree) + "\x00" + cfg.Harness + "\x00" + cfg.Base + "\x00" + strings.TrimSpace(baseID)))
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
	capsMu       sync.Mutex
	capsCache    = map[string]string{}
	harnessCache = map[string]runner.Harness{}
)

// imageHarness reads what the harness inside an agent image declares, by asking the image's own
// ynh (ADR-012), once per image and harness: `ynh ls` finds the harness, `ynh info` its manifest.
// want picks one when the image carries several (pickHarness).
func imageHarness(ctx context.Context, image, want string) (runner.Harness, error) {
	capsMu.Lock()
	defer capsMu.Unlock()
	if h, ok := harnessCache[image+"\x00"+want]; ok {
		return h, nil
	}
	ynh := func(args ...string) (string, error) {
		if image == "" { // the ynh installed here: an inline run in the factory image
			return stdoutOf(ctx, "ynh", args...)
		}
		return stdoutOf(ctx, "docker", append([]string{"run", "--rm", "--network", "none", "--entrypoint", "ynh", image}, args...)...)
	}
	out, err := ynh("ls", "--format", "json")
	if err != nil {
		return runner.Harness{}, err
	}
	var ls struct {
		Harnesses []listed `json:"harnesses"`
	}
	if err := json.Unmarshal([]byte(out), &ls); err != nil {
		return runner.Harness{}, fmt.Errorf("ynh ls in %s: %w", image, err)
	}
	id, err := pickHarness(ls.Harnesses, want)
	if err != nil {
		return runner.Harness{}, fmt.Errorf("%s: %w", image, err)
	}
	ids := []string{id}
	out, err = ynh("info", ids[0], "--format", "json")
	if err != nil {
		return runner.Harness{}, err
	}
	var info struct {
		Harness struct {
			Manifest json.RawMessage `json:"manifest"`
		} `json:"harness"`
	}
	if err := json.Unmarshal([]byte(out), &info); err != nil || len(info.Harness.Manifest) == 0 {
		return runner.Harness{}, fmt.Errorf("ynh info %s in %s: %q", ids[0], image, tailLines(out, 3))
	}
	h, err := runner.ParseManifest(info.Harness.Manifest)
	if err != nil {
		return runner.Harness{}, fmt.Errorf("the manifest of %s in %s: %w", ids[0], image, err)
	}
	h.ID = ids[0]
	harnessCache[image+"\x00"+want] = h
	return h, nil
}

type listed = struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// pickHarness chooses the harness a lane means from those an image carries. An empty name or "."
// means the one it carries (the engine passes "" for an image it built from a harness folder);
// anything else must match a harness id or name exactly, so a lane naming the wrong harness on a
// published image fails rather than running whatever the image holds.
func pickHarness(hs []listed, want string) (string, error) {
	var all []string
	for _, h := range hs {
		all = append(all, h.ID)
		if want != "" && (want == h.ID || want == h.Name) {
			return h.ID, nil
		}
	}
	if (want == "" || want == ".") && len(hs) == 1 {
		return hs[0].ID, nil
	}
	return "", fmt.Errorf("no single harness for %q among %v; name one in ynh.harness", want, all)
}

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
