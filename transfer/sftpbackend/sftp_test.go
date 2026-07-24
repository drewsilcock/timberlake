package sftpbackend_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"net"
	"os"
	"path/filepath"
	"testing"

	"timberlake/config"
	"timberlake/transfer"
	"timberlake/transfer/localfs"
	"timberlake/transfer/sftpbackend"
	"timberlake/transfer/transfertest"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// startSFTPServer spins up an in-process SSH server exposing the sftp subsystem,
// rooted at a temp dir. Returns host, port and the served root path.
func startSFTPServer(t *testing.T) (host, port, root string) {
	t.Helper()
	root = t.TempDir()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if c.User() == "test" && string(pass) == "test" {
				return nil, nil
			}
			return nil, os.ErrPermission
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveSFTP(conn, cfg)
		}
	}()

	host, port, err = net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return host, port, root
}

func serveSFTP(nConn net.Conn, cfg *ssh.ServerConfig) {
	sconn, chans, reqs, err := ssh.NewServerConn(nConn, cfg)
	if err != nil {
		return
	}
	defer func() { _ = sconn.Close() }()
	go ssh.DiscardRequests(reqs)

	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			_ = newChan.Reject(ssh.UnknownChannelType, "only sessions")
			continue
		}
		ch, requests, err := newChan.Accept()
		if err != nil {
			continue
		}
		go func(in <-chan *ssh.Request) {
			for req := range in {
				ok := req.Type == "subsystem" && len(req.Payload) >= 4 && string(req.Payload[4:]) == "sftp"
				_ = req.Reply(ok, nil)
			}
		}(requests)
		server, err := sftp.NewServer(ch)
		if err != nil {
			continue
		}
		go func() { _ = server.Serve(); _ = ch.Close() }()
	}
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
