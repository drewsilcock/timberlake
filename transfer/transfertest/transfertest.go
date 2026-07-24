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

// Sync performs a full single-threaded transfer of every item from src to dst,
// applying the same skip/resume flow the worker pool uses.
func Sync(ctx context.Context, src transfer.Source, dst transfer.Destination) error {
	items, err := src.Scan(ctx, nil)
	if err != nil {
		return fmt.Errorf("scan: %w", err)
	}
	for _, item := range items {
		exists, size, err := dst.Stat(ctx, item)
		if err == nil && exists && size == item.Size {
			continue
		}
		open, err := src.Open(ctx, item)
		if err != nil {
			return fmt.Errorf("open %s: %w", item.RelativePath, err)
		}
		if err := dst.Put(ctx, item, open, item.Size, nil); err != nil {
			return fmt.Errorf("put %s: %w", item.RelativePath, err)
		}
	}
	return nil
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
