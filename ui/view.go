package ui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
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

	// State-specific layout
	switch m.State {
	case StateScanning:
		b.WriteString(panelStyle.Render(fmt.Sprintf(
			"%s Scanning source%s\nSource: %s",
			m.Spinner.View(),
			m.t(" tree... ('It's Gonna Be Me!')", "..."),
			statusScanningStyle.Render(m.sourceLabel()),
		)))

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
		for i, w := range m.Workers {
			wIDStr := fmt.Sprintf("#%02d", i+1)
			if w.Status == "Idle" {
				workerLines = append(workerLines, fmt.Sprintf("%s  %-6s  %s",
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
			case "Error":
				statusBadge = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF")).Background(lipgloss.Color("#FF4500")).Render(m.t(" TEAR ", " ERR "))
			}

			shortName := filepath.Base(w.FileName)
			if len(shortName) > 24 {
				shortName = shortName[:21] + "..."
			}

			line := fmt.Sprintf("%s  %s [%s] %5.1f%% %-24s (%s / %s)",
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
	}

	// Footer Help Text
	b.WriteString("\n")
	b.WriteString(helpStyle.Render(m.t(
		"Controls: [p/Space] Pause/Resume (\"Drive Myself Crazy\")  [↑/↓/k/j] Scroll Cowboys  [q] Quit (\"Bye Bye Bye!\")",
		"Controls: [p/Space] Pause/Resume  [↑/↓/k/j] Scroll  [q] Quit")))

	return b.String()
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

	avgSpeedBps := float64(m.UploadedBytes) / duration.Seconds()

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
