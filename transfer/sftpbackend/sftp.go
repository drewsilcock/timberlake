// Package sftpbackend implements an SFTP (SSH) transfer.Source and
// transfer.Destination. It connects to an existing SSH server's sftp subsystem;
// no separate SFTP daemon is required.
package sftpbackend

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"sync/atomic"

	"timberlake/config"
	"timberlake/transfer"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// SFTP is an SFTP backend rooted at a remote path.
type SFTP struct {
	ssh    *ssh.Client
	client *sftp.Client
	root   string
	label  string
}

// New dials the SSH server and opens an SFTP session rooted at ep.Path.
func New(_ context.Context, ep transfer.Endpoint, cfg *config.AppConfig) (*SFTP, error) {
	user := ep.User
	if user == "" {
		user = os.Getenv("USER")
	}

	auths, err := authMethods(cfg)
	if err != nil {
		return nil, err
	}
	hostKey, err := hostKeyCallback(cfg)
	if err != nil {
		return nil, err
	}

	sshCfg := &ssh.ClientConfig{
		User:            user,
		Auth:            auths,
		HostKeyCallback: hostKey,
	}
	addr := net.JoinHostPort(ep.Host, ep.Port)
	sshConn, err := ssh.Dial("tcp", addr, sshCfg)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s: %w", addr, err)
	}
	client, err := sftp.NewClient(sshConn)
	if err != nil {
		_ = sshConn.Close()
		return nil, fmt.Errorf("open sftp session: %w", err)
	}

	return &SFTP{
		ssh:    sshConn,
		client: client,
		root:   ep.Path,
		label:  fmt.Sprintf("sftp://%s@%s%s", user, ep.Host, ep.Path),
	}, nil
}

func (s *SFTP) Describe() string { return s.label }

func (s *SFTP) Close() error {
	err := s.client.Close()
	if cerr := s.ssh.Close(); err == nil {
		err = cerr
	}
	return err
}

func (s *SFTP) remote(item transfer.Item) string {
	return path.Join(s.root, item.RelativePath)
}

// --- Source ---

// Scan walks the remote root, collecting regular files.
func (s *SFTP) Scan(_ context.Context, progress transfer.ScanProgress) ([]transfer.Item, error) {
	var items []transfer.Item
	var files, bytes int64
	walker := s.client.Walk(s.root)
	for walker.Step() {
		if err := walker.Err(); err != nil {
			continue // skip unreadable entries
		}
		info := walker.Stat()
		if info.IsDir() || !info.Mode().IsRegular() {
			continue
		}
		rel, err := relPath(s.root, walker.Path())
		if err != nil {
			continue
		}
		items = append(items, transfer.Item{RelativePath: rel, Size: info.Size()})
		files++
		bytes += info.Size()
		if progress != nil && files%100 == 0 {
			progress(files, bytes)
		}
	}
	if progress != nil {
		progress(files, bytes)
	}
	return items, nil
}

// Open returns an opener that reads the remote file from any offset.
func (s *SFTP) Open(_ context.Context, item transfer.Item) (transfer.OpenFunc, error) {
	remote := s.remote(item)
	return func(offset int64) (io.ReadCloser, error) {
		f, err := s.client.Open(remote)
		if err != nil {
			return nil, err
		}
		if offset > 0 {
			if _, err := f.Seek(offset, io.SeekStart); err != nil {
				_ = f.Close()
				return nil, err
			}
		}
		return f, nil
	}, nil
}

// --- Destination ---

// Stat reports the remote file's existence and size.
func (s *SFTP) Stat(_ context.Context, item transfer.Item) (bool, int64, error) {
	fi, err := s.client.Stat(s.remote(item))
	if err != nil {
		if os.IsNotExist(err) {
			return false, 0, nil
		}
		return false, 0, nil // treat stat errors as "not present" -> re-upload
	}
	return true, fi.Size(), nil
}

// Put writes the item to the remote, resuming a shorter partial file by append.
func (s *SFTP) Put(_ context.Context, item transfer.Item, open transfer.OpenFunc, size int64, progress transfer.Progress) error {
	remote := s.remote(item)
	if dir := path.Dir(remote); dir != "." && dir != "/" {
		if err := s.client.MkdirAll(dir); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}

	// Resume only if a shorter partial file exists AND its bytes actually match
	// the source prefix (verified by checksum). Otherwise re-upload from scratch,
	// so a corrupt/divergent partial can never silently produce a bad file.
	var start int64
	if fi, err := s.client.Stat(remote); err == nil && fi.Size() > 0 && fi.Size() < size {
		if ok, err := s.partialMatches(remote, open, fi.Size()); err == nil && ok {
			start = fi.Size()
		}
	}

	// Seek-to-offset rather than O_APPEND: SFTP's append flag is not honoured
	// consistently across servers, so we position writes explicitly.
	flags := os.O_CREATE | os.O_WRONLY
	if start == 0 {
		flags |= os.O_TRUNC
	}
	out, err := s.client.OpenFile(remote, flags)
	if err != nil {
		return fmt.Errorf("open remote %s: %w", remote, err)
	}
	defer func() { _ = out.Close() }()
	if start > 0 {
		if _, err := out.Seek(start, io.SeekStart); err != nil {
			return fmt.Errorf("seek remote %s to %d: %w", remote, start, err)
		}
	}

	src, err := open(start)
	if err != nil {
		return fmt.Errorf("open source at %d: %w", start, err)
	}
	defer func() { _ = src.Close() }()

	var written atomic.Int64
	written.Store(start)
	counting := io.TeeReader(src, writerFunc(func(p []byte) (int, error) {
		n := written.Add(int64(len(p)))
		if progress != nil {
			progress(n, n, n)
		}
		return len(p), nil
	}))
	if _, err := io.Copy(out, counting); err != nil {
		return fmt.Errorf("copy to %s: %w", remote, err)
	}
	if progress != nil {
		progress(size, size, size)
	}
	return nil
}

type writerFunc func(p []byte) (int, error)

func (w writerFunc) Write(p []byte) (int, error) { return w(p) }

// partialMatches reports whether the first `length` bytes of the remote file
// equal the first `length` bytes of the source, by comparing SHA-256 hashes. It
// reads both prefixes; on a slow link this costs one download of the partial,
// which is still far cheaper than re-uploading an already-correct prefix.
func (s *SFTP) partialMatches(remote string, open transfer.OpenFunc, length int64) (bool, error) {
	rf, err := s.client.Open(remote)
	if err != nil {
		return false, err
	}
	defer func() { _ = rf.Close() }()
	remoteSum := sha256.New()
	if _, err := io.CopyN(remoteSum, rf, length); err != nil {
		return false, err
	}

	sf, err := open(0)
	if err != nil {
		return false, err
	}
	defer func() { _ = sf.Close() }()
	sourceSum := sha256.New()
	if _, err := io.CopyN(sourceSum, sf, length); err != nil {
		return false, err
	}

	return bytes.Equal(remoteSum.Sum(nil), sourceSum.Sum(nil)), nil
}

// relPath returns the forward-slash path of target relative to root.
func relPath(root, target string) (string, error) {
	rel := target
	if root != "" && root != "." {
		if len(target) < len(root) {
			return "", fmt.Errorf("%q not under %q", target, root)
		}
		rel = target[len(root):]
	}
	for len(rel) > 0 && rel[0] == '/' {
		rel = rel[1:]
	}
	if rel == "" {
		return path.Base(target), nil
	}
	return rel, nil
}

// --- auth helpers ---

func authMethods(cfg *config.AppConfig) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	if cfg.SFTPPassword != "" {
		methods = append(methods, ssh.Password(cfg.SFTPPassword))
	}

	// SSH agent, if available.
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if conn, err := net.Dial("unix", sock); err == nil {
			methods = append(methods, ssh.PublicKeysCallback(agent.NewClient(conn).Signers))
		}
	}

	// Explicit or default private keys.
	var keyPaths []string
	if cfg.SFTPKeyPath != "" {
		keyPaths = append(keyPaths, cfg.SFTPKeyPath)
	} else if home, err := os.UserHomeDir(); err == nil {
		keyPaths = append(keyPaths,
			path.Join(home, ".ssh", "id_ed25519"),
			path.Join(home, ".ssh", "id_rsa"),
		)
	}
	for _, kp := range keyPaths {
		data, err := os.ReadFile(kp) //nolint:gosec // user-provided key path
		if err != nil {
			continue
		}
		signer, err := ssh.ParsePrivateKey(data)
		if err != nil {
			continue
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}

	if len(methods) == 0 {
		return nil, fmt.Errorf("no SFTP auth available: set -sftp-password, -sftp-key, or an SSH agent/default key")
	}
	return methods, nil
}

func hostKeyCallback(cfg *config.AppConfig) (ssh.HostKeyCallback, error) {
	if cfg.SFTPInsecure {
		return ssh.InsecureIgnoreHostKey(), nil //nolint:gosec // opt-in via -sftp-insecure
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	cb, err := knownhosts.New(path.Join(home, ".ssh", "known_hosts"))
	if err != nil {
		return nil, fmt.Errorf("load known_hosts (use -sftp-insecure to skip): %w", err)
	}
	return cb, nil
}
