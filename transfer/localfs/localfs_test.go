package localfs_test

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"timberlake/transfer"
	"timberlake/transfer/localfs"
	"timberlake/transfer/transfertest"
)

func TestLocalToLocalRoundTrip(t *testing.T) {
	ctx := context.Background()
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	want := transfertest.MakeTree(t, srcDir, 12, 100, 50_000)

	src, err := localfs.New(srcDir)
	if err != nil {
		t.Fatal(err)
	}
	dst, err := localfs.New(dstDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := transfertest.Sync(ctx, src, dst); err != nil {
		t.Fatalf("sync: %v", err)
	}

	verify, _ := localfs.New(dstDir)
	transfertest.VerifyDest(t, ctx, verify, want)
}

func TestLocalSkipsUpToDate(t *testing.T) {
	ctx := context.Background()
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	transfertest.MakeTree(t, srcDir, 3, 1000, 1000)

	src, _ := localfs.New(srcDir)
	dst, _ := localfs.New(dstDir)
	if err := transfertest.Sync(ctx, src, dst); err != nil {
		t.Fatal(err)
	}

	// Second sync: everything should be skipped (Stat reports full size).
	items, _ := src.Scan(ctx, nil)
	for _, it := range items {
		exists, size, err := dst.Stat(ctx, it)
		if err != nil || !exists || size != it.Size {
			t.Errorf("expected %s present at full size, got exists=%v size=%d err=%v", it.RelativePath, exists, size, err)
		}
	}
}

func TestLocalResumeAppendsPartial(t *testing.T) {
	ctx := context.Background()
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	full := make([]byte, 20_000)
	for i := range full {
		full[i] = byte(i * 7)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "big.bin"), full, 0o644); err != nil {
		t.Fatal(err)
	}
	// Pre-seed the destination with the first half, simulating an interruption.
	if err := os.WriteFile(filepath.Join(dstDir, "big.bin"), full[:8000], 0o644); err != nil {
		t.Fatal(err)
	}

	src, _ := localfs.New(srcDir)
	dst, _ := localfs.New(dstDir)
	item := transfer.Item{RelativePath: "big.bin", Size: int64(len(full))}
	open, _ := src.Open(ctx, item)
	if err := dst.Put(ctx, item, open, item.Size, nil); err != nil {
		t.Fatalf("resume put: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dstDir, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(got) != sha256.Sum256(full) {
		t.Errorf("resumed file does not match original (len got=%d want=%d)", len(got), len(full))
	}
}
