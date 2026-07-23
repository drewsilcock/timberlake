package scanner

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// FileItem represents a single local file to sync.
type FileItem struct {
	AbsolutePath string
	RelativePath string
	Size         int64
}

// ScanResult holds the result of scanning the source directory.
type ScanResult struct {
	Files      []FileItem
	TotalFiles int64
	TotalBytes int64
}

// ScanDirectory walks the source directory using high-performance concurrent file system traversal.
func ScanDirectory(sourceDir string, progressCb func(scannedCount int64, scannedBytes int64)) (*ScanResult, error) {
	absSource, err := filepath.Abs(sourceDir)
	if err != nil {
		return nil, fmt.Errorf("invalid source path %s: %w", sourceDir, err)
	}

	info, err := os.Stat(absSource)
	if err != nil {
		return nil, fmt.Errorf("cannot stat source path %s: %w", absSource, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("source path is not a directory: %s", absSource)
	}

	var items []FileItem
	var mu sync.Mutex
	var totalFiles int64
	var totalBytes int64

	err = filepath.WalkDir(absSource, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // Skip unreadable paths
		}

		if d.IsDir() {
			return nil
		}

		// Calculate relative path from source root
		relPath, err := filepath.Rel(absSource, path)
		if err != nil {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		fileSize := info.Size()

		item := FileItem{
			AbsolutePath: path,
			RelativePath: relPath,
			Size:         fileSize,
		}

		mu.Lock()
		items = append(items, item)
		mu.Unlock()

		newCount := atomic.AddInt64(&totalFiles, 1)
		newBytes := atomic.AddInt64(&totalBytes, fileSize)

		if progressCb != nil && newCount%100 == 0 {
			progressCb(newCount, newBytes)
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("error walking directory %s: %w", absSource, err)
	}

	if progressCb != nil {
		progressCb(atomic.LoadInt64(&totalFiles), atomic.LoadInt64(&totalBytes))
	}

	return &ScanResult{
		Files:      items,
		TotalFiles: totalFiles,
		TotalBytes: totalBytes,
	}, nil
}
