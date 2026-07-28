package ui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"timberlake/web"

	"github.com/charmbracelet/lipgloss"
	"github.com/mdp/qrterminal/v3"
)

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FAFAFA")).
			Background(lipgloss.Color("#7D56F4")).
			Padding(0, 1)

	subtitleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#A0A0A0")).
			MarginLeft(1)

	headerBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#7D56F4")).
			Padding(0, 1).
			MarginBottom(1)

	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#5A5A5A")).
			Padding(0, 1).
			MarginBottom(1)

	statusScanningStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#00BFFF"))
	statusUploadingStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#32CD32"))
	statusPausedStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFD700"))
	statusErrorStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FF4500"))

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#888888")).
			MarginTop(1)
)

// t returns the 'N SYNC-themed string, or the plain one when --out-of-sync is set.
func (m Model) t(themed, plain string) string {
	if m.Config != nil && m.Config.OutOfSync {
		return plain
	}
	return themed
}

// sourceLabel / destLabel describe the endpoints for display, falling back to
// config when the backends aren't set (e.g. in unit tests).
func (m Model) sourceLabel() string {
	if m.Source != nil {
		return m.Source.Describe()
	}
	return m.Config.SourceDir
}

func (m Model) destLabel() string {
	if m.Dest != nil {
		return m.Dest.Describe()
	}
	return m.Config.Destination
}

func (m Model) View() string {
	var b strings.Builder

	// Header
	headerText := fmt.Sprintf("%s %s",
		titleStyle.Render("TIMBERLAKE"),
		subtitleStyle.Render(fmt.Sprintf("Source: %s ➔ %s%s", m.sourceLabel(), m.destLabel(), m.t(" 🎶 No Strings Attached", ""))),
	)
	b.WriteString(headerBoxStyle.Render(headerText))
	b.WriteString("\n")

	// Zoomed worker takes over the whole screen.
	if m.ZoomWorker && m.SelectedWorker >= 0 && m.SelectedWorker < len(m.Workers) {
		b.WriteString(renderWorkerDetail(m, m.SelectedWorker))
		b.WriteString("\n")
		b.WriteString(helpStyle.Render("Controls: [↑/↓/k/j] Other worker  [Space/Esc] Back to list  [p] Pause  [q] Quit"))
		return b.String()
	}

	// Pausing during catch-up should keep showing the catch-up panel.
	displayState := m.State
	if m.State == StatePaused && m.PausedFrom == StateCatchingUp {
		displayState = StateCatchingUp
	}

	// State-specific layout
	switch displayState {
	case StateScanning:
		b.WriteString(panelStyle.Render(fmt.Sprintf(
			"%s Scanning source%s\nSource: %s",
			m.Spinner.View(),
			m.t(" tree... ('It's Gonna Be Me!')", "..."),
			statusScanningStyle.Render(m.sourceLabel()),
		)))

	case StateCatchingUp:
		b.WriteString(renderCatchUpPanel(m))
		b.WriteString("\n")
		b.WriteString(helpStyle.Render("Controls: [p/Space] Pause  [q] Quit"))
		return b.String()

	case StateUploading, StatePaused, StateDone, StateError:
		if m.State == StateDone {
			b.WriteString(renderSummaryBox(m))
			b.WriteString("\n")
			b.WriteString(helpStyle.Render(m.t("Sync complete! Ain't no lie, Bye Bye Bye! 👋 Press [q] to exit.", "Sync complete. Press [q] to exit.")))
			return b.String()
		}

		// Total Data Progress Bar — transferred (committed + in-flight) + skipped.
		transferred := m.liveTransferredBytes() + m.SkippedBytes
		totalBytesRatio := m.totalBarRatio()

		// Show "measuring…" during the warmup window instead of a misleading 0.
		speedText := formatSpeed(m.SpeedBps)
		etaText := formatETA(m.TotalBytes-transferred, m.SpeedBps)
		if m.SpeedBps <= 0 && m.State == StateUploading && time.Since(m.StartTime) < 8*time.Second {
			speedText = "measuring…"
			etaText = "--:--"
		}

		dataPanel := fmt.Sprintf(
			m.t("DIRTY POP (DATA)", "DATA")+" [%s] %5.1f%%\n%s / %s (Speed: %s | ETA: %s)",
			m.TotalBytesBar.ViewAs(totalBytesRatio),
			totalBytesRatio*100,
			formatBytes(transferred),
			formatBytes(m.TotalBytes),
			speedText,
			etaText,
		)

		// Total Files Progress Bar
		totalFilesRatio := float64(0)
		if m.TotalFiles > 0 {
			totalFilesRatio = float64(m.UploadedFiles+m.SkippedFiles+m.FailedFiles) / float64(m.TotalFiles)
		}
		if totalFilesRatio > 1.0 {
			totalFilesRatio = 1.0
		}

		filesPanel := fmt.Sprintf(
			m.t("IT'S GONNA BE ME", "FILES")+" [%s] %5.1f%%\n%d / %d Files (%d Uploaded, %d Skipped, %d Failed)",
			m.TotalFilesBar.ViewAs(totalFilesRatio),
			totalFilesRatio*100,
			m.UploadedFiles+m.SkippedFiles+m.FailedFiles,
			m.TotalFiles,
			m.UploadedFiles,
			m.SkippedFiles,
			m.FailedFiles,
		)

		statusBadge := ""
		switch m.State {
		case StateUploading:
			switch {
			case m.Config.VerifyOnly:
				statusBadge = statusPausedStyle.Render("VERIFYING (no writes)")
			case m.Config.DryRun:
				statusBadge = statusPausedStyle.Render("DRY RUN (no writes)")
			default:
				statusBadge = statusUploadingStyle.Render(m.t("UPLOADING (\"CAN'T STOP THE FEELING!\")", "UPLOADING"))
			}
		case StatePaused:
			statusBadge = statusPausedStyle.Render(m.t("PAUSED (\"DRIVE MYSELF CRAZY\")", "PAUSED"))
		case StateError:
			statusBadge = statusErrorStyle.Render(m.t("ERROR (\"TEARIN' UP MY HEART\") ", "ERROR ") + m.ErrorMessage)
		}

		b.WriteString(panelStyle.Render(fmt.Sprintf(
			"Status: %s | "+m.t("Space Cowboys", "Workers")+": %d | Part Size: %d MiB\n\n%s\n\n%s",
			statusBadge,
			m.Config.Jobs,
			m.Config.PartSizeMB,
			dataPanel,
			filesPanel,
		)))
		b.WriteString("\n")

		// 'N SYNC Trivia Box (hidden entirely in --out-of-sync mode)
		if len(m.TriviaList) > 0 && !m.Config.OutOfSync {
			triviaFact := m.TriviaList[m.TriviaIndex%len(m.TriviaList)]
			triviaHeader := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FF69B4")).Render("💡 'N SYNC TRIVIA BREAK")
			triviaText := lipgloss.NewStyle().Foreground(lipgloss.Color("#FAFAFA")).Render(triviaFact)

			triviaBoxStyle := lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#FF69B4")).
				Padding(0, 1)

			b.WriteString(triviaBoxStyle.Render(fmt.Sprintf("%s\n%s", triviaHeader, triviaText)))
			b.WriteString("\n\n")
		}

		// Active Worker Progress Bars List
		var workerLines []string
		activeCount := 0
		cursor := func(i int) string {
			if i == m.SelectedWorker {
				return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFD700")).Render("▸ ")
			}
			return "  "
		}
		for i, w := range m.Workers {
			wIDStr := fmt.Sprintf("#%02d", i+1)
			if w.Status == "Idle" {
				workerLines = append(workerLines, fmt.Sprintf("%s%s  %-6s  %s",
					cursor(i),
					lipgloss.NewStyle().Foreground(lipgloss.Color("#666666")).Render(wIDStr),
					lipgloss.NewStyle().Foreground(lipgloss.Color("#444444")).Render("[IDLE]"),
					lipgloss.NewStyle().Foreground(lipgloss.Color("#333333")).Render("—"),
				))
				continue
			}

			activeCount++
			wRatio := float64(0)
			if w.TotalSize > 0 {
				wRatio = float64(w.UploadedSize) / float64(w.TotalSize)
			}
			if wRatio > 1.0 {
				wRatio = 1.0
			}

			statusBadge := w.Status
			switch w.Status {
			case "Uploading":
				statusBadge = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#000000")).Background(lipgloss.Color("#32CD32")).Render(m.t(" POP! ", " UP  "))
			case "Checking":
				statusBadge = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#000000")).Background(lipgloss.Color("#FFD700")).Render(m.t(" MAY! ", " CHK "))
			case "Queued":
				statusBadge = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FAFAFA")).Background(lipgloss.Color("#5A5A5A")).Render(m.t(" WAIT ", " WAIT "))
			case "Error":
				statusBadge = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF")).Background(lipgloss.Color("#FF4500")).Render(m.t(" TEAR ", " ERR "))
			}

			shortName := filepath.Base(w.FileName)
			if len(shortName) > 24 {
				shortName = shortName[:21] + "..."
			}

			line := fmt.Sprintf("%s%s  %s [%s] %5.1f%% %-24s (%s / %s)",
				cursor(i),
				lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7D56F4")).Render(wIDStr),
				statusBadge,
				renderBufferBar(m.WorkerBars[i].Width, w.CommittedSize, w.UploadedSize, w.BufferedSize, w.TotalSize),
				wRatio*100,
				shortName,
				formatBytes(w.UploadedSize),
				formatBytes(w.TotalSize),
			)
			workerLines = append(workerLines, line)
		}

		m.Viewport.SetContent(strings.Join(workerLines, "\n"))
		// Keep the cursor in view as it moves through the list.
		if h := m.Viewport.Height; h > 0 {
			switch {
			case m.SelectedWorker < m.Viewport.YOffset:
				m.Viewport.SetYOffset(m.SelectedWorker)
			case m.SelectedWorker >= m.Viewport.YOffset+h:
				m.Viewport.SetYOffset(m.SelectedWorker - h + 1)
			}
		}

		workersBoxStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#7D56F4")).
			Padding(0, 1)

		workersBoxHeader := lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7D56F4")).
			Render(fmt.Sprintf("⚙ "+m.t("SPACE COWBOYS", "WORKERS")+" & ACTIVE TRANSFERS (%d/%d Active)", activeCount, len(m.Workers)))

		b.WriteString(workersBoxHeader)
		b.WriteString("\n")
		b.WriteString(workersBoxStyle.Render(m.Viewport.View()))
		b.WriteString("\n")
		b.WriteString(renderHistoryPanel(m))
		if m.ShowQR {
			b.WriteString("\n")
			b.WriteString(renderQRPanel(m))
		}
	}

	// Footer Help Text
	b.WriteString("\n")
	b.WriteString(helpStyle.Render(m.t(
		"Controls: [p] Pause/Resume (\"Drive Myself Crazy\")  [↑/↓/k/j] Select Cowboy  [Space] Zoom  [q] Quit (\"Bye Bye Bye!\")",
		"Controls: [p] Pause/Resume  [↑/↓/k/j] Select worker  [Space] Zoom  [q] Quit")))

	return b.String()
}

// renderQRPanel shows the share URL plus a scannable QR code, and the state of
// the optional public tunnel.
func renderQRPanel(m Model) string {
	if m.Web == nil {
		return ""
	}
	lbl := lipgloss.NewStyle().Foreground(lipgloss.Color("#A0A0A0"))
	val := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FAFAFA"))
	head := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FF69B4"))

	share := m.Web.ShareURL()

	var qr strings.Builder
	qrterminal.GenerateWithConfig(share, qrterminal.Config{
		Level:          qrterminal.L,
		Writer:         &qr,
		HalfBlocks:     true,
		BlackChar:      qrterminal.BLACK_BLACK,
		WhiteChar:      qrterminal.WHITE_WHITE,
		WhiteBlackChar: qrterminal.WHITE_BLACK,
		BlackWhiteChar: qrterminal.BLACK_WHITE,
		QuietZone:      1,
	})

	tunnelLine := lbl.Render("Public link:") + " " + lbl.Render("off — press [w] to share beyond this network")
	if m.Tunnel != nil {
		switch state, u, err := m.Tunnel.State(); state {
		case web.TunnelStarting:
			tunnelLine = lbl.Render("Public link:") + " " + statusPausedStyle.Render("starting…")
		case web.TunnelOn:
			tunnelLine = lbl.Render("Public link:") + " " + lipgloss.NewStyle().Bold(true).
				Foreground(lipgloss.Color("#32CD32")).Render(u)
		case web.TunnelFailed:
			msg := "failed"
			if err != nil {
				msg = err.Error()
			}
			tunnelLine = lbl.Render("Public link:") + " " + statusErrorStyle.Render(msg)
		}
	}

	lines := []string{
		head.Render("📱 SCAN TO WATCH PROGRESS"),
		"",
		lbl.Render("On this network:") + " " + val.Render(m.Web.LanURL()),
		tunnelLine,
	}
	if m.TunnelNote != "" {
		lines = append(lines, lbl.Render(m.TunnelNote))
	}
	lines = append(lines, "", strings.TrimRight(qr.String(), "\n"))

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#FF69B4")).
		Padding(0, 1).
		Render(strings.Join(lines, "\n"))
}

// renderWorkerDetail is the full-screen view for a single worker: what it is
// transferring right now, its own throughput, and the files it has finished.
func renderWorkerDetail(m Model, idx int) string {
	w := m.Workers[idx]

	lbl := lipgloss.NewStyle().Foreground(lipgloss.Color("#A0A0A0"))
	val := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FAFAFA"))
	purple := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7D56F4"))

	header := purple.Render(fmt.Sprintf("🔍 %s #%02d — DETAIL",
		strings.ToUpper(m.t("SPACE COWBOY", "WORKER")), idx+1))

	// Current item
	var current string
	if w.Status == "Idle" || w.FileName == "" {
		current = lbl.Render("  Idle — waiting for the next file.")
	} else {
		ratio := float64(0)
		if w.TotalSize > 0 {
			ratio = float64(w.UploadedSize) / float64(w.TotalSize)
		}
		if ratio > 1 {
			ratio = 1
		}
		barWidth := m.Width - 20
		if barWidth < 20 {
			barWidth = 20
		}

		// Throughput for the file in flight.
		rate := "—"
		if w.Status == "Uploading" && !w.StartTime.IsZero() {
			if secs := time.Since(w.StartTime).Seconds(); secs > 0.5 {
				rate = formatSpeed(float64(w.CommittedSize) / secs)
			}
		}
		eta := "--:--"
		if w.Status == "Uploading" && !w.StartTime.IsZero() {
			if secs := time.Since(w.StartTime).Seconds(); secs > 0.5 && w.CommittedSize > 0 {
				bps := float64(w.CommittedSize) / secs
				eta = formatETA(w.TotalSize-w.CommittedSize, bps)
			}
		}

		current = fmt.Sprintf(
			"  %s %s\n  %s %s\n\n  [%s] %5.1f%%\n\n  %s %s   %s %s   %s %s\n  %s %s   %s %s",
			lbl.Render("File:  "), val.Render(w.FileName),
			lbl.Render("Status:"), val.Render(w.Status),
			renderBufferBar(barWidth, w.CommittedSize, w.UploadedSize, w.BufferedSize, w.TotalSize),
			ratio*100,
			lbl.Render("Committed:"), val.Render(formatBytes(w.CommittedSize)),
			lbl.Render("Sent:"), val.Render(formatBytes(w.UploadedSize)),
			lbl.Render("Size:"), val.Render(formatBytes(w.TotalSize)),
			lbl.Render("Rate:     "), val.Render(rate),
			lbl.Render("ETA: "), val.Render(eta),
		)
	}

	// Lifetime stats for this worker.
	lifetime := fmt.Sprintf("  %s %s   %s %s",
		lbl.Render("Files finished:"), val.Render(formatNumber(w.FilesDone)),
		lbl.Render("Data moved:"), val.Render(formatBytes(w.BytesMoved)),
	)

	// This worker's file history, newest first.
	const show = 12
	hist := w.History
	if len(hist) > show {
		hist = hist[len(hist)-show:]
	}
	histLines := []string{purple.Render("  History (newest first)")}
	if len(hist) == 0 {
		histLines = append(histLines, lbl.Render("    (nothing yet)"))
	}
	for i := len(hist) - 1; i >= 0; i-- {
		r := hist[i]
		var tag string
		switch r.Status {
		case "Done":
			tag = lipgloss.NewStyle().Foreground(lipgloss.Color("#32CD32")).Render("✔")
		case "Skipped":
			tag = lipgloss.NewStyle().Foreground(lipgloss.Color("#00BFFF")).Render("•")
		default:
			tag = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF4500")).Render("✖")
		}
		took := ""
		if r.Duration > 0 {
			took = fmt.Sprintf(" in %s", r.Duration.Round(time.Second))
		}
		name := r.Name
		if len(name) > 46 {
			name = "..." + name[len(name)-43:]
		}
		histLines = append(histLines, fmt.Sprintf("    %s %-46s %10s%s",
			tag, name, formatBytes(r.Size), lbl.Render(took)))
	}

	body := strings.Join([]string{
		header, "", current, "", lifetime, "", strings.Join(histLines, "\n"),
	}, "\n")

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#7D56F4")).
		Padding(0, 1).
		Render(body)
}

// renderHistoryPanel shows the most recently finished items across all workers.
func renderHistoryPanel(m Model) string {
	const show = 5
	hist := m.RecentFiles
	if len(hist) > show {
		hist = hist[len(hist)-show:]
	}

	lbl := lipgloss.NewStyle().Foreground(lipgloss.Color("#A0A0A0"))
	header := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#00BFFF")).
		Render(fmt.Sprintf("🕘 RECENT FILES (%s remaining)",
			formatNumber(m.TotalFiles-(m.UploadedFiles+m.SkippedFiles+m.FailedFiles))))

	var lines []string
	if len(hist) == 0 {
		lines = append(lines, lbl.Render("  (nothing finished yet)"))
	}
	// Newest first for reading.
	for i := len(hist) - 1; i >= 0; i-- {
		r := hist[i]
		var tag string
		switch r.Status {
		case "Done":
			tag = lipgloss.NewStyle().Foreground(lipgloss.Color("#32CD32")).Render("✔ sent   ")
		case "Skipped":
			tag = lipgloss.NewStyle().Foreground(lipgloss.Color("#00BFFF")).Render("• skipped")
		default:
			tag = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF4500")).Render("✖ failed ")
		}
		name := filepath.Base(r.Name)
		if len(name) > 32 {
			name = name[:29] + "..."
		}
		took := ""
		if r.Duration > 0 {
			took = fmt.Sprintf("  in %s", r.Duration.Round(time.Second))
		}
		lines = append(lines, fmt.Sprintf("  %s  %-32s %10s%s", tag, name, formatBytes(r.Size), lbl.Render(took)))
	}

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#5A5A5A")).
		Padding(0, 1).
		Render(header + "\n" + strings.Join(lines, "\n"))
}

// renderCatchUpPanel draws the reconcile phase: the destination is being checked
// for files a previous run already transferred. This is deliberately *not* the
// upload view — during catch-up nothing is being transferred, so showing the
// upload/data bars racing up from zero misrepresents what's happening.
func renderCatchUpPanel(m Model) string {
	checked := m.SkippedFiles + m.UploadedFiles + m.FailedFiles

	ratio := float64(0)
	if m.TotalFiles > 0 {
		ratio = float64(checked) / float64(m.TotalFiles)
	}
	if ratio > 1 {
		ratio = 1
	}

	lbl := lipgloss.NewStyle().Foreground(lipgloss.Color("#A0A0A0"))
	val := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FAFAFA"))
	cyan := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#00BFFF"))

	header := cyan.Render("⟳ CATCHING UP WITH PREVIOUS RUN")
	if m.State == StatePaused {
		header = statusPausedStyle.Render("⏸ CATCH-UP PAUSED")
	}

	// Rate and ETA for the catch-up itself, not for any transfer.
	rateText, etaText := "measuring…", "--:--"
	if elapsed := time.Since(m.StartTime).Seconds(); elapsed >= 1 && checked > 0 {
		rate := float64(checked) / elapsed
		rateText = fmt.Sprintf("%s files/s", formatNumber(int64(rate)))
		if remaining := m.TotalFiles - checked; remaining > 0 && rate > 0 {
			etaText = (time.Duration(float64(remaining)/rate) * time.Second).Round(time.Second).String()
		}
	}

	body := fmt.Sprintf(
		"%s %s\n%s\n\n%s [%s] %5.1f%%\n%s\n\n  %s %s\n  %s %s\n  %s %s",
		m.Spinner.View(),
		header,
		lbl.Render("Checking which files are already at the destination — nothing is being transferred yet."),
		lbl.Render("Checked"),
		m.TotalFilesBar.ViewAs(ratio),
		ratio*100,
		fmt.Sprintf("%s / %s files", val.Render(formatNumber(checked)), val.Render(formatNumber(m.TotalFiles))),
		lbl.Render("Already at destination:"), cyan.Render(fmt.Sprintf("%s files · %s", formatNumber(m.SkippedFiles), formatBytes(m.SkippedBytes))),
		lbl.Render("Check rate:            "), val.Render(rateText),
		lbl.Render("Catch-up ETA:          "), val.Render(etaText),
	)

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#00BFFF")).
		Padding(0, 1).
		Render(body)
}

func renderSummaryBox(m Model) string {
	endTime := m.EndTime
	if endTime.IsZero() {
		endTime = time.Now()
	}
	duration := endTime.Sub(m.StartTime)
	if duration <= 0 {
		duration = time.Millisecond
	}

	// Average speed is measured from the first real transfer, so a long
	// catch-up phase (which moves no bytes) doesn't deflate the figure.
	transferDuration := duration
	if !m.TransferStartTime.IsZero() {
		if d := endTime.Sub(m.TransferStartTime); d > 0 {
			transferDuration = d
		}
	}
	avgSpeedBps := float64(m.UploadedBytes) / transferDuration.Seconds()

	// Styles for Summary Output Box — status depends on how the run ended.
	boxBorderColor := lipgloss.Color("#00FF7F") // Emerald green
	statusBadgeBg := lipgloss.Color("#00FF7F")
	statusBadgeText := lipgloss.Color("#000000")
	statusBadgeIcon := "✔"
	statusTitle := m.t("SYNC COMPLETE ('BYE BYE BYE!')", "SYNC COMPLETE")

	amber := func(title string) {
		boxBorderColor = lipgloss.Color("#FFD700")
		statusBadgeBg = lipgloss.Color("#FFD700")
		statusBadgeText = lipgloss.Color("#000000")
		statusBadgeIcon = "⏹"
		statusTitle = title
	}
	red := func(title string) {
		boxBorderColor = lipgloss.Color("#FF4500")
		statusBadgeBg = lipgloss.Color("#FF4500")
		statusBadgeText = lipgloss.Color("#FFFFFF")
		statusBadgeIcon = "✖"
		statusTitle = title
	}

	switch {
	case m.State == StateCancelled:
		amber(m.t("SYNC CANCELLED ('BYE BYE BYE!' — cut short)", "SYNC CANCELLED"))
	case m.Config.VerifyOnly:
		if m.FailedFiles > 0 {
			red(fmt.Sprintf("VERIFICATION FAILED — %d discrepancy(ies)", m.FailedFiles))
		} else {
			statusTitle = m.t("VERIFICATION PASSED ('N SYNC — all present)", "VERIFICATION PASSED")
		}
	case m.Config.DryRun:
		amber("DRY RUN COMPLETE (no data written)")
	case m.FailedFiles > 0:
		red(m.t("SYNC COMPLETED WITH ERRORS ('TEARIN' UP MY HEART')", "SYNC COMPLETED WITH ERRORS"))
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.DoubleBorder()).
		BorderForeground(boxBorderColor).
		Padding(1, 2).
		MarginBottom(1)

	badgeStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(statusBadgeText).
		Background(statusBadgeBg).
		Padding(0, 1)

	headerTitleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#FAFAFA")).
		MarginLeft(1)

	secHeaderStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#7D56F4")).
		MarginTop(1)

	lblStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#A0A0A0"))

	valStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#FAFAFA"))

	cyanValStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#00BFFF"))

	greenValStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#32CD32"))

	magentaValStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#FF69B4"))

	// Header Banner
	headerBanner := fmt.Sprintf("%s%s",
		badgeStyle.Render(fmt.Sprintf("%s %s", statusBadgeIcon, statusTitle)),
		headerTitleStyle.Render("— TIMBERLAKE EXECUTION REPORT"),
	)

	// Section 1: Target Info
	targetSec := fmt.Sprintf(
		"%s\n  %s %s\n  %s %s\n  %s %s",
		secHeaderStyle.Render(m.t("📍 SYNC TARGETS & CONFIGURATION (\"No Strings Attached\")", "📍 SYNC TARGETS & CONFIGURATION")),
		lblStyle.Render("Source:            "), valStyle.Render(m.sourceLabel()),
		lblStyle.Render("Destination:       "), cyanValStyle.Render(m.destLabel()),
		lblStyle.Render(m.t("Space Cowboys:     ", "Workers:           ")), valStyle.Render(fmt.Sprintf("%d Workers | Chunk Size: %d MiB", m.Config.Jobs, m.Config.PartSizeMB)),
	)

	// Section 2: File Statistics Cards
	cardStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(0, 1).
		Width(24)

	uploadedCard := cardStyle.BorderForeground(lipgloss.Color("#32CD32")).Render(
		fmt.Sprintf("%s\n%s\n%s",
			greenValStyle.Render(m.t("Uploaded ('Bye Bye')", "Uploaded")),
			valStyle.Render(fmt.Sprintf("%s files", formatNumber(m.UploadedFiles))),
			lblStyle.Render(formatBytes(m.UploadedBytes)),
		),
	)

	skippedCard := cardStyle.BorderForeground(lipgloss.Color("#00BFFF")).Render(
		fmt.Sprintf("%s\n%s\n%s",
			cyanValStyle.Render(m.t("Skipped ('N Sync)", "Skipped")),
			valStyle.Render(fmt.Sprintf("%s files", formatNumber(m.SkippedFiles))),
			lblStyle.Render(formatBytes(m.SkippedBytes)),
		),
	)

	failedColor := lipgloss.Color("#5A5A5A")
	failedValStyle := lblStyle
	if m.FailedFiles > 0 {
		failedColor = lipgloss.Color("#FF4500")
		failedValStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FF4500"))
	}
	failedCard := cardStyle.BorderForeground(failedColor).Render(
		fmt.Sprintf("%s\n%s\n%s",
			failedValStyle.Render(m.t("Failed ('Cry Me a River')", "Failed")),
			valStyle.Render(fmt.Sprintf("%s files", formatNumber(m.FailedFiles))),
			lblStyle.Render(fmt.Sprintf("%d errors", m.FailedFiles)),
		),
	)

	cardsRow := lipgloss.JoinHorizontal(lipgloss.Top, uploadedCard, " ", skippedCard, " ", failedCard)

	// Breakout bar
	breakoutBar := ""
	if m.TotalFiles > 0 {
		width := 36
		uW := int(float64(m.UploadedFiles) / float64(m.TotalFiles) * float64(width))
		sW := int(float64(m.SkippedFiles) / float64(m.TotalFiles) * float64(width))
		fW := int(float64(m.FailedFiles) / float64(m.TotalFiles) * float64(width))
		rem := width - (uW + sW + fW)
		if rem > 0 {
			uW += rem
		}
		uBar := greenValStyle.Render(strings.Repeat("█", uW))
		sBar := cyanValStyle.Render(strings.Repeat("█", sW))
		fBar := lipgloss.NewStyle().Foreground(lipgloss.Color("#FF4500")).Render(strings.Repeat("█", fW))

		uPct := float64(m.UploadedFiles) / float64(m.TotalFiles) * 100
		sPct := float64(m.SkippedFiles) / float64(m.TotalFiles) * 100

		breakoutBar = fmt.Sprintf("  %s [%s%s%s] %.1f%% uploaded • %.1f%% skipped",
			lblStyle.Render("Breakout:"),
			uBar, sBar, fBar,
			uPct, sPct,
		)
	}

	statsSec := fmt.Sprintf(
		"%s\n%s\n\n%s",
		secHeaderStyle.Render(m.t("📊 FILE PROCESSING BREAKDOWN (\"Dirty Pop\")", "📊 FILE PROCESSING BREAKDOWN")),
		cardsRow,
		breakoutBar,
	)

	// Section 3: Performance & Throughput
	perfSec := fmt.Sprintf(
		"%s\n  %s %s %s\n  %s %s\n  %s %s\n  %s %s",
		secHeaderStyle.Render(m.t("⚡ PERFORMANCE & THROUGHPUT (\"Can't Stop The Feeling!\")", "⚡ PERFORMANCE & THROUGHPUT")),
		lblStyle.Render("Total Dataset Size:"), valStyle.Render(formatBytes(m.TotalBytes)), lblStyle.Render(fmt.Sprintf("(%s total files scanned)", formatNumber(m.TotalFiles))),
		lblStyle.Render("New Data Transferred:"), greenValStyle.Render(formatBytes(m.UploadedBytes)),
		lblStyle.Render("Elapsed Time:"), valStyle.Render(duration.Round(time.Second).String()),
		lblStyle.Render("Average Upload Speed:"), magentaValStyle.Render(formatSpeed(avgSpeedBps)),
	)

	parts := []string{headerBanner, targetSec, statsSec, perfSec}
	if !m.Config.OutOfSync {
		quoteSec := fmt.Sprintf("\n  %s", lipgloss.NewStyle().Italic(true).Foreground(lipgloss.Color("#FF69B4")).Render("🎤 \"Ain't no lie, baby, Bye Bye Bye! Bringing Sync Back to S3.\""))
		parts = append(parts, quoteSec)
	}
	content := strings.Join(parts, "\n\n")

	return boxStyle.Render(content)
}

// renderBufferBar draws a per-worker progress bar with three filled layers, like
// a video player scrubber, given nested values committed <= uploaded <= buffered:
//
//   - committed (bright): bytes in parts the server has acknowledged (done chunks)
//   - uploading (lilac):  bytes of the chunk(s) currently streaming to the wire
//   - buffered  (dim):    bytes read from disk into memory, ahead of the send
//
// then the empty remainder. The lilac segment is the live edge that fills as a
// single chunk uploads and then "locks in" to the committed colour.
func renderBufferBar(width int, committed, uploaded, buffered, total int64) string {
	if width < 1 {
		width = 1
	}
	emptyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#2E2E38"))
	if total <= 0 {
		return emptyStyle.Render(strings.Repeat("█", width))
	}

	clamp := func(v int64) int64 {
		switch {
		case v < 0:
			return 0
		case v > total:
			return total
		default:
			return v
		}
	}
	// Enforce committed <= uploaded <= buffered <= total.
	committed = clamp(committed)
	uploaded = clamp(uploaded)
	buffered = clamp(buffered)
	if uploaded < committed {
		uploaded = committed
	}
	if buffered < uploaded {
		buffered = uploaded
	}

	cells := func(v int64) int { return int(float64(v) / float64(total) * float64(width)) }
	cCells := cells(committed)
	uCells := cells(uploaded) - cCells
	bCells := cells(buffered) - cCells - uCells
	if cCells+uCells+bCells > width {
		bCells = width - cCells - uCells
	}
	eCells := width - cCells - uCells - bCells

	committedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#7D56F4")) // done chunks
	uploadingStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#B39DFF")) // chunk in flight
	bufferStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#413A5E"))    // read-ahead buffer

	return committedStyle.Render(strings.Repeat("█", cCells)) +
		uploadingStyle.Render(strings.Repeat("█", uCells)) +
		bufferStyle.Render(strings.Repeat("█", bCells)) +
		emptyStyle.Render(strings.Repeat("█", eCells))
}

func formatNumber(n int64) string {
	in := fmt.Sprintf("%d", n)
	out := ""
	for i, c := range in {
		if i > 0 && (len(in)-i)%3 == 0 {
			out += ","
		}
		out += string(c)
	}
	return out
}

// PrintFinalSummary displays the summary to standard stdout after the TUI exits.
func PrintFinalSummary(m Model) {
	fmt.Println(renderSummaryBox(m))
}

func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

func formatSpeed(bytesPerSec float64) string {
	if bytesPerSec <= 0 {
		return "0 B/s"
	}
	return formatBytes(int64(bytesPerSec)) + "/s"
}

func formatETA(remainingBytes int64, bytesPerSec float64) string {
	if remainingBytes <= 0 {
		return "0s"
	}
	if bytesPerSec <= 0 {
		return "--:--"
	}
	seconds := float64(remainingBytes) / bytesPerSec
	d := time.Duration(seconds) * time.Second
	return d.Round(time.Second).String()
}
