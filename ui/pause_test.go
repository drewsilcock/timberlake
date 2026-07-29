package ui

import (
	"context"
	"testing"
	"time"
)

// TestPauseGateBlocksAndReleases: pause must actually stop workers, not just
// relabel the UI.
func TestPauseGateBlocksAndReleases(t *testing.T) {
	g := newPauseGate()
	ctx := context.Background()

	if !g.wait(ctx) {
		t.Fatal("unpaused gate should pass straight through")
	}

	g.set(true)
	if !g.isPaused() {
		t.Fatal("gate should report paused")
	}

	released := make(chan bool, 1)
	go func() { released <- g.wait(ctx) }()

	select {
	case <-released:
		t.Fatal("wait returned while paused; workers would keep running")
	case <-time.After(150 * time.Millisecond):
	}

	g.set(false)
	select {
	case ok := <-released:
		if !ok {
			t.Error("wait should report success after resume")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("resume did not release the waiting worker")
	}
}

// TestPauseGateReleasesOnCancel: quitting while paused must not hang.
func TestPauseGateReleasesOnCancel(t *testing.T) {
	g := newPauseGate()
	g.set(true)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan bool, 1)
	go func() { done <- g.wait(ctx) }()

	cancel()
	select {
	case ok := <-done:
		if ok {
			t.Error("wait should report failure when the run is cancelled")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelling while paused hung")
	}
}

// TestPauseKeyDrivesTheGate wires the keypress to the gate.
func TestPauseKeyDrivesTheGate(t *testing.T) {
	m := newTestModel(2, 10, 100)
	if m.pause.isPaused() {
		t.Fatal("should start unpaused")
	}
	m = step(m, key("p"))
	if m.State != StatePaused || !m.pause.isPaused() {
		t.Errorf("after p: state=%v gatePaused=%v, want paused/true", m.State, m.pause.isPaused())
	}
	m = step(m, key("p"))
	if m.State == StatePaused || m.pause.isPaused() {
		t.Errorf("after second p: state=%v gatePaused=%v, want running/false", m.State, m.pause.isPaused())
	}
}
