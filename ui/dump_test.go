package ui

import (
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"timberlake/config"
	"timberlake/transfer"
	"timberlake/web"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// demoModel builds a Model populated with realistic state, so the dashboard can
// be rendered and inspected without running a sync.
func demoModel(width, height int) Model {
	const jobs = 16
	workers := make([]WorkerState, jobs)
	bars := make([]progress.Model, jobs)
	for i := range workers {
		bars[i] = progress.New(progress.WithSolidFill("#7D56F4"), progress.WithoutPercentage())
		switch {
		case i < 10:
			total := int64(240<<20) + int64(i)<<20
			workers[i] = WorkerState{
				ID: i, Status: "Uploading",
				FileName:      fmt.Sprintf("4_The_Revenge_of_the_Queen/Photogrammetry/DSC0%d.tif", 3600+i),
				TotalSize:     total,
				CommittedSize: total / int64(3+i%4),
				UploadedSize:  total / int64(2+i%3),
				BufferedSize:  total/int64(2+i%3) + 16<<20,
				SpeedBps:      float64(1<<20) * float64(2+i%3),
				StartTime:     time.Now().Add(-time.Duration(40+i*7) * time.Second),
				BytesMoved:    int64(i) * 900 << 20,
				FilesDone:     int64(i * 3),
			}
		case i < 12:
			workers[i] = WorkerState{ID: i, Status: "Checking", FileName: "3_Antechamber/RTI/RTI_18.tif", TotalSize: 210 << 20}
		case i < 13:
			workers[i] = WorkerState{ID: i, Status: "Queued", FileName: "2_West_Passage/Photogrammetry/DSC04120.tif", TotalSize: 250 << 20}
		default:
			workers[i] = WorkerState{ID: i, Status: "Idle"}
		}
	}

	var recent []FileRecord
	for i := 0; i < 40; i++ {
		st := "Done"
		if i%5 == 0 {
			st = "Skipped"
		}
		if i == 7 {
			st = "Error"
		}
		recent = append(recent, FileRecord{
			Name:     fmt.Sprintf("1_The_Kings_Chamber/Photogrammetry/DSC0%d.tif", 3000+i),
			Size:     int64(241<<20) + int64(i),
			Status:   st,
			Duration: time.Duration(60+i) * time.Second,
			At:       time.Now(),
		})
	}

	queue := make([]transfer.Item, 0, 200)
	for i := 0; i < 200; i++ {
		queue = append(queue, transfer.Item{
			RelativePath: fmt.Sprintf("2_West_Passage/Photogrammetry/DSC0%d.tif", 4200+i),
			Size:         int64(238<<20) + int64(i),
		})
	}

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	// A real (loopback) server so the Web panel renders links and a QR code.
	srv, err := web.New("127.0.0.1:0")
	if err == nil {
		_, _ = srv.Start()
	}

	return Model{
		Config: &config.AppConfig{
			SourceDir: "/Volumes/Elements", Destination: "s3://small-grants-photogrammetry/input-data",
			Jobs: jobs, PartSizeMB: 16,
		},
		State:             StateUploading,
		Workers:           workers,
		WorkQueue:         queue,
		QueuePos:          40,
		TotalFiles:        16206,
		UploadedFiles:     884,
		SkippedFiles:      5941,
		FailedFiles:       3,
		TotalBytes:        2_814_749_767_106,
		CompletedBytes:    1_100_000_000_000,
		SkippedBytes:      980_000_000_000,
		SpeedBps:          3.36 * (1 << 20),
		StartTime:         time.Now().Add(-92 * time.Minute),
		TransferStartTime: time.Now().Add(-70 * time.Minute),
		RecentFiles:       recent,
		TriviaList:        NSyncTrivia,
		TotalBytesBar:     progress.New(progress.WithDefaultGradient(), progress.WithoutPercentage()),
		TotalFilesBar:     progress.New(progress.WithSolidFill("#00BFFF"), progress.WithoutPercentage()),
		WorkerBars:        bars,
		Spinner:           sp,
		Viewport:          viewport.New(width, height),
		MsgChan:           make(chan tea.Msg, 8),
		layout:            &layoutBounds{},
		pause:             newPauseGate(),
		Web:               srv,
		Width:             width,
		Height:            height,
	}
}

// TestDumpUI prints the dashboard for each panel so the layout can be reviewed.
// Opt-in:  TL_UI_DUMP=1 go test ./ui -run TestDumpUI -v
// Size:    TL_UI_COLS=160 TL_UI_ROWS=50
func TestDumpUI(t *testing.T) {
	if os.Getenv("TL_UI_DUMP") == "" {
		t.Skip("set TL_UI_DUMP=1 to print the dashboard")
	}
	cols := envInt("TL_UI_COLS", 130)
	rows := envInt("TL_UI_ROWS", 40)

	m := demoModel(cols, rows)
	for _, p := range allPanes {
		m.ActivePane = p
		fmt.Printf("\n===== PANE: %s  (%dx%d, %d lines) =====\n%s\n",
			p.Title(), cols, rows, lipgloss.Height(m.View()), m.View())
	}

	// Also show the zoomed worker detail and the paused header.
	m.ActivePane = PaneTransfers
	m.ZoomWorker = true
	m.SelectedWorker = 3
	fmt.Printf("\n===== PANE: Transfers (zoomed) =====\n%s\n", m.View())
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
