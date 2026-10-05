package spool

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// HostVolumes is a sparse disk image per run, attached at the run's folder: it needs no privilege,
// and Docker Desktop shares it into a container like any other folder of the host.
func HostVolumes() Volumes { return &diskImages{hdiutil: "hdiutil", images: map[string]string{}} }

type diskImages struct {
	hdiutil string
	mu      sync.Mutex
	images  map[string]string // run folder to the image attached there
}

func (*diskImages) Name() string { return "disk image" }

func (d *diskImages) Backing(dir string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.images[dir]
}

func (d *diskImages) Mount(dir string, size int64, _ int) (func() error, error) {
	if _, err := exec.LookPath(d.hdiutil); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoVolume, err)
	}
	backing, err := os.MkdirTemp("", backingPrefix)
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
	d.mu.Lock()
	d.images[dir] = img + ".sparseimage"
	d.mu.Unlock()
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

// Attached is the disk image ynf made for a run (backing, the image path it recorded), when it is
// still attached. It matches on that image path and nothing else: other disk images attached to
// the machine are not ynf's, and are never detached. The path must also be one ynf makes, a run
// image in a folder of ynf's naming.
func (d *diskImages) Attached(_, backing string) (func() error, error) {
	if !strings.HasPrefix(filepath.Base(filepath.Dir(backing)), backingPrefix) || !strings.HasSuffix(backing, ".sparseimage") {
		return nil, nil
	}
	out, err := exec.Command(d.hdiutil, "info").Output()
	if err != nil {
		return nil, fmt.Errorf("hdiutil info: %w", err)
	}
	want := resolved(backing)
	for _, a := range parseHdiutilInfo(string(out)) {
		if a.dev == "" || resolved(a.image) != want {
			continue
		}
		return func() error {
			defer removeBacking(backing)
			if err := exec.Command(d.hdiutil, "detach", "-quiet", a.dev).Run(); err != nil {
				if out, err := exec.Command(d.hdiutil, "detach", "-quiet", "-force", a.dev).CombinedOutput(); err != nil {
					return fmt.Errorf("hdiutil detach: %w: %s", err, strings.TrimSpace(string(out)))
				}
			}
			return nil
		}, nil
	}
	return nil, nil
}

// resolved is a path with its links followed, since hdiutil lists /private/var for /var.
func resolved(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}
