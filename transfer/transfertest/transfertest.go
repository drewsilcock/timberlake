// Package transfertest provides helpers for end-to-end backend tests: building
// source trees, running a full Scan→Stat→Open→Put sync, and verifying that a
// destination received byte-identical content.
package transfertest

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"timberlake/transfer"
)

// Tree describes the expected content of a set of relative paths by checksum.
type Tree map[string][sha256.Size]byte

// MakeTree writes `count` random files (sizes in [minBytes,maxBytes]) under dir
// and returns their relative-path→SHA256 map.
func MakeTree(t *testing.T, dir string, count, minBytes, maxBytes int) Tree {
	t.Helper()
	rng := rand.New(rand.NewSource(1)) //nolint:gosec // deterministic test data
	tree := make(Tree)
	for i := 0; i < count; i++ {
		rel := fmt.Sprintf("sub%d/file_%02d.bin", i%3, i)
		abs := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		size := minBytes
		if maxBytes > minBytes {
			size += rng.Intn(maxBytes - minBytes)
		}
		buf := make([]byte, size)
		rng.Read(buf)
		if err := os.WriteFile(abs, buf, 0o644); err != nil {
			t.Fatalf("write %s: %v", abs, err)
		}
		tree[rel] = sha256.Sum256(buf)
	}
	return tree
}

// WriteFileTree writes explicit relative-path→content files under dir and
// returns their checksum tree. Use it for edge cases (empty files, odd names).
func WriteFileTree(t *testing.T, dir string, files map[string][]byte) Tree {
	t.Helper()
	tree := make(Tree)
	for rel, content := range files {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(abs, content, 0o644); err != nil {
			t.Fatalf("write %s: %v", abs, err)
		}
		tree[rel] = sha256.Sum256(content)
	}
	return tree
}

// EdgeCaseFiles returns a set of files exercising tricky paths and sizes:
// empty, single-byte, unicode and spaces in names, deep nesting, and a 12 MiB
// file that spans multiple S3 parts (at the 5 MiB minimum part size).
func EdgeCaseFiles() map[string][]byte {
	rng := rand.New(rand.NewSource(7)) //nolint:gosec
	big := make([]byte, 12<<20)
	rng.Read(big)
	return map[string][]byte{
		"empty.bin":                 {},
		"one-byte.bin":              {0x42},
		"a/b/c/d/deeply-nested.bin": []byte("deep"),
		"name with spaces.bin":      []byte("spaces"),
		"unicode-café-δρ-🎶.bin":     []byte("unicode"),
		"multipart-12mib.bin":       big,
	}
}

// Sync performs a full single-threaded transfer of every item from src to dst.
func Sync(ctx context.Context, src transfer.Source, dst transfer.Destination) error {
	return ConcurrentSync(ctx, src, dst, 1)
}

// ConcurrentSync transfers every item from src to dst using `workers` goroutines,
// mirroring the worker pool's skip/open/put flow — used to exercise backend
// concurrency safety under -race.
func ConcurrentSync(ctx context.Context, src transfer.Source, dst transfer.Destination, workers int) error {
	items, err := src.Scan(ctx, nil)
	if err != nil {
		return fmt.Errorf("scan: %w", err)
	}
	if workers < 1 {
		workers = 1
	}
	work := make(chan transfer.Item)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	setErr := func(e error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = e
		}
		mu.Unlock()
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range work {
				exists, size, err := dst.Stat(ctx, item)
				if err == nil && exists && size == item.Size {
					continue
				}
				open, err := src.Open(ctx, item)
				if err != nil {
					setErr(fmt.Errorf("open %s: %w", item.RelativePath, err))
					continue
				}
				if err := dst.Put(ctx, item, open, item.Size, nil); err != nil {
					setErr(fmt.Errorf("put %s: %w", item.RelativePath, err))
				}
			}
		}()
	}
	for _, item := range items {
		work <- item
	}
	close(work)
	wg.Wait()
	return firstErr
}

// VerifyDest reads every expected item back through the destination-as-source
// (or a local dir) and checks the SHA256 matches.
func VerifyDest(t *testing.T, ctx context.Context, src transfer.Source, want Tree) {
	t.Helper()
	items, err := src.Scan(ctx, nil)
	if err != nil {
		t.Fatalf("verify scan: %v", err)
	}
	got := make(map[string]bool)
	for _, item := range items {
		open, err := src.Open(ctx, item)
		if err != nil {
			t.Fatalf("verify open %s: %v", item.RelativePath, err)
		}
		r, err := open(0)
		if err != nil {
			t.Fatalf("verify read %s: %v", item.RelativePath, err)
		}
		h := sha256.New()
		if _, err := io.Copy(h, r); err != nil {
			_ = r.Close()
			t.Fatalf("verify hash %s: %v", item.RelativePath, err)
		}
		_ = r.Close()
		var sum [sha256.Size]byte
		copy(sum[:], h.Sum(nil))
		wantSum, ok := want[item.RelativePath]
		if !ok {
			t.Errorf("unexpected item at destination: %s", item.RelativePath)
			continue
		}
		if sum != wantSum {
			t.Errorf("checksum mismatch for %s", item.RelativePath)
		}
		got[item.RelativePath] = true
	}
	for rel := range want {
		if !got[rel] {
			t.Errorf("missing item at destination: %s", rel)
		}
	}
}
