package spool

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestTheHostsVolumeIsHard: where this host allows a volume (a disk image on macOS, a tmpfs on
// Linux with CAP_SYS_ADMIN), a run's folder cannot grow past its size, and nothing is left mounted
// or behind. Where it does not, the test says so and skips; CI's unprivileged runners do.
func TestTheHostsVolumeIsHard(t *testing.T) {
	v := HostVolumes()
	if v == nil {
		t.Skip("no volume implementation for this host")
	}
	dir := filepath.Join(t.TempDir(), "run")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	const size = 2 << 20
	unmount, err := v.Mount(dir, size, maxEntries)
	if errors.Is(err, ErrNoVolume) {
		t.Skipf("this host does not allow a volume: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			_ = unmount()
		}
	}()
	f, err := os.Create(filepath.Join(dir, "flood.bin"))
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 64<<10)
	var wrote int64
	var werr error
	for wrote < 4*size && werr == nil {
		var n int
		n, werr = f.Write(chunk)
		wrote += int64(n)
	}
	_ = f.Close()
	if werr == nil || wrote > size {
		t.Errorf("wrote %d bytes into a %d byte volume, err %v: it is not hard", wrote, int64(size), werr)
	}
	if err := unmount(); err != nil {
		t.Fatal(err)
	}
	released = true
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Errorf("the folder under the mount is not empty: %v", ents)
	}
}
