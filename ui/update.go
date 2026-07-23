package ui

import (
	"fmt"
	"time"

	"timberlake/s3client"
	"timberlake/scanner"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

// How long each 'N SYNC trivia fact stays on screen before rotating.
const triviaDisplayDuration = 45 * time.Second

// resetWorker returns a worker slot to idle, clearing per-file progress.
func resetWorker(w *WorkerState) {
	w.Status = "Idle"
	w.CommittedSize = 0
	w.UploadedSize = 0
	w.BufferedSize = 0
	w.TotalSize = 0
	w.FileName = ""
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.Cancel()
			// Only a genuinely finished run counts as success. Quitting while
			// work remains is a cancellation, not a completion.
			if m.State != StateDone && m.State != StateError {
				m.State = StateCancelled
			}
			if m.EndTime.IsZero() {
				m.EndTime = time.Now()
			}
			return m, tea.Quit

		case "p", " ":
			switch m.State {
			case StateUploading:
				m.State = StatePaused
			case StatePaused:
				m.State = StateUploading
			}

		case "up", "k":
			m.Viewport.ScrollUp(1)

		case "down", "j":
			m.Viewport.ScrollDown(1)
		}

	case tea.WindowSizeMsg:
		m.Width = msg.Width
		m.Height = msg.Height
		m.TotalBytesBar.Width = msg.Width - 30
		m.TotalFilesBar.Width = msg.Width - 30
		if m.TotalBytesBar.Width < 20 {
			m.TotalBytesBar.Width = 20
		}
		if m.TotalFilesBar.Width < 20 {
			m.TotalFilesBar.Width = 20
		}
		for i := range m.WorkerBars {
			m.WorkerBars[i].Width = msg.Width - 45
			if m.WorkerBars[i].Width < 15 {
				m.WorkerBars[i].Width = 15
			}
		}
		m.Viewport.Width = msg.Width - 4
		m.Viewport.Height = msg.Height - 20
		if m.Viewport.Height < 4 {
			m.Viewport.Height = 4
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.Spinner, cmd = m.Spinner.Update(msg)
		cmds = append(cmds, cmd)

		if len(m.TriviaList) > 0 && time.Since(m.LastTriviaUpdate) >= triviaDisplayDuration {
			m.TriviaIndex = (m.TriviaIndex + 1) % len(m.TriviaList)
			m.LastTriviaUpdate = time.Now()
		}

		m.sampleSpeed()

	case ScanCompleteMsg:
		if msg.Err != nil {
			m.State = StateError
			m.ErrorMessage = msg.Err.Error()
			m.EndTime = time.Now()
			return m, nil
		}

		m.ScanResult = msg.Result
		m.TotalFiles = msg.Result.TotalFiles
		m.TotalBytes = msg.Result.TotalBytes
		m.WorkQueue = msg.Result.Files
		m.State = StateUploading
		m.StartTime = time.Now()

		// Initialize S3 Client
		s3Client, err := s3client.NewS3Client(m.Ctx, m.Config)
		if err != nil {
			m.State = StateError
			m.ErrorMessage = fmt.Sprintf("Failed to initialize S3 client: %v", err)
			m.EndTime = time.Now()
			return m, nil
		}
		m.S3Client = s3Client

		// Start worker pool. The single message-channel consumer armed in
		// Init() keeps pumping worker messages from here on — we must not arm
		// a second one, or two goroutines would race to drain the channel.
		startWorkerPool(&m)

	case WorkerProgressMsg:
		if msg.WorkerID >= 0 && msg.WorkerID < len(m.Workers) {
			w := &m.Workers[msg.WorkerID]
			w.CommittedSize = msg.CommittedSize
			w.UploadedSize = msg.UploadedSize
			w.BufferedSize = msg.BufferedSize
			w.TotalSize = msg.TotalSize
			if msg.FileName != "" {
				w.FileName = msg.FileName
			}
		}

		m.recalcUploadedBytes()
		cmds = append(cmds, m.TotalBytesBar.SetPercent(m.totalBarRatio()))
		// Speed is sampled on the spinner tick (rolling window), not here.
		// Per-worker bars are rendered directly from Uploaded/Buffered in View.

		cmds = append(cmds, waitForMsgCmd(m.MsgChan))

	case WorkerStatusMsg:
		if msg.WorkerID >= 0 && msg.WorkerID < len(m.Workers) {
			w := &m.Workers[msg.WorkerID]
			w.Status = msg.Status
			w.FileName = msg.FileName
			w.LastError = msg.Err

			switch msg.Status {
			case "Uploading":
				// New file starting: seed the bar denominator, reset progress.
				w.TotalSize = msg.Size
				w.CommittedSize = 0
				w.UploadedSize = 0
				w.BufferedSize = 0
			case "Done":
				m.UploadedFiles++
				m.CompletedBytes += msg.Size
				resetWorker(w)
			case "Skipped":
				m.SkippedFiles++
				m.SkippedBytes += msg.Size
				resetWorker(w)
			case "Error":
				m.FailedFiles++
				m.Errors = append(m.Errors, FileError{RelativePath: msg.FileName, Message: msg.Err})
				resetWorker(w)
			}
		}

		m.recalcUploadedBytes()
		cmds = append(cmds, m.TotalBytesBar.SetPercent(m.totalBarRatio()))

		if m.TotalFiles > 0 {
			ratio := float64(m.UploadedFiles+m.SkippedFiles+m.FailedFiles) / float64(m.TotalFiles)
			cmd := m.TotalFilesBar.SetPercent(ratio)
			cmds = append(cmds, cmd)
		}

		if m.UploadedFiles+m.SkippedFiles+m.FailedFiles >= m.TotalFiles && m.TotalFiles > 0 {
			m.State = StateDone
			if m.EndTime.IsZero() {
				m.EndTime = time.Now()
			}
		} else {
			cmds = append(cmds, waitForMsgCmd(m.MsgChan))
		}

	case progress.FrameMsg:
		progressModel, cmd := m.TotalBytesBar.Update(msg)
		m.TotalBytesBar = progressModel.(progress.Model)
		cmds = append(cmds, cmd)

		progressModelFiles, cmdFiles := m.TotalFilesBar.Update(msg)
		m.TotalFilesBar = progressModelFiles.(progress.Model)
		cmds = append(cmds, cmdFiles)
	}

	return m, tea.Batch(cmds...)
}

// recalcUploadedBytes sets the headline "uploaded" figure to bytes that are
// actually committed to the server (fully-completed files only).
//
// It deliberately does NOT include in-flight worker progress. The upload
// library's progress callback fires as bytes are *read from local disk* into
// part buffers, which races far ahead of the *network upload* — on a slow link
// a worker's read hits 100% almost instantly while the object is still being
// sent. Counting that read-ahead made the speed/ETA wildly optimistic and the
// bars look "stuck at 100%". Per-worker bars still show read progress as an
// activity indicator, but overall speed/ETA/transferred reflect real uploads.
func (m *Model) recalcUploadedBytes() {
	m.UploadedBytes = m.CompletedBytes
}

// liveTransferredBytes estimates bytes actually pushed to the server right now:
// fully-committed files plus the current progress of in-flight uploads. Unlike
// the committed-only UploadedBytes, this keeps moving while large files are
// mid-upload, so live speed/ETA don't read zero. It excludes skipped bytes
// (those were never transferred).
func (m Model) liveTransferredBytes() int64 {
	total := m.CompletedBytes
	for i := range m.Workers {
		if m.Workers[i].Status == "Uploading" {
			total += m.Workers[i].UploadedSize
		}
	}
	return total
}

// totalBarRatio is the overall dataset-completion fraction for the data bar:
// transferred + skipped, clamped to [0,1].
func (m Model) totalBarRatio() float64 {
	if m.TotalBytes <= 0 {
		return 0
	}
	r := float64(m.liveTransferredBytes()+m.SkippedBytes) / float64(m.TotalBytes)
	if r > 1 {
		r = 1
	}
	return r
}

const (
	// The upload library reads files into part buffers ahead of the network
	// send; at startup those buffers fill from fast local disk, causing a brief
	// throughput spike. We skip the first few seconds so that transient never
	// enters the rolling window, then measure over a trailing window.
	speedWarmup    = 3 * time.Second
	speedSampleGap = 750 * time.Millisecond
	speedWindow    = 12 * time.Second
)

// sampleSpeed records a throughput sample and recomputes SpeedBps as a rolling
// average over the trailing window. This avoids both the startup spike (warmup)
// and the "drops to 0" problem of an average-since-start figure when no whole
// file has finished yet.
func (m *Model) sampleSpeed() {
	now := time.Now()
	if now.Sub(m.StartTime) < speedWarmup {
		return
	}
	if !m.lastSpeedTime.IsZero() && now.Sub(m.lastSpeedTime) < speedSampleGap {
		return
	}
	m.lastSpeedTime = now
	m.speedSamples = append(m.speedSamples, speedSample{at: now, bytes: m.liveTransferredBytes()})

	cutoff := now.Add(-speedWindow)
	drop := 0
	for drop < len(m.speedSamples)-1 && m.speedSamples[drop].at.Before(cutoff) {
		drop++
	}
	m.speedSamples = m.speedSamples[drop:]

	if len(m.speedSamples) >= 2 {
		first := m.speedSamples[0]
		last := m.speedSamples[len(m.speedSamples)-1]
		if span := last.at.Sub(first.at).Seconds(); span >= 2 {
			if bps := float64(last.bytes-first.bytes) / span; bps >= 0 {
				m.SpeedBps = bps
			}
		}
	}
}

func startWorkerPool(m *Model) {
	fileChan := make(chan scanner.FileItem, len(m.WorkQueue))
	for _, f := range m.WorkQueue {
		fileChan <- f
	}
	close(fileChan)

	for i := 0; i < m.Config.Jobs; i++ {
		workerID := i
		go func() {
			for item := range fileChan {
				if m.Ctx.Err() != nil {
					return
				}

				key := s3client.BuildKey(m.Config.Prefix, item.RelativePath)

				// Step 1: Check if object already exists
				m.MsgChan <- WorkerStatusMsg{
					WorkerID: workerID,
					Status:   "Checking",
					FileName: item.RelativePath,
					Size:     item.Size,
				}

				exists, err := m.S3Client.CheckObjectExists(m.Ctx, m.Config.Bucket, key, item.Size)
				if err == nil && exists {
					m.MsgChan <- WorkerStatusMsg{
						WorkerID: workerID,
						Status:   "Skipped",
						FileName: item.RelativePath,
						Size:     item.Size,
					}
					continue
				}

				// Step 2: Upload file
				m.MsgChan <- WorkerStatusMsg{
					WorkerID: workerID,
					Status:   "Uploading",
					FileName: item.RelativePath,
					Size:     item.Size,
				}

				// onProgress is already rate-limited (ticked inside UploadFile)
				// and reports absolute values, so we just forward it
				// non-blocking — the upload must never stall on the UI.
				err = m.S3Client.UploadFile(m.Ctx, item.AbsolutePath, m.Config.Bucket, key, func(committed, uploaded, buffered int64) {
					select {
					case m.MsgChan <- WorkerProgressMsg{
						WorkerID:      workerID,
						FileName:      item.RelativePath,
						CommittedSize: committed,
						UploadedSize:  uploaded,
						BufferedSize:  buffered,
						TotalSize:     item.Size,
					}:
					default:
					}
				})

				if err != nil {
					m.MsgChan <- WorkerStatusMsg{
						WorkerID: workerID,
						Status:   "Error",
						FileName: item.RelativePath,
						Err:      err.Error(),
						Size:     item.Size,
					}
				} else {
					m.MsgChan <- WorkerStatusMsg{
						WorkerID: workerID,
						Status:   "Done",
						FileName: item.RelativePath,
						Size:     item.Size,
					}
				}
			}
		}()
	}
}
