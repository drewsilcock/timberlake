package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func key(s string) tea.KeyMsg {
	if s == " " {
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestWorkerSelectionMovesAndClamps(t *testing.T) {
	m := newTestModel(3, 10, 100)

	m = step(m, key("j"))
	if m.SelectedWorker != 1 {
		t.Errorf("after j, selected = %d, want 1", m.SelectedWorker)
	}
	m = step(m, key("j"))
	m = step(m, key("j")) // past the end
	if m.SelectedWorker != 2 {
		t.Errorf("selection should clamp at last worker, got %d", m.SelectedWorker)
	}
	m = step(m, key("k"))
	if m.SelectedWorker != 1 {
		t.Errorf("after k, selected = %d, want 1", m.SelectedWorker)
	}
	m = step(m, key("k"))
	m = step(m, key("k")) // past the start
	if m.SelectedWorker != 0 {
		t.Errorf("selection should clamp at 0, got %d", m.SelectedWorker)
	}
}

func TestSpaceTogglesZoomAndPauseIsSeparate(t *testing.T) {
	m := newTestModel(2, 10, 100)

	m = step(m, key(" "))
	if !m.ZoomWorker {
		t.Error("space should zoom the selected worker")
	}
	if m.State == StatePaused {
		t.Error("space must no longer pause the sync")
	}

	m = step(m, key(" "))
	if m.ZoomWorker {
		t.Error("space again should leave the zoomed view")
	}

	// p still pauses.
	m = step(m, key("p"))
	if m.State != StatePaused {
		t.Errorf("p should pause, state = %v", m.State)
	}
	m = step(m, key("p"))
	if m.State == StatePaused {
		t.Error("p again should resume")
	}
}

func TestEscLeavesZoom(t *testing.T) {
	m := newTestModel(2, 10, 100)
	m = step(m, key(" "))
	if !m.ZoomWorker {
		t.Fatal("precondition: should be zoomed")
	}
	m = step(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.ZoomWorker {
		t.Error("esc should leave the zoomed view")
	}
}

func TestHistoryRecordedPerWorkerAndGlobally(t *testing.T) {
	m := newTestModel(2, 4, 400)

	m = step(m, WorkerStatusMsg{WorkerID: 0, Status: "Uploading", FileName: "a.bin", Size: 100})
	m = step(m, WorkerStatusMsg{WorkerID: 0, Status: "Done", FileName: "a.bin", Size: 100})
	m = step(m, WorkerStatusMsg{WorkerID: 1, Status: "Skipped", FileName: "b.bin", Size: 50})

	if got := len(m.RecentFiles); got != 2 {
		t.Errorf("global history = %d entries, want 2", got)
	}
	if got := len(m.Workers[0].History); got != 1 {
		t.Errorf("worker 0 history = %d, want 1", got)
	}
	if got := len(m.Workers[1].History); got != 1 {
		t.Errorf("worker 1 history = %d, want 1", got)
	}
	if m.Workers[0].BytesMoved != 100 {
		t.Errorf("worker 0 BytesMoved = %d, want 100", m.Workers[0].BytesMoved)
	}
	// Skipped files move no bytes but do count as handled.
	if m.Workers[1].BytesMoved != 0 {
		t.Errorf("skip should not count as bytes moved, got %d", m.Workers[1].BytesMoved)
	}
	if m.Workers[1].FilesDone != 1 {
		t.Errorf("worker 1 FilesDone = %d, want 1", m.Workers[1].FilesDone)
	}
}

func TestZoomedViewRendersSelectedWorker(t *testing.T) {
	m := newTestModel(3, 10, 1000)
	m.Width = 100
	m = step(m, WorkerStatusMsg{WorkerID: 1, Status: "Uploading", FileName: "deep/path/target.tif", Size: 500})
	m = step(m, key("j")) // select worker #2 (index 1)
	m = step(m, key(" "))

	out := m.View()
	if !strings.Contains(out, "DETAIL") {
		t.Error("zoomed view should render the worker detail panel")
	}
	if !strings.Contains(out, "target.tif") {
		t.Error("zoomed view should show the selected worker's current file")
	}
}
