package ui

import (
	"context"
	"os"
	"testing"
	"time"

	"timberlake/config"

	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
)

// newTestModel builds a minimal uploading-state model with the given number of
// workers, wired up enough to drive Update with worker messages.
func newTestModel(jobs int, totalFiles, totalBytes int64) Model {
	workers := make([]WorkerState, jobs)
	bars := make([]progress.Model, jobs)
	for i := range workers {
		workers[i] = WorkerState{ID: i, Status: "Idle"}
		bars[i] = progress.New()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return Model{
		Config:        &config.AppConfig{Jobs: jobs},
		State:         StateUploading,
		Workers:       workers,
		WorkerBars:    bars,
		TotalBytesBar: progress.New(),
		TotalFilesBar: progress.New(),
		MsgChan:       make(chan tea.Msg, 16),
		TotalFiles:    totalFiles,
		TotalBytes:    totalBytes,
		StartTime:     time.Now(),
		pause:         newPauseGate(),
		layout:        &layoutBounds{},
		Ctx:           ctx,
		Cancel:        cancel,
	}
}

func step(m Model, msg tea.Msg) Model {
	next, _ := m.Update(msg)
	return next.(Model)
}

// TestWorkerAdvancesAfterFileCompletes reproduces the reported hang: a worker
// finishes a file and must return to Idle with the byte/file counts updated so
// it can pick up the next item.
func TestWorkerAdvancesAfterFileCompletes(t *testing.T) {
	m := newTestModel(2, 2, 200)

	// Worker 0 uploads a 100-byte file: Uploading -> progress -> Done.
	m = step(m, WorkerStatusMsg{WorkerID: 0, Status: "Uploading", FileName: "a.tif", Size: 100})
	if m.Workers[0].Status != "Uploading" {
		t.Fatalf("worker 0 should be Uploading, got %q", m.Workers[0].Status)
	}

	m = step(m, WorkerProgressMsg{WorkerID: 0, UploadedSize: 100, TotalSize: 100, FileName: "a.tif"})
	// The per-worker bar tracks read progress (activity indicator)...
	if m.Workers[0].UploadedSize != 100 {
		t.Fatalf("worker read progress = %d, want 100", m.Workers[0].UploadedSize)
	}
	// ...but in-flight read-ahead must NOT count as uploaded/committed bytes.
	if m.UploadedBytes != 0 {
		t.Fatalf("in-flight read counted as uploaded: got %d, want 0", m.UploadedBytes)
	}

	m = step(m, WorkerStatusMsg{WorkerID: 0, Status: "Done", FileName: "a.tif", Size: 100})

	if m.Workers[0].Status != "Idle" {
		t.Errorf("after Done, worker 0 status = %q, want Idle (worker never freed to take next file)", m.Workers[0].Status)
	}
	if m.UploadedFiles != 1 {
		t.Errorf("UploadedFiles = %d, want 1", m.UploadedFiles)
	}
	if m.CompletedBytes != 100 {
		t.Errorf("CompletedBytes = %d, want 100", m.CompletedBytes)
	}
	if m.UploadedBytes != 100 {
		t.Errorf("UploadedBytes = %d, want 100 (completed only, no in-flight)", m.UploadedBytes)
	}
	if m.State == StateDone {
		t.Errorf("state should not be Done with 1/2 files processed")
	}
}

// TestUploadedBytesCountsCommittedOnly verifies the headline uploaded figure
// counts only bytes committed to the server, not local read-ahead of in-flight
// files (which raced ahead and produced the misleading "246 MB/s at 100%").
func TestUploadedBytesCountsCommittedOnly(t *testing.T) {
	m := newTestModel(1, 3, 300)

	// File 1 completes fully -> committed.
	m = step(m, WorkerStatusMsg{WorkerID: 0, Status: "Uploading", FileName: "1", Size: 100})
	m = step(m, WorkerStatusMsg{WorkerID: 0, Status: "Done", FileName: "1", Size: 100})

	// File 2 in flight, fully read locally but not yet committed.
	m = step(m, WorkerStatusMsg{WorkerID: 0, Status: "Uploading", FileName: "2", Size: 100})
	m = step(m, WorkerProgressMsg{WorkerID: 0, UploadedSize: 100, TotalSize: 100, FileName: "2"})

	if m.UploadedBytes != 100 {
		t.Errorf("UploadedBytes = %d, want 100 (committed only; in-flight read excluded)", m.UploadedBytes)
	}
}

// TestAllFilesDoneReachesDoneState checks the terminal transition.
func TestAllFilesDoneReachesDoneState(t *testing.T) {
	m := newTestModel(1, 2, 200)

	m = step(m, WorkerStatusMsg{WorkerID: 0, Status: "Uploading", FileName: "1", Size: 100})
	m = step(m, WorkerStatusMsg{WorkerID: 0, Status: "Done", FileName: "1", Size: 100})
	m = step(m, WorkerStatusMsg{WorkerID: 0, Status: "Skipped", FileName: "2", Size: 100})

	if m.State != StateDone {
		t.Errorf("state = %v, want StateDone after all files processed", m.State)
	}
	if m.UploadedBytes != 100 || m.SkippedBytes != 100 {
		t.Errorf("UploadedBytes=%d SkippedBytes=%d, want 100/100", m.UploadedBytes, m.SkippedBytes)
	}
}

// TestCtrlCMidRunIsCancelled: quitting while work remains must NOT report success.
func TestCtrlCMidRunIsCancelled(t *testing.T) {
	m := newTestModel(1, 10, 1000)
	m = step(m, WorkerStatusMsg{WorkerID: 0, Status: "Uploading", FileName: "1", Size: 100})

	m = step(m, tea.KeyMsg{Type: tea.KeyCtrlC})

	if m.State != StateCancelled {
		t.Errorf("state = %v, want StateCancelled after Ctrl-C mid-run", m.State)
	}
	if m.EndTime.IsZero() {
		t.Error("EndTime should be set on cancel")
	}
}

// TestQuitAfterDoneStaysSuccess: pressing q on a finished run is success, not cancel.
func TestQuitAfterDoneStaysSuccess(t *testing.T) {
	m := newTestModel(1, 1, 100)
	m = step(m, WorkerStatusMsg{WorkerID: 0, Status: "Uploading", FileName: "1", Size: 100})
	m = step(m, WorkerStatusMsg{WorkerID: 0, Status: "Done", FileName: "1", Size: 100})
	if m.State != StateDone {
		t.Fatalf("precondition: state = %v, want StateDone", m.State)
	}

	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})

	if m.State != StateDone {
		t.Errorf("state = %v, want StateDone (q after completion is success)", m.State)
	}
}

// TestFailedUploadsAreCollected: errors must be captured for the on-exit log.
func TestFailedUploadsAreCollected(t *testing.T) {
	m := newTestModel(1, 2, 200)
	m = step(m, WorkerStatusMsg{WorkerID: 0, Status: "Uploading", FileName: "bad.tif", Size: 100})
	m = step(m, WorkerStatusMsg{WorkerID: 0, Status: "Error", FileName: "bad.tif", Err: "connection reset", Size: 100})

	if m.FailedFiles != 1 {
		t.Errorf("FailedFiles = %d, want 1", m.FailedFiles)
	}
	if len(m.Errors) != 1 || m.Errors[0].RelativePath != "bad.tif" || m.Errors[0].Message != "connection reset" {
		t.Errorf("Errors = %+v, want one {bad.tif, connection reset}", m.Errors)
	}

	// And it should surface via the error-log writer.
	path, err := WriteErrorLog(m)
	if err != nil {
		t.Errorf("WriteErrorLog: %v", err)
	}
	if path != "" {
		t.Cleanup(func() { _ = os.Remove(path) })
	}
}

// TestLiveTransferredIncludesInFlight: live figure keeps moving while a large
// file is mid-upload (so speed/ETA don't read zero), unlike committed-only.
func TestLiveTransferredIncludesInFlight(t *testing.T) {
	m := newTestModel(2, 5, 5000)
	m = step(m, WorkerStatusMsg{WorkerID: 0, Status: "Uploading", FileName: "big1", Size: 3000})
	m = step(m, WorkerStatusMsg{WorkerID: 1, Status: "Uploading", FileName: "big2", Size: 2000})
	m = step(m, WorkerProgressMsg{WorkerID: 0, UploadedSize: 1200, TotalSize: 3000})
	m = step(m, WorkerProgressMsg{WorkerID: 1, UploadedSize: 800, TotalSize: 2000})

	if got := m.liveTransferredBytes(); got != 2000 {
		t.Errorf("liveTransferredBytes = %d, want 2000 (1200+800 in-flight, 0 committed)", got)
	}
	if m.UploadedBytes != 0 {
		t.Errorf("committed UploadedBytes = %d, want 0", m.UploadedBytes)
	}
}
