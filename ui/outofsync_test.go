package ui

import (
	"strings"
	"testing"
	"time"

	"timberlake/config"
)

var nsyncPhrases = []string{
	"Bye Bye", "Cowboy", "Dirty Pop", "Cry Me", "'N Sync", "No Strings",
	"Ain't no lie", "Can't Stop", "TEARIN", "Bringing Sync", "Drive Myself",
	"Gonna Be Me",
}

func summaryModel(cfg *config.AppConfig) Model {
	return Model{
		Config:        cfg,
		State:         StateDone,
		TotalFiles:    10,
		UploadedFiles: 8,
		SkippedFiles:  2,
		UploadedBytes: 1 << 20,
		StartTime:     time.Now().Add(-time.Minute),
		EndTime:       time.Now(),
	}
}

func TestOutOfSyncSummaryStripsReferences(t *testing.T) {
	out := renderSummaryBox(summaryModel(&config.AppConfig{OutOfSync: true, Jobs: 4, PartSizeMB: 16}))
	for _, p := range nsyncPhrases {
		if strings.Contains(out, p) {
			t.Errorf("out-of-sync summary still contains %q", p)
		}
	}
}

func TestThemedSummaryKeepsReferences(t *testing.T) {
	// Sanity: with the flag off, the flavour is present.
	out := renderSummaryBox(summaryModel(&config.AppConfig{OutOfSync: false, Jobs: 4, PartSizeMB: 16}))
	if !strings.Contains(out, "Bye Bye") {
		t.Error("themed summary should contain 'N SYNC references")
	}
}

func TestThemeHelper(t *testing.T) {
	on := Model{Config: &config.AppConfig{OutOfSync: true}}
	if got := on.t("fancy", "plain"); got != "plain" {
		t.Errorf("out-of-sync t() = %q, want plain", got)
	}
	off := Model{Config: &config.AppConfig{OutOfSync: false}}
	if got := off.t("fancy", "plain"); got != "fancy" {
		t.Errorf("themed t() = %q, want fancy", got)
	}
}
