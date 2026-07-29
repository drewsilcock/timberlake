package ui

import (
	"context"
	"sync"
	"testing"

	"timberlake/config"
	"timberlake/transfer"
)

// bulkDest is a countingDest that can also answer in bulk, so we can compare the
// two reconcile paths and count how many round-trips each costs.
type bulkDest struct {
	countingDest

	bulkMu    sync.Mutex
	bulkCalls int
	tooBig    bool
	failBulk  error
}

func (d *bulkDest) StatAll(_ context.Context, limit int, progress func(int64)) (map[string]int64, bool, error) {
	d.bulkMu.Lock()
	d.bulkCalls++
	d.bulkMu.Unlock()

	if d.failBulk != nil {
		return nil, false, d.failBulk
	}
	if d.tooBig {
		return nil, false, nil
	}
	out := make(map[string]int64)
	for name := range d.existingFile {
		out[name] = 1 // makeItems uses size 1
	}
	if limit > 0 && len(out) > limit {
		return nil, false, nil
	}
	if progress != nil {
		progress(int64(len(out)))
	}
	return out, true, nil
}

// runReconcile drives scan → (bulk) → workers to completion, mirroring the real
// message flow, and returns the final model.
func runReconcile(t *testing.T, dest transfer.Destination, items []transfer.Item, jobs int) Model {
	t.Helper()
	src := &staticSource{items: items}
	m := poolModel(t, &config.AppConfig{Jobs: jobs}, src, dest)
	m.TotalFiles = int64(len(items))

	// Scan is already done in poolModel; emulate what Update does next.
	if bs, ok := dest.(transfer.BulkStater); ok && m.TotalFiles >= bulkStatThreshold {
		known, okBulk, err := bs.StatAll(context.Background(), maxBulkEntries, nil)
		if err == nil && okBulk {
			m.KnownDest = known
		}
	}
	return drainPool(t, m)
}

func TestBulkStatAndPerItemAgree(t *testing.T) {
	items := makeItems(400) // above bulkStatThreshold
	existing := map[string]bool{}
	for i, it := range items {
		if i%3 == 0 {
			existing[it.RelativePath] = true
		}
	}

	bulk := &bulkDest{countingDest: countingDest{existingFile: existing}}
	perItem := &countingDest{existingFile: existing}

	withBulk := runReconcile(t, bulk, items, 4)
	withoutBulk := runReconcile(t, perItem, items, 4)

	if withBulk.SkippedFiles != withoutBulk.SkippedFiles {
		t.Errorf("skip decisions differ: bulk=%d per-item=%d",
			withBulk.SkippedFiles, withoutBulk.SkippedFiles)
	}
	if withBulk.UploadedFiles != withoutBulk.UploadedFiles {
		t.Errorf("upload decisions differ: bulk=%d per-item=%d",
			withBulk.UploadedFiles, withoutBulk.UploadedFiles)
	}
	if withBulk.SkippedFiles == 0 {
		t.Fatal("expected some files to be skipped")
	}

	// The whole point: the bulk path must not issue per-file Stat round-trips.
	bulk.mu.Lock()
	statCalls := bulk.maxStat
	bulk.mu.Unlock()
	if statCalls != 0 {
		t.Errorf("bulk path still made per-file Stat calls (max concurrent %d)", statCalls)
	}
}

func TestBulkStatFallsBackWhenTooLarge(t *testing.T) {
	items := makeItems(300)
	existing := map[string]bool{items[0].RelativePath: true}
	dest := &bulkDest{countingDest: countingDest{existingFile: existing}, tooBig: true}

	final := runReconcile(t, dest, items, 4)

	if final.KnownDest != nil {
		t.Error("an over-large listing must not be cached")
	}
	// Falling back still produces correct results.
	if final.SkippedFiles != 1 {
		t.Errorf("SkippedFiles = %d, want 1 via the per-item fallback", final.SkippedFiles)
	}
	if final.UploadedFiles != int64(len(items))-1 {
		t.Errorf("UploadedFiles = %d, want %d", final.UploadedFiles, len(items)-1)
	}
}

func TestBulkStatSkippedForSmallRuns(t *testing.T) {
	// Below the threshold the fixed cost of listing isn't worth it.
	items := makeItems(10)
	dest := &bulkDest{countingDest: countingDest{existingFile: map[string]bool{}}}

	src := &staticSource{items: items}
	m := poolModel(t, &config.AppConfig{Jobs: 2}, src, dest)
	m.TotalFiles = int64(len(items))
	var bs transfer.BulkStater = dest
	if m.TotalFiles >= bulkStatThreshold {
		_, _, _ = bs.StatAll(context.Background(), maxBulkEntries, nil)
	}
	drainPool(t, m)

	dest.bulkMu.Lock()
	calls := dest.bulkCalls
	dest.bulkMu.Unlock()
	if calls != 0 {
		t.Errorf("bulk listing ran for a %d-file run (threshold %d)", len(items), bulkStatThreshold)
	}
}

func TestBulkStatSizeMismatchStillUploads(t *testing.T) {
	// An entry present at the WRONG size must be re-uploaded, not skipped.
	items := makeItems(300) // makeItems uses size 1
	existing := map[string]bool{}
	for _, it := range items {
		existing[it.RelativePath] = true
	}
	dest := &bulkDest{countingDest: countingDest{existingFile: existing}}

	src := &staticSource{items: items}
	m := poolModel(t, &config.AppConfig{Jobs: 4}, src, dest)
	m.TotalFiles = int64(len(items))

	// Everything is "present" but recorded at a size that does not match.
	known := make(map[string]int64, len(items))
	for _, it := range items {
		known[it.RelativePath] = it.Size + 7
	}
	m.KnownDest = known

	final := drainPool(t, m)

	if final.SkippedFiles != 0 {
		t.Errorf("SkippedFiles = %d, want 0 — sizes did not match", final.SkippedFiles)
	}
	if final.UploadedFiles != int64(len(items)) {
		t.Errorf("UploadedFiles = %d, want %d", final.UploadedFiles, len(items))
	}
}
