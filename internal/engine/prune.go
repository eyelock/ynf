package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/eyelock/ynf/internal/policy"
	"github.com/eyelock/ynf/internal/store"
)

// SeenTTL is how long a webhook delivery's dedupe key is kept (ADR-003).
const SeenTTL = 7 * 24 * time.Hour

// SeenDoc is the document stored under seen/github/<delivery id>: when ynf first saw it.
func SeenDoc(now time.Time) []byte {
	b, _ := json.Marshal(struct {
		At time.Time `json:"at"`
	}{now.UTC()})
	return b
}

// stepsKey is the folder name under <work_dir>/steps for an item.
func stepsKey(itemKey string) string {
	return strings.NewReplacer("/", "_", "#", "_").Replace(itemKey)
}

// Prune removes what a lane's retention and the dedupe TTL say has expired: step run folders
// older than the lane's retention, and seen/ keys older than SeenTTL (ADR-010, ADR-003).
func (e *Engine) Prune(ctx context.Context) error {
	return errors.Join(e.pruneSteps(ctx), e.pruneSeen(ctx))
}

func (e *Engine) pruneSteps(ctx context.Context) error {
	if e.WorkDir == "" {
		return nil
	}
	items, err := e.allItems(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, it := range items {
		dir := filepath.Join(e.WorkDir, "steps", stepsKey(it.Key))
		runs, err := os.ReadDir(dir)
		if err != nil {
			continue // an item that never ran has no folder
		}
		retention := policy.DefaultRetention
		if _, lane, err := e.laneFor(ctx, it); err == nil {
			retention = lane.RetentionFor()
		}
		cutoff := e.Now().Add(-retention)
		left := len(runs)
		for _, r := range runs {
			info, err := r.Info()
			if err != nil || !info.ModTime().Before(cutoff) {
				continue
			}
			if err := os.RemoveAll(filepath.Join(dir, r.Name())); err != nil {
				errs = append(errs, err)
				continue
			}
			left--
		}
		if left == 0 {
			_ = os.Remove(dir)
		}
	}
	return errors.Join(errs...)
}

func (e *Engine) pruneSeen(ctx context.Context) error {
	keys, err := e.Store.Keys(ctx, "seen/")
	if err != nil {
		return err
	}
	now := e.Now()
	var errs []error
	for _, k := range keys {
		doc, ver, err := e.Store.Get(ctx, k)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		var s struct {
			At time.Time `json:"at"`
		}
		_ = json.Unmarshal(doc, &s)
		if s.At.IsZero() {
			// A key written before keys carried their time: its age starts now.
			if _, err := e.Store.Put(ctx, k, SeenDoc(now), ver); err != nil && !errors.Is(err, store.ErrConflict) {
				errs = append(errs, err)
			}
			continue
		}
		if now.Sub(s.At) <= SeenTTL {
			continue
		}
		if err := e.Store.Delete(ctx, k, ver); err != nil && !errors.Is(err, store.ErrConflict) && !errors.Is(err, store.ErrNotFound) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
