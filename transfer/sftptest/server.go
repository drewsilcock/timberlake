// Package sftptest provides an in-process SSH/SFTP server for end-to-end tests,
// so SFTP backend behaviour can be exercised without an external daemon.
package sftptest

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"os"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Server is a running in-process SFTP server rooted at Root.
type Server struct {
	Host string
	Port string
	Root string // absolute path served as the SFTP root
	User string // accepted username
	Pass string // accepted password

	ln net.Listener
}

// Start launches an SFTP server on a random loopback port, serving a fresh temp
// directory. Call Close when done.
func Start() (*Server, error) {
	root, err := os.MkdirTemp("", "sftptest-*")
	if err != nil {
		return nil, err
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}

	s := &Server{Root: root, User: "test", Pass: "test"}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if c.User() == s.User && string(pass) == s.Pass {
				return nil, nil
			}
			return nil, fmt.Errorf("auth failed")
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s.ln = ln
	s.Host, s.Port, err = net.SplitHostPort(ln.Addr().String())
	if err != nil {
		_ = ln.Close()
		return nil, err
	}

	go s.accept(cfg)
	return s, nil
}

// Close stops the server and removes its served directory.
func (s *Server) Close() error {
	err := s.ln.Close()
	_ = os.RemoveAll(s.Root)
	return err
}

func (s *Server) accept(cfg *ssh.ServerConfig) {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go serve(conn, cfg)
	}
}

func serve(nConn net.Conn, cfg *ssh.ServerConfig) {
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
