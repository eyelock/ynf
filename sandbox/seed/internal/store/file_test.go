package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSave(t *testing.T) {
	p := filepath.Join(t.TempDir(), "blob")
	if err := Save(p, []byte("x")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil || string(b) != "x" {
		t.Fatalf("got %q, %v", b, err)
	}
}
