package sqlite_test

import (
	"path/filepath"
	"testing"

	"github.com/eyelock/ynf/internal/store"
	"github.com/eyelock/ynf/internal/store/sqlite"
	"github.com/eyelock/ynf/internal/store/storetest"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store {
		s, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}
