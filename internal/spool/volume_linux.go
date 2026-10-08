package spool

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// HostVolumes is a tmpfs per run: memory sized by the quota, with no backing file to clean up. It
// needs CAP_SYS_ADMIN, which an unprivileged user or a job container does not have, and then the
// quota watcher is the bound.
func HostVolumes() Volumes { return tmpfs{} }

type tmpfs struct{}

// tmpfsSource is the source ynf gives its tmpfs mounts, which is how it knows them again.
const tmpfsSource = "ynf-run"

func (tmpfs) Name() string { return "tmpfs" }

func (tmpfs) Backing(string) string { return "" }

func (tmpfs) Mount(dir string, size int64, entries int) (func() error, error) {
	opts := fmt.Sprintf("size=%d,nr_inodes=%d,uid=%d,gid=%d,mode=0700", size, entries, os.Getuid(), os.Getgid())
	err := syscall.Mount(tmpfsSource, dir, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, opts)
	switch {
	case errors.Is(err, syscall.EPERM), errors.Is(err, syscall.EACCES), errors.Is(err, syscall.ENODEV):
		return nil, fmt.Errorf("%w: a tmpfs needs CAP_SYS_ADMIN (%w)", ErrNoVolume, err)
	case err != nil:
		return nil, err
	}
	return func() error { return unmountTmpfs(dir) }, nil
}

func unmountTmpfs(dir string) error {
	if err := syscall.Unmount(dir, 0); err != nil {
		// Something still holds a file open: detach it, and it goes when the last holder does.
		return syscall.Unmount(dir, syscall.MNT_DETACH)
	}
	return nil
}

// Attached is the tmpfs mounted at dir, when it is one ynf mounted: a tmpfs of source ynf-run, at
// exactly dir, as the kernel lists it. A mount of anything else, there or elsewhere, is not found.
func (tmpfs) Attached(dir, _ string) (func() error, error) {
	info, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	real, err := filepath.EvalSymlinks(dir) // the kernel lists the resolved path
	if err != nil {
		return nil, err
	}
	for _, mp := range parseMountinfo(string(info), tmpfsSource) {
		if mp == real {
			return func() error { return unmountTmpfs(real) }, nil
		}
	}
	return nil, nil
}
