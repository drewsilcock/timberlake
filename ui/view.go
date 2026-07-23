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

func (m Model) View() string {
	var b strings.Builder

	// Header
	headerText := fmt.Sprintf("%s %s",
		titleStyle.Render("TIMBERLAKE"),
		subtitleStyle.Render(fmt.Sprintf("Source: %s ➔ s3://%s/%s 🎶 No Strings Attached", m.Config.SourceDir, m.Config.Bucket, m.Config.Prefix)),
	)
	b.WriteString(headerBoxStyle.Render(headerText))
	b.WriteString("\n")

	// State-specific layout
	switch m.State {
	case StateScanning:
		b.WriteString(panelStyle.Render(fmt.Sprintf(
			"%s Scanning local directory tree... ('It's Gonna Be Me!')\nSource: %s",
			m.Spinner.View(),
			statusScanningStyle.Render(m.Config.SourceDir),
		)))

	case StateUploading, StatePaused, StateDone, StateError:
		if m.State == StateDone {
			b.WriteString(renderSummaryBox(m))
			b.WriteString("\n")
			b.WriteString(helpStyle.Render("Sync complete! Ain't no lie, Bye Bye Bye! 👋 Press [q] to exit."))
			return b.String()
		}

		// Total Data Progress Bar
		totalBytesRatio := float64(0)
		if m.TotalBytes > 0 {
			totalBytesRatio = float64(m.UploadedBytes+m.SkippedBytes) / float64(m.TotalBytes)
		}
		if totalBytesRatio > 1.0 {
			totalBytesRatio = 1.0
		}

		speedText := formatSpeed(m.SpeedBps)
		etaText := formatETA(m.TotalBytes-(m.UploadedBytes+m.SkippedBytes), m.SpeedBps)

		dataPanel := fmt.Sprintf(
			"DIRTY POP (DATA) [%s] %5.1f%%\n%s / %s (Speed: %s | ETA: %s)",
			m.TotalBytesBar.ViewAs(totalBytesRatio),
			totalBytesRatio*100,
			formatBytes(m.UploadedBytes+m.SkippedBytes),
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
			"IT'S GONNA BE ME [%s] %5.1f%%\n%d / %d Files (%d Uploaded, %d Skipped, %d Failed)",
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
			statusBadge = statusUploadingStyle.Render("UPLOADING (\"CAN'T STOP THE FEELING!\")")
		case StatePaused:
			statusBadge = statusPausedStyle.Render("PAUSED (\"DRIVE MYSELF CRAZY\")")
		case StateError:
			statusBadge = statusErrorStyle.Render("ERROR (\"TEARIN' UP MY HEART\") " + m.ErrorMessage)
		}

		b.WriteString(panelStyle.Render(fmt.Sprintf(
			"Status: %s | Space Cowboys: %d | Part Size: %d MiB\n\n%s\n\n%s",
			statusBadge,
			m.Config.Jobs,
			m.Config.PartSizeMB,
			dataPanel,
			filesPanel,
		)))
		b.WriteString("\n")

		// 'N SYNC Trivia Box
		if len(m.TriviaList) > 0 {
			triviaFact := m.TriviaList[m.TriviaIndex%len(m.TriviaList)]
			triviaHeader := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FF69B4")).Render(fmt.Sprintf("💡 'N SYNC TRIVIA BREAK (%d/%d)", m.TriviaIndex+1, len(m.TriviaList)))
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
				statusBadge = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#000000")).Background(lipgloss.Color("#32CD32")).Render(" POP! ")
			case "Checking":
				statusBadge = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#000000")).Background(lipgloss.Color("#FFD700")).Render(" MAY! ")
			case "Error":
				statusBadge = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF")).Background(lipgloss.Color("#FF4500")).Render(" TEAR ")
			}

			shortName := filepath.Base(w.FileName)
			if len(shortName) > 24 {
				shortName = shortName[:21] + "..."
			}

			line := fmt.Sprintf("%s  %s [%s] %5.1f%% %-24s (%s / %s)",
				lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7D56F4")).Render(wIDStr),
				statusBadge,
				m.WorkerBars[i].ViewAs(wRatio),
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
			Render(fmt.Sprintf("⚙ SPACE COWBOYS & ACTIVE TRANSFERS (%d/%d Active)", activeCount, len(m.Workers)))

		b.WriteString(workersBoxHeader)
		b.WriteString("\n")
		b.WriteString(workersBoxStyle.Render(m.Viewport.View()))
	}

	// Footer Help Text
	b.WriteString("\n")
	b.WriteString(helpStyle.Render("Controls: [p/Space] Pause/Resume (\"Drive Myself Crazy\")  [↑/↓/k/j] Scroll Cowboys  [q] Quit (\"Bye Bye Bye!\")"))

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

	// Styles for Summary Output Box
	boxBorderColor := lipgloss.Color("#00FF7F") // Emerald green
	statusBadgeBg := lipgloss.Color("#00FF7F")
	statusBadgeText := lipgloss.Color("#000000")
	statusBadgeIcon := "✔"
	statusTitle := "SYNC COMPLETE ('BYE BYE BYE!')"

	if m.FailedFiles > 0 {
		boxBorderColor = lipgloss.Color("#FF4500")
		statusBadgeBg = lipgloss.Color("#FF4500")
		statusBadgeText = lipgloss.Color("#FFFFFF")
		statusBadgeIcon = "✖"
		statusTitle = "SYNC COMPLETED WITH ERRORS ('TEARIN' UP MY HEART')"
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
		secHeaderStyle.Render("📍 SYNC TARGETS & CONFIGURATION (\"No Strings Attached\")"),
		lblStyle.Render("Source Directory:  "), valStyle.Render(m.Config.SourceDir),
		lblStyle.Render("S3 Destination:    "), cyanValStyle.Render(fmt.Sprintf("s3://%s/%s", m.Config.Bucket, m.Config.Prefix)),
		lblStyle.Render("Space Cowboys:     "), valStyle.Render(fmt.Sprintf("%d Workers | Chunk Size: %d MiB", m.Config.Jobs, m.Config.PartSizeMB)),
	)

	// Section 2: File Statistics Cards
	cardStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(0, 1).
		Width(24)

	uploadedCard := cardStyle.BorderForeground(lipgloss.Color("#32CD32")).Render(
		fmt.Sprintf("%s\n%s\n%s",
			greenValStyle.Render("Uploaded ('Bye Bye')"),
			valStyle.Render(fmt.Sprintf("%s files", formatNumber(m.UploadedFiles))),
			lblStyle.Render(formatBytes(m.UploadedBytes)),
		),
	)

	skippedCard := cardStyle.BorderForeground(lipgloss.Color("#00BFFF")).Render(
		fmt.Sprintf("%s\n%s\n%s",
			cyanValStyle.Render("Skipped ('N Sync)"),
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
			failedValStyle.Render("Failed ('Cry Me a River')"),
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
		secHeaderStyle.Render("📊 FILE PROCESSING BREAKDOWN (\"Dirty Pop\")"),
		cardsRow,
		breakoutBar,
	)

	// Section 3: Performance & Throughput
	perfSec := fmt.Sprintf(
		"%s\n  %s %s %s\n  %s %s\n  %s %s\n  %s %s",
		secHeaderStyle.Render("⚡ PERFORMANCE & THROUGHPUT (\"Can't Stop The Feeling!\")"),
		lblStyle.Render("Total Dataset Size:"), valStyle.Render(formatBytes(m.TotalBytes)), lblStyle.Render(fmt.Sprintf("(%s total files scanned)", formatNumber(m.TotalFiles))),
		lblStyle.Render("New Data Transferred:"), greenValStyle.Render(formatBytes(m.UploadedBytes)),
		lblStyle.Render("Elapsed Time:"), valStyle.Render(duration.Round(time.Second).String()),
		lblStyle.Render("Average Upload Speed:"), magentaValStyle.Render(formatSpeed(avgSpeedBps)),
	)

	quoteSec := fmt.Sprintf("\n  %s", lipgloss.NewStyle().Italic(true).Foreground(lipgloss.Color("#FF69B4")).Render("🎤 \"Ain't no lie, baby, Bye Bye Bye! Bringing Sync Back to Ceph S3.\""))

	var parts []string
	parts = append(parts, headerBanner, targetSec, statsSec, perfSec, quoteSec)
	content := strings.Join(parts, "\n\n")

	return boxStyle.Render(content)
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
