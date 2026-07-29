package ui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"timberlake/web"

	"github.com/charmbracelet/lipgloss"
)

// Pane identifies a selectable panel. Only one is shown at a time; Tab (or the
// number keys) switches between them. A single active panel keeps the dashboard
// readable on any terminal size, rather than stacking every panel vertically
// and overflowing tall screens.
type Pane int

const (
	PaneProgress Pane = iota
	PaneTransfers
	PaneHistory
	PaneQueue
	PaneWeb
)

// allPanes is the sidebar order.
var allPanes = []Pane{PaneProgress, PaneTransfers, PaneHistory, PaneQueue, PaneWeb}

func (p Pane) Title() string {
	switch p {
	case PaneProgress:
		return "Progress"
	case PaneTransfers:
		return "Transfers"
	case PaneHistory:
		return "History"
	case PaneQueue:
		return "Queue"
	case PaneWeb:
		return "Web UI"
	}
	return "?"
}

const sidebarWidth = 20

var (
	paneLbl    = lipgloss.NewStyle().Foreground(lipgloss.Color("#A0A0A0"))
	paneVal    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FAFAFA"))
	paneAccent = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7D56F4"))
	paneGreen  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#32CD32"))
	paneCyan   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#00BFFF"))
	paneDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("#666666"))
)

// renderSidebar draws the panel chooser with a per-panel count badge.
func (m Model) renderSidebar(height int) string {
	var rows []string
	rows = append(rows, paneAccent.Render("PANELS"), "")
	for i, p := range allPanes {
		label := fmt.Sprintf("%d %s", i+1, p.Title())
		badge := m.paneBadge(p)
		line := fmt.Sprintf("%-11s %s", label, badge)
		if p == m.ActivePane {
			line = lipgloss.NewStyle().Bold(true).
				Foreground(lipgloss.Color("#000000")).
				Background(lipgloss.Color("#FFD700")).
				Render("▸" + fmt.Sprintf("%-18s", line))
		} else {
			line = paneLbl.Render(" " + line)
		}
		rows = append(rows, line)
	}

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#5A5A5A")).
		Width(sidebarWidth).
		Height(height).
		Padding(0, 1).
		Render(strings.Join(rows, "\n"))
}

// paneBadge is the small right-hand count shown next to each panel name.
func (m Model) paneBadge(p Pane) string {
	switch p {
	case PaneProgress:
		done := m.UploadedFiles + m.SkippedFiles + m.FailedFiles
		if m.TotalFiles > 0 {
			return paneDim.Render(fmt.Sprintf("%.0f%%", float64(done)/float64(m.TotalFiles)*100))
		}
	case PaneTransfers:
		active := 0
		for _, w := range m.Workers {
			if w.Status != "Idle" {
				active++
			}
		}
		return paneDim.Render(fmt.Sprintf("%d", active))
	case PaneHistory:
		return paneDim.Render(formatNumber(int64(len(m.RecentFiles))))
	case PaneQueue:
		return paneDim.Render(formatNumber(m.remainingFiles()))
	case PaneWeb:
		if m.Web == nil {
			return paneDim.Render("off")
		}
		if m.Tunnel != nil {
			if s, _, _ := m.Tunnel.State(); s == web.TunnelOn {
				return paneGreen.Render("pub")
			}
		}
		return paneCyan.Render("lan")
	}
	return ""
}

func (m Model) remainingFiles() int64 {
	return m.TotalFiles - (m.UploadedFiles + m.SkippedFiles + m.FailedFiles)
}

// renderPaneContent produces the body of the active panel, sized to width.
func (m Model) renderPaneContent(width, height int) string {
	switch m.ActivePane {
	case PaneProgress:
		return m.renderProgressPane(width)
	case PaneTransfers:
		return m.renderTransfersPane(width)
	case PaneHistory:
		return m.renderHistoryPane(width, height)
	case PaneQueue:
		return m.renderQueuePane(width, height)
	case PaneWeb:
		return renderWebPane(m)
	}
	return ""
}

func (m Model) renderProgressPane(width int) string {
	barWidth := width - 26
	if barWidth < 10 {
		barWidth = 10
	}

	transferred := m.liveTransferredBytes() + m.SkippedBytes
	ratio := m.totalBarRatio()
	speedText := formatSpeed(m.SpeedBps)
	etaText := formatETA(m.TotalBytes-transferred, m.SpeedBps)
	if m.SpeedBps <= 0 && m.State == StateUploading && time.Since(m.StartTime) < 8*time.Second {
		speedText, etaText = "measuring…", "--:--"
	}

	filesRatio := float64(0)
	if m.TotalFiles > 0 {
		filesRatio = float64(m.UploadedFiles+m.SkippedFiles+m.FailedFiles) / float64(m.TotalFiles)
	}
	if filesRatio > 1 {
		filesRatio = 1
	}

	m.TotalBytesBar.Width = barWidth
	m.TotalFilesBar.Width = barWidth

	lines := []string{
		fmt.Sprintf("%s  %s", paneLbl.Render("Status:"), m.statusBadge()),
		"",
		fmt.Sprintf("%s [%s] %5.1f%%", paneAccent.Render(m.t("DIRTY POP (DATA)", "DATA        ")),
			m.TotalBytesBar.ViewAs(ratio), ratio*100),
		fmt.Sprintf("  %s / %s   %s %s   %s %s",
			paneVal.Render(formatBytes(transferred)), paneLbl.Render(formatBytes(m.TotalBytes)),
			paneLbl.Render("Speed:"), paneVal.Render(speedText),
			paneLbl.Render("ETA:"), paneVal.Render(etaText)),
		"",
		fmt.Sprintf("%s [%s] %5.1f%%", paneAccent.Render(m.t("IT'S GONNA BE ME", "FILES       ")),
			m.TotalFilesBar.ViewAs(filesRatio), filesRatio*100),
		fmt.Sprintf("  %s / %s files   %s %s   %s %s   %s %s",
			paneVal.Render(formatNumber(m.UploadedFiles+m.SkippedFiles+m.FailedFiles)),
			paneLbl.Render(formatNumber(m.TotalFiles)),
			paneGreen.Render("↑"), paneVal.Render(formatNumber(m.UploadedFiles)),
			paneCyan.Render("•"), paneVal.Render(formatNumber(m.SkippedFiles)),
			lipgloss.NewStyle().Foreground(lipgloss.Color("#FF4500")).Render("✖"),
			paneVal.Render(formatNumber(m.FailedFiles))),
		"",
		fmt.Sprintf("  %s %s   %s %s   %s %d MiB",
			paneLbl.Render("Elapsed:"), paneVal.Render(time.Since(m.StartTime).Round(time.Second).String()),
			paneLbl.Render(m.t("Space Cowboys:", "Workers:")), paneVal.Render(fmt.Sprintf("%d", m.Config.Jobs)),
			paneLbl.Render("Part size:"), m.Config.PartSizeMB),
	}

	// Trivia lives at the bottom of the progress panel now.
	if len(m.TriviaList) > 0 && !m.Config.OutOfSync {
		fact := m.TriviaList[m.TriviaIndex%len(m.TriviaList)]
		lines = append(lines, "",
			lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FF69B4")).
				Render("💡 'N SYNC TRIVIA BREAK"),
			lipgloss.NewStyle().Foreground(lipgloss.Color("#FAFAFA")).
				Width(width).Render(fact))
	}
	return strings.Join(lines, "\n")
}

func (m Model) statusBadge() string {
	switch m.State {
	case StatePaused:
		return statusPausedStyle.Render(m.t("PAUSED (\"DRIVE MYSELF CRAZY\")", "PAUSED") + " — workers stopped")
	case StateError:
		return statusErrorStyle.Render(m.t("ERROR (\"TEARIN' UP MY HEART\") ", "ERROR ") + m.ErrorMessage)
	case StateCatchingUp:
		return statusScanningStyle.Render("CATCHING UP")
	}
	switch {
	case m.Config.VerifyOnly:
		return statusPausedStyle.Render("VERIFYING (no writes)")
	case m.Config.DryRun:
		return statusPausedStyle.Render("DRY RUN (no writes)")
	}
	return statusUploadingStyle.Render(m.t("UPLOADING (\"CAN'T STOP THE FEELING!\")", "UPLOADING"))
}

// renderTransfersPane lists the workers, or the detail for the selected one.
func (m Model) renderTransfersPane(width int) string {
	if m.ZoomWorker && m.SelectedWorker >= 0 && m.SelectedWorker < len(m.Workers) {
		return renderWorkerDetail(m, m.SelectedWorker, width)
	}

	barWidth := width - 62
	if barWidth < 10 {
		barWidth = 10
	}

	var lines []string
	for i, w := range m.Workers {
		cursor := "  "
		if i == m.SelectedWorker {
			cursor = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFD700")).Render("▸ ")
		}
		id := fmt.Sprintf("#%02d", i+1)

		if w.Status == "Idle" {
			lines = append(lines, fmt.Sprintf("%s%s  %s  %s",
				cursor, paneDim.Render(id),
				lipgloss.NewStyle().Foreground(lipgloss.Color("#444444")).Render("[IDLE]"),
				paneDim.Render("—")))
			continue
		}

		ratio := float64(0)
		if w.TotalSize > 0 {
			ratio = float64(w.UploadedSize) / float64(w.TotalSize)
		}
		if ratio > 1 {
			ratio = 1
		}
		name := filepath.Base(w.FileName)
		if len(name) > 22 {
			name = name[:19] + "..."
		}
		lines = append(lines, fmt.Sprintf("%s%s %s [%s] %5.1f%% %-22s %9s",
			cursor,
			lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7D56F4")).Render(id),
			workerBadge(m, w.Status),
			renderBufferBar(barWidth, w.CommittedSize, w.UploadedSize, w.BufferedSize, w.TotalSize),
			ratio*100, name, formatBytes(w.TotalSize)))
	}
	if len(lines) == 0 {
		lines = append(lines, paneDim.Render("  (no workers)"))
	}
	lines = append(lines, "", paneDim.Render("  [↑/↓] select   [Space] detail"))
	return strings.Join(lines, "\n")
}

func workerBadge(m Model, status string) string {
	bold := lipgloss.NewStyle().Bold(true)
	switch status {
	case "Uploading":
		return bold.Foreground(lipgloss.Color("#000000")).Background(lipgloss.Color("#32CD32")).Render(m.t(" POP! ", " UP  "))
	case "Checking":
		return bold.Foreground(lipgloss.Color("#000000")).Background(lipgloss.Color("#FFD700")).Render(m.t(" MAY! ", " CHK "))
	case "Queued":
		return bold.Foreground(lipgloss.Color("#FAFAFA")).Background(lipgloss.Color("#5A5A5A")).Render(" WAIT ")
	case "Error":
		return bold.Foreground(lipgloss.Color("#FFFFFF")).Background(lipgloss.Color("#FF4500")).Render(m.t(" TEAR ", " ERR "))
	}
	return status
}

func (m Model) renderHistoryPane(width, height int) string {
	hist := m.RecentFiles
	if len(hist) > height {
		hist = hist[len(hist)-height:]
	}
	if len(hist) == 0 {
		return paneDim.Render("  (nothing finished yet)")
	}
	nameWidth := width - 34
	if nameWidth < 12 {
		nameWidth = 12
	}

	var lines []string
	for i := len(hist) - 1; i >= 0; i-- {
		r := hist[i]
		var tag string
		switch r.Status {
		case "Done":
			tag = paneGreen.Render("✔ sent   ")
		case "Skipped":
			tag = paneCyan.Render("• skipped")
		default:
			tag = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF4500")).Render("✖ failed ")
		}
		name := filepath.Base(r.Name)
		if len(name) > nameWidth {
			name = name[:nameWidth-3] + "..."
		}
		took := ""
		if r.Duration > 0 {
			took = "  in " + r.Duration.Round(time.Second).String()
		}
		lines = append(lines, fmt.Sprintf("  %s  %-*s %10s%s",
			tag, nameWidth, name, formatBytes(r.Size), paneLbl.Render(took)))
	}
	return strings.Join(lines, "\n")
}

// renderQueuePane previews what is coming up next in scan order.
func (m Model) renderQueuePane(width, height int) string {
	start := m.QueuePos + 1
	if start < 0 {
		start = 0
	}
	if start > len(m.WorkQueue) {
		start = len(m.WorkQueue)
	}
	upcoming := m.WorkQueue[start:]

	head := fmt.Sprintf("  %s %s files, %s remaining",
		paneLbl.Render("Up next:"),
		paneVal.Render(formatNumber(m.remainingFiles())),
		paneVal.Render(formatBytes(m.TotalBytes-(m.liveTransferredBytes()+m.SkippedBytes))))

	if len(upcoming) == 0 {
		return head + "\n\n" + paneDim.Render("  (queue drained)")
	}
	show := height - 3
	if show < 1 {
		show = 1
	}
	if len(upcoming) > show {
		upcoming = upcoming[:show]
	}
	nameWidth := width - 20
	if nameWidth < 12 {
		nameWidth = 12
	}

	lines := []string{head, ""}
	for i, it := range upcoming {
		name := it.RelativePath
		if len(name) > nameWidth {
			name = "..." + name[len(name)-(nameWidth-3):]
		}
		lines = append(lines, fmt.Sprintf("  %s %-*s %10s",
			paneDim.Render(fmt.Sprintf("%3d.", start+i+1)), nameWidth, name, formatBytes(it.Size)))
	}
	return strings.Join(lines, "\n")
}
