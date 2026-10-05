package spool

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// HostVolumes is a sparse disk image per run, attached at the run's folder: it needs no privilege,
// and Docker Desktop shares it into a container like any other folder of the host.
func HostVolumes() Volumes { return diskImage{hdiutil: "hdiutil"} }

type diskImage struct{ hdiutil string }

func (diskImage) Name() string { return "disk image" }

func (d diskImage) Mount(dir string, size int64, _ int) (func() error, error) {
	if _, err := exec.LookPath(d.hdiutil); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoVolume, err)
	}
	backing, err := os.MkdirTemp("", "ynf-run-volume-")
	if err != nil {
		return nil, err
	}
	fail := func(err error) (func() error, error) {
		_ = os.RemoveAll(backing)
		return nil, err
	}
	mib := max((size+(1<<20)-1)>>20, 1)
	img := filepath.Join(backing, "run")
	if out, err := exec.Command(d.hdiutil, "create", "-quiet", "-size", fmt.Sprintf("%dm", mib), "-type", "SPARSE", "-fs", "HFS+", "-volname", "ynf-run", img).CombinedOutput(); err != nil {
		return fail(fmt.Errorf("hdiutil create: %w: %s", err, strings.TrimSpace(string(out))))
	}
	if out, err := exec.Command(d.hdiutil, "attach", "-quiet", "-mountpoint", dir, "-nobrowse", "-noverify", "-noautoopen", img+".sparseimage").CombinedOutput(); err != nil {
		return fail(fmt.Errorf("hdiutil attach: %w: %s", err, strings.TrimSpace(string(out))))
	}
	return func() error {
		defer func() { _ = os.RemoveAll(backing) }()
		if err := exec.Command(d.hdiutil, "detach", "-quiet", dir).Run(); err != nil {
			if out, err := exec.Command(d.hdiutil, "detach", "-quiet", "-force", dir).CombinedOutput(); err != nil {
				return fmt.Errorf("hdiutil detach: %w: %s", err, strings.TrimSpace(string(out)))
			}
		}
		return nil
	}, nil
}
