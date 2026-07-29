package ui

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"timberlake/config"
	"timberlake/transfer"

	tea "github.com/charmbracelet/bubbletea"
)

// countingDest records how many Puts are in flight at once, and how many Stat
// calls run concurrently, so we can assert the transfer cap applies to transfers
// only and never throttles the destination checks.
type countingDest struct {
	mu           sync.Mutex
	curPut       int
	maxPut       int
	curStat      int
	maxStat      int
	putDelay     time.Duration
	statDelay    time.Duration
	existingFile map[string]bool
}

func (d *countingDest) Describe() string { return "counting://dest" }
func (d *countingDest) Close() error     { return nil }

func (d *countingDest) Stat(_ context.Context, item transfer.Item) (bool, int64, error) {
	d.mu.Lock()
	d.curStat++
	if d.curStat > d.maxStat {
		d.maxStat = d.curStat
	}
	d.mu.Unlock()

	time.Sleep(d.statDelay)

	d.mu.Lock()
	d.curStat--
	d.mu.Unlock()

	if d.existingFile[item.RelativePath] {
		return true, item.Size, nil
	}
	return false, 0, nil
}

func (d *countingDest) Put(_ context.Context, _ transfer.Item, _ transfer.OpenFunc, _ int64, _ transfer.Progress) error {
	d.mu.Lock()
	d.curPut++
	if d.curPut > d.maxPut {
		d.maxPut = d.curPut
	}
	d.mu.Unlock()

	time.Sleep(d.putDelay)

	d.mu.Lock()
	d.curPut--
	d.mu.Unlock()
	return nil
}

// staticSource serves a fixed item list with trivial content.
type staticSource struct{ items []transfer.Item }

func (s *staticSource) Describe() string { return "static://source" }
func (s *staticSource) Close() error     { return nil }
func (s *staticSource) Scan(context.Context, transfer.ScanProgress) ([]transfer.Item, error) {
	return s.items, nil
}
func (s *staticSource) Open(context.Context, transfer.Item) (transfer.OpenFunc, error) {
	return func(int64) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("x")), nil
	}, nil
}

// drainPool runs the worker pool to completion and returns the final model.
func drainPool(t *testing.T, m *Model) Model {
	t.Helper()
	startWorkerPool(m)

	total := int64(len(m.WorkQueue))
	deadline := time.After(20 * time.Second)
	var done int64
	for done < total {
		select {
		case msg := <-m.MsgChan:
			if s, ok := msg.(WorkerStatusMsg); ok {
				next, _ := m.Update(s)
				updated := next.(Model)
				m = &updated
				switch s.Status {
				case "Done", "Skipped", "Error":
					done++
				}
			}
		case <-deadline:
			t.Fatalf("timed out after %d/%d items", done, total)
		}
	}
	return *m
}

func poolModel(t *testing.T, cfg *config.AppConfig, src transfer.Source, dst transfer.Destination) *Model {
	t.Helper()
	items, err := src.Scan(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	workers := make([]WorkerState, cfg.Jobs)
	for i := range workers {
		workers[i] = WorkerState{ID: i, Status: "Idle"}
	}
	return &Model{
		Config:     cfg,
		Source:     src,
		Dest:       dst,
		Ctx:        context.Background(),
		MsgChan:    make(chan tea.Msg, 1024),
		WorkQueue:  items,
		Workers:    workers,
		TotalFiles: int64(len(items)),
		State:      StateCatchingUp,
		pause:      newPauseGate(),
		layout:     &layoutBounds{},
		StartTime:  time.Now(),
	}
}

func makeItems(n int) []transfer.Item {
	items := make([]transfer.Item, n)
	for i := range items {
		items[i] = transfer.Item{RelativePath: string(rune('a'+i%26)) + "-" + itoa(i) + ".bin", Size: 1}
	}
	return items
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// TestUploadJobsCapsTransfersNotChecks is the core of the split-concurrency
// feature: transfers are limited to --upload-jobs while all --jobs workers stay
// free to run destination checks.
func TestUploadJobsCapsTransfersNotChecks(t *testing.T) {
	const jobs, uploadJobs = 12, 3
	dest := &countingDest{putDelay: 40 * time.Millisecond, statDelay: 10 * time.Millisecond}
	src := &staticSource{items: makeItems(36)}

	m := poolModel(t, &config.AppConfig{Jobs: jobs, UploadJobs: uploadJobs}, src, dest)
	drainPool(t, m)

	dest.mu.Lock()
	maxPut, maxStat := dest.maxPut, dest.maxStat
	dest.mu.Unlock()

	if maxPut > uploadJobs {
		t.Errorf("concurrent transfers = %d, want <= %d (--upload-jobs)", maxPut, uploadJobs)
	}
	if maxStat <= uploadJobs {
		t.Errorf("concurrent checks = %d; checks should not be limited by --upload-jobs (%d)", maxStat, uploadJobs)
	}
}

// TestUploadJobsZeroMeansNoExtraLimit: without --upload-jobs, transfers may use
// every worker (previous behaviour).
func TestUploadJobsZeroMeansNoExtraLimit(t *testing.T) {
	const jobs = 8
	dest := &countingDest{putDelay: 40 * time.Millisecond}
	src := &staticSource{items: makeItems(24)}

	m := poolModel(t, &config.AppConfig{Jobs: jobs, UploadJobs: 0}, src, dest)
	drainPool(t, m)

	dest.mu.Lock()
	maxPut := dest.maxPut
	dest.mu.Unlock()

	if maxPut <= 1 {
		t.Errorf("concurrent transfers = %d, expected parallelism with no cap", maxPut)
	}
}

// TestCatchUpEndsAtFirstTransfer: a resumed run stays in the catch-up phase
// while files are being skipped, and flips to uploading on the first transfer.
func TestCatchUpEndsAtFirstTransfer(t *testing.T) {
	items := makeItems(6)
	existing := map[string]bool{}
	for _, it := range items[:4] { // first four already at destination
		existing[it.RelativePath] = true
	}
	dest := &countingDest{existingFile: existing}
	src := &staticSource{items: items}

	m := poolModel(t, &config.AppConfig{Jobs: 1}, src, dest) // 1 worker => ordered
	if m.State != StateCatchingUp {
		t.Fatalf("initial state = %v, want StateCatchingUp", m.State)
	}

	final := drainPool(t, m)

	if final.State == StateCatchingUp {
		t.Error("state should have left catch-up once a transfer started")
	}
	if final.SkippedFiles != 4 {
		t.Errorf("SkippedFiles = %d, want 4", final.SkippedFiles)
	}
	if final.UploadedFiles != 2 {
		t.Errorf("UploadedFiles = %d, want 2", final.UploadedFiles)
	}
	if final.TransferStartTime.IsZero() {
		t.Error("TransferStartTime should be set when the first transfer begins")
	}
}

// TestCatchUpStaysWhenEverythingSkipped: a fully up-to-date run never leaves the
// catch-up phase (nothing is ever transferred) and still reaches Done.
func TestCatchUpStaysWhenEverythingSkipped(t *testing.T) {
	items := makeItems(5)
	existing := map[string]bool{}
	for _, it := range items {
		existing[it.RelativePath] = true
	}
	dest := &countingDest{existingFile: existing}
	src := &staticSource{items: items}

	m := poolModel(t, &config.AppConfig{Jobs: 3}, src, dest)
	final := drainPool(t, m)

	if final.SkippedFiles != int64(len(items)) {
		t.Errorf("SkippedFiles = %d, want %d", final.SkippedFiles, len(items))
	}
	if final.State != StateDone {
		t.Errorf("state = %v, want StateDone", final.State)
	}
	if !final.TransferStartTime.IsZero() {
		t.Error("TransferStartTime should stay unset when nothing is transferred")
	}
}
