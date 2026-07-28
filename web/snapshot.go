// Package web serves a read-only live progress page for a running sync, over
// the LAN by default and optionally through a Cloudflare quick tunnel.
package web

import (
	"sync/atomic"
	"time"
)

// WorkerSnapshot is one worker row as shown on the page.
type WorkerSnapshot struct {
	ID        int    `json:"id"`
	Status    string `json:"status"`
	File      string `json:"file"`
	Committed int64  `json:"committed"`
	Uploaded  int64  `json:"uploaded"`
	Buffered  int64  `json:"buffered"`
	Total     int64  `json:"total"`
}

// HistorySnapshot is one finished item.
type HistorySnapshot struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Status  string `json:"status"`
	Seconds int    `json:"seconds"`
}

// Snapshot is an immutable view of sync progress, published by the UI and
// broadcast to connected browsers. It deliberately carries no credentials.
type Snapshot struct {
	Phase       string            `json:"phase"`
	Source      string            `json:"source"`
	Destination string            `json:"destination"`
	TotalFiles  int64             `json:"totalFiles"`
	Uploaded    int64             `json:"uploadedFiles"`
	Skipped     int64             `json:"skippedFiles"`
	Failed      int64             `json:"failedFiles"`
	TotalBytes  int64             `json:"totalBytes"`
	SentBytes   int64             `json:"sentBytes"`
	SkipBytes   int64             `json:"skippedBytes"`
	SpeedBps    float64           `json:"speedBps"`
	ElapsedSec  int               `json:"elapsedSec"`
	ETASec      int               `json:"etaSec"`
	Workers     []WorkerSnapshot  `json:"workers"`
	Recent      []HistorySnapshot `json:"recent"`
	Boring      bool              `json:"boring"` // --out-of-sync
	UpdatedAt   int64             `json:"updatedAt"`
}

// store holds the latest snapshot for new subscribers.
type store struct{ v atomic.Pointer[Snapshot] }

func (s *store) set(snap Snapshot) {
	snap.UpdatedAt = time.Now().UnixMilli()
	s.v.Store(&snap)
}

func (s *store) get() Snapshot {
	if p := s.v.Load(); p != nil {
		return *p
	}
	return Snapshot{Phase: "starting"}
}
