package ui

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"timberlake/config"
	"timberlake/transfer"
	"timberlake/transfer/localfs"

	tea "github.com/charmbracelet/bubbletea"
)

// runWorkers drives startWorkerPool over a source/dest and returns the terminal
// status per file.
func runWorkers(t *testing.T, cfg *config.AppConfig, src transfer.Source, dst transfer.Destination) map[string]string {
	t.Helper()
	ctx := context.Background()
	items, err := src.Scan(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := Model{
		Config:    cfg,
		Source:    src,
		Dest:      dst,
		Ctx:       ctx,
		MsgChan:   make(chan tea.Msg, 256),
		WorkQueue: items,
	}
	startWorkerPool(&m)

	terminal := make(map[string]string)
	deadline := time.After(10 * time.Second)
	for len(terminal) < len(items) {
		select {
		case msg := <-m.MsgChan:
			if s, ok := msg.(WorkerStatusMsg); ok {
				switch s.Status {
				case "Done", "Skipped", "Error":
					terminal[s.FileName] = s.Status
				}
			}
		case <-deadline:
			t.Fatalf("timed out; have %d/%d terminals", len(terminal), len(items))
		}
	}
	return terminal
}

func countStatuses(m map[string]string) (done, skipped, errored int) {
	for _, s := range m {
		switch s {
		case "Done":
			done++
		case "Skipped":
			skipped++
		case "Error":
			errored++
		}
	}
	return
}

func TestDryRunWritesNothing(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	for _, n := range []string{"a.bin", "b.bin", "c.bin"} {
		if err := os.WriteFile(filepath.Join(srcDir, n), []byte("data-"+n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	src, _ := localfs.New(srcDir)
	dst, _ := localfs.New(dstDir)

	res := runWorkers(t, &config.AppConfig{Jobs: 3, DryRun: true}, src, dst)
	done, skipped, errored := countStatuses(res)
	if done != 3 || skipped != 0 || errored != 0 {
		t.Errorf("dry-run statuses done=%d skipped=%d error=%d, want 3/0/0", done, skipped, errored)
	}
	if entries, _ := os.ReadDir(dstDir); len(entries) != 0 {
		t.Errorf("dry-run must not write to destination, found %d entries", len(entries))
	}
}

func TestVerifyOnlyReportsDiscrepancies(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	for _, n := range []string{"present.bin", "missing.bin", "wrongsize.bin"} {
		if err := os.WriteFile(filepath.Join(srcDir, n), []byte("the-real-"+n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Destination: one correct, one wrong size, one absent.
	if err := os.WriteFile(filepath.Join(dstDir, "present.bin"), []byte("the-real-present.bin"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dstDir, "wrongsize.bin"), []byte("short"), 0o644); err != nil {
		t.Fatal(err)
	}
	src, _ := localfs.New(srcDir)
	dst, _ := localfs.New(dstDir)

	res := runWorkers(t, &config.AppConfig{Jobs: 3, VerifyOnly: true}, src, dst)
	done, skipped, errored := countStatuses(res)
	if done != 0 || skipped != 1 || errored != 2 {
		t.Errorf("verify statuses done=%d skipped=%d error=%d, want 0/1/2", done, skipped, errored)
	}
	if res["present.bin"] != "Skipped" {
		t.Errorf("present.bin should verify OK (Skipped), got %s", res["present.bin"])
	}
}
