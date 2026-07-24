package sftpbackend_test

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"timberlake/config"
	"timberlake/transfer"
	"timberlake/transfer/localfs"
	"timberlake/transfer/sftpbackend"
	"timberlake/transfer/sftptest"
	"timberlake/transfer/transfertest"
)

// startSFTPServer spins up an in-process SSH/SFTP server and returns host, port
// and the served root path.
func startSFTPServer(t *testing.T) (host, port, root string) {
	t.Helper()
	srv, err := sftptest.Start()
	if err != nil {
		t.Fatalf("start sftp server: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv.Host, srv.Port, srv.Root
}

func newSFTP(t *testing.T, host, port, root string) *sftpbackend.SFTP {
	t.Helper()
	ep := transfer.Endpoint{Scheme: transfer.SchemeSFTP, User: "test", Host: host, Port: port, Path: root}
	cfg := &config.AppConfig{SFTPPassword: "test", SFTPInsecure: true}
	b, err := sftpbackend.New(context.Background(), ep, cfg)
	if err != nil {
		t.Fatalf("connect sftp: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func TestSFTPRoundTrip(t *testing.T) {
	ctx := context.Background()
	host, port, remoteRoot := startSFTPServer(t)

	srcDir := t.TempDir()
	want := transfertest.MakeTree(t, srcDir, 8, 100, 30_000)

	src, _ := localfs.New(srcDir)
	remote := newSFTP(t, host, port, remoteRoot)
	if err := transfertest.Sync(ctx, src, remote); err != nil {
		t.Fatalf("local->sftp: %v", err)
	}
	transfertest.VerifyDest(t, ctx, newSFTP(t, host, port, remoteRoot), want)

	// sftp -> local round trip.
	backDir := t.TempDir()
	localDst, _ := localfs.New(backDir)
	if err := transfertest.Sync(ctx, remote, localDst); err != nil {
		t.Fatalf("sftp->local: %v", err)
	}
	verify, _ := localfs.New(backDir)
	transfertest.VerifyDest(t, ctx, verify, want)
}

func TestSFTPResumeAppendsPartial(t *testing.T) {
	ctx := context.Background()
	host, port, remoteRoot := startSFTPServer(t)

	full := make([]byte, 50_000)
	for i := range full {
		full[i] = byte((i * 13) % 251)
	}
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "big.bin"), full, 0o644); err != nil {
		t.Fatal(err)
	}
	// Pre-seed remote with a partial copy.
	if err := os.WriteFile(filepath.Join(remoteRoot, "big.bin"), full[:20_000], 0o644); err != nil {
		t.Fatal(err)
	}

	src, _ := localfs.New(srcDir)
	remote := newSFTP(t, host, port, remoteRoot)
	item := transfer.Item{RelativePath: "big.bin", Size: int64(len(full))}
	open, _ := src.Open(ctx, item)
	if err := remote.Put(ctx, item, open, item.Size, nil); err != nil {
		t.Fatalf("resume put: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(remoteRoot, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(got) != sha256.Sum256(full) {
		t.Errorf("resumed sftp file mismatch (got %d want %d bytes)", len(got), len(full))
	}
}

func TestSFTPResumeRejectsCorruptPartial(t *testing.T) {
	ctx := context.Background()
	host, port, remoteRoot := startSFTPServer(t)

	full := make([]byte, 50_000)
	for i := range full {
		full[i] = byte((i * 13) % 251)
	}
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "big.bin"), full, 0o644); err != nil {
		t.Fatal(err)
	}
	// Pre-seed remote with a partial whose bytes DIFFER from the source prefix.
	bad := make([]byte, 20_000)
	for i := range bad {
		bad[i] = 0xEE
	}
	if err := os.WriteFile(filepath.Join(remoteRoot, "big.bin"), bad, 0o644); err != nil {
		t.Fatal(err)
	}

	src, _ := localfs.New(srcDir)
	remote := newSFTP(t, host, port, remoteRoot)
	item := transfer.Item{RelativePath: "big.bin", Size: int64(len(full))}
	open, _ := src.Open(ctx, item)
	if err := remote.Put(ctx, item, open, item.Size, nil); err != nil {
		t.Fatalf("put: %v", err)
	}

	// The corrupt partial must have been discarded and the whole file rewritten.
	got, err := os.ReadFile(filepath.Join(remoteRoot, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(got) != sha256.Sum256(full) {
		t.Errorf("corrupt partial not corrected (got %d bytes)", len(got))
	}
}
