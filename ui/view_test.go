package ui

import (
	"fmt"
	"testing"
	"time"

	"timberlake/config"
)

func TestRenderSummaryBox(t *testing.T) {
	appCfg := &config.AppConfig{
		SourceDir:   "/data/photogrammetry/site-001",
		Destination: "s3://mybucket/scans/site-001",
		Bucket:      "mybucket",
		Prefix:      "scans/site-001",
		Jobs:        16,
		PartSizeMB:  256,
	}

	m := Model{
		Config:        appCfg,
		TotalFiles:    1690,
		UploadedFiles: 1240,
		SkippedFiles:  450,
		FailedFiles:   0,
		TotalBytes:    6442450944, // 6 GB
		UploadedBytes: 4509715456, // 4.2 GB
		SkippedBytes:  1932735488, // 1.8 GB
		StartTime:     time.Now().Add(-84 * time.Second),
		EndTime:       time.Now(),
	}

	summary := renderSummaryBox(m)
	fmt.Println("--- TEST RENDER SUMMARY BOX OUTPUT ---")
	fmt.Println(summary)
	fmt.Println("--------------------------------------")
}
