package ui

import (
	"context"
	"sync"
)

// pauseGate lets the UI actually stop the worker pool. Previously "pause" only
// changed a label: the workers never consulted it, so a paused sync carried on
// transferring. Workers now wait on this gate between files.
//
// Pausing takes effect at file boundaries — a transfer already in flight runs to
// completion rather than being torn down, so no partial work is wasted.
type pauseGate struct {
	mu     sync.Mutex
	cond   *sync.Cond
	paused bool
}

func newPauseGate() *pauseGate {
	g := &pauseGate{}
	g.cond = sync.NewCond(&g.mu)
	return g
}

// set pauses or resumes the pool, waking any waiting workers on resume.
func (g *pauseGate) set(paused bool) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.paused = paused
	g.mu.Unlock()
	g.cond.Broadcast()
}

// isPaused reports the current state.
func (g *pauseGate) isPaused() bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.paused
}

// wait blocks while paused, returning false if the context is cancelled first.
func (g *pauseGate) wait(ctx context.Context) bool {
	if g == nil {
		return true
	}
	g.mu.Lock()
	if !g.paused {
		g.mu.Unlock()
		return ctx.Err() == nil
	}

	// Wake the sleeper if the run is cancelled while paused, so quitting from a
	// paused state doesn't hang.
	stop := context.AfterFunc(ctx, func() { g.cond.Broadcast() })
	defer stop()

	for g.paused && ctx.Err() == nil {
		g.cond.Wait()
	}
	g.mu.Unlock()
	return ctx.Err() == nil
}
