package ui

import (
	"context"
	"fmt"
	"time"

	"timberlake/s3client"
	"timberlake/scanner"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.Cancel()
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

		if len(m.TriviaList) > 0 && time.Since(m.LastTriviaUpdate) >= 5*time.Second {
			m.TriviaIndex = (m.TriviaIndex + 1) % len(m.TriviaList)
			m.LastTriviaUpdate = time.Now()
		}

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

		// Start worker pool
		startWorkerPool(&m)
		cmds = append(cmds, waitForMsgCmd(m.MsgChan))

	case WorkerProgressMsg:
		m.UploadedBytes += msg.BytesDelta
		if msg.WorkerID >= 0 && msg.WorkerID < len(m.Workers) {
			w := &m.Workers[msg.WorkerID]
			w.UploadedSize = msg.UploadedSize
			w.TotalSize = msg.TotalSize
			if msg.FileName != "" {
				w.FileName = msg.FileName
			}

			if w.TotalSize > 0 {
				ratio := float64(w.UploadedSize) / float64(w.TotalSize)
				cmd := m.WorkerBars[msg.WorkerID].SetPercent(ratio)
				cmds = append(cmds, cmd)
			}
		}

		if m.TotalBytes > 0 {
			ratio := float64(m.UploadedBytes+m.SkippedBytes) / float64(m.TotalBytes)
			cmd := m.TotalBytesBar.SetPercent(ratio)
			cmds = append(cmds, cmd)
		}

		elapsed := time.Since(m.StartTime).Seconds()
		if elapsed > 0 {
			m.SpeedBps = float64(m.UploadedBytes) / elapsed
		}

		cmds = append(cmds, waitForMsgCmd(m.MsgChan))

	case WorkerStatusMsg:
		if msg.WorkerID >= 0 && msg.WorkerID < len(m.Workers) {
			w := &m.Workers[msg.WorkerID]
			w.Status = msg.Status
			w.FileName = msg.FileName
			w.LastError = msg.Err

			switch msg.Status {
			case "Done":
				m.UploadedFiles++
				w.Status = "Idle"
				w.UploadedSize = 0
				w.TotalSize = 0
				w.FileName = ""
			case "Skipped":
				m.SkippedFiles++
				m.SkippedBytes += msg.Size
				w.Status = "Idle"
				w.UploadedSize = 0
				w.TotalSize = 0
				w.FileName = ""
			case "Error":
				m.FailedFiles++
			}
		}

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

				exists, err := m.S3Client.CheckObjectExists(context.Background(), m.Config.Bucket, key, item.Size)
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

				var uploadedForThisFile int64
				err = m.S3Client.UploadFile(context.Background(), item.AbsolutePath, m.Config.Bucket, key, func(bytesRead int) {
					uploadedForThisFile += int64(bytesRead)
					m.MsgChan <- WorkerProgressMsg{
						WorkerID:     workerID,
						BytesDelta:   int64(bytesRead),
						FileName:     item.RelativePath,
						UploadedSize: uploadedForThisFile,
						TotalSize:    item.Size,
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
