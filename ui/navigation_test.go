package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

func TestZoomedViewRendersInlineNotFullscreen(t *testing.T) {
	m := newTestModel(3, 10, 1000)
	m.Width, m.Height = 120, 44
	m = step(m, WorkerStatusMsg{WorkerID: 1, Status: "Uploading", FileName: "deep/path/target.tif", Size: 500})
	m = step(m, key("j")) // select worker #2 (index 1)
	m = step(m, key(" "))

	out := m.View()
	if !strings.Contains(out, "DETAIL") {
		t.Error("zoom should render the worker detail panel")
	}
	if !strings.Contains(out, "target.tif") {
		t.Error("zoom should show the selected worker's current file")
	}
	// The rest of the dashboard must remain visible — zoom is inline now.
	for _, want := range []string{"TIMBERLAKE", "Status:", "RECENT FILES"} {
		if !strings.Contains(out, want) {
			t.Errorf("zoomed view lost %q; it should render inside the workers panel", want)
		}
	}
}

func TestViewFitsTerminalHeight(t *testing.T) {
	// Regression: the dashboard used to overflow the terminal, scrolling the
	// header off the top.
	for _, h := range []int{24, 40, 60} {
		m := newTestModel(16, 5000, 1<<40)
		m.Width, m.Height = 140, h
		for i := 0; i < 8; i++ {
			m = step(m, WorkerStatusMsg{WorkerID: i, Status: "Uploading", FileName: "f.bin", Size: 100})
		}
		for i := 0; i < 20; i++ {
			m = step(m, WorkerStatusMsg{WorkerID: 0, Status: "Done", FileName: "done.bin", Size: 10})
		}
		got := lipgloss.Height(m.View())
		if got > h {
			t.Errorf("terminal height %d: view rendered %d lines (overflows)", h, got)
		}
	}
}

func TestTabFocusesRecentPane(t *testing.T) {
	m := newTestModel(3, 10, 1000)
	m.Width, m.Height = 120, 44
	if m.FocusedPane != PaneWorkers {
		t.Fatalf("default focus = %v, want PaneWorkers", m.FocusedPane)
	}

	m = step(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.FocusedPane != PaneRecent {
		t.Errorf("after tab, focus = %v, want PaneRecent", m.FocusedPane)
	}
	if strings.Contains(m.View(), "[Tab] to expand") {
		t.Error("focused recent pane should not still advertise [Tab] to expand")
	}

	m = step(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.FocusedPane != PaneWorkers {
		t.Errorf("tab again should return focus to workers, got %v", m.FocusedPane)
	}
}
