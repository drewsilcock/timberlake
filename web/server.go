package web

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

//go:embed page.html
var pageHTML []byte

// Server exposes a read-only progress page over HTTP + WebSocket.
type Server struct {
	store store
	token string
	addr  string

	mu       sync.RWMutex
	lanURL   string
	remote   string // tunnel URL, empty when not running
	listener net.Listener
	srv      *http.Server

	broadcast chan struct{}
	subsMu    sync.Mutex
	subs      map[chan struct{}]struct{}
}

// New creates a server bound to addr (e.g. ":8765"). The page is served under an
// unguessable token path so simply knowing the host and port is not enough.
func New(addr string) (*Server, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	return &Server{
		token:     hex.EncodeToString(buf),
		addr:      addr,
		broadcast: make(chan struct{}, 1),
		subs:      map[chan struct{}]struct{}{},
	}, nil
}

// Start begins listening. The returned URL is the LAN address to share.
func (s *Server) Start() (string, error) {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return "", fmt.Errorf("listen on %s: %w", s.addr, err)
	}
	s.listener = ln

	_, port, _ := net.SplitHostPort(ln.Addr().String())
	url := fmt.Sprintf("http://%s:%s/t/%s", lanIP(), port, s.token)

	mux := http.NewServeMux()
	mux.HandleFunc("/t/"+s.token, s.handlePage)
	mux.HandleFunc("/t/"+s.token+"/ws", s.handleWS)
	// Anything else (including a bare / probe) gets a flat 404: the token is
	// the only entry point.
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})

	s.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = s.srv.Serve(ln) }()

	s.mu.Lock()
	s.lanURL = url
	s.mu.Unlock()
	return url, nil
}

// Publish stores the latest snapshot and wakes every connected browser.
func (s *Server) Publish(snap Snapshot) {
	s.store.set(snap)
	s.subsMu.Lock()
	for ch := range s.subs {
		select {
		case ch <- struct{}{}:
		default: // subscriber already has an update pending
		}
	}
	s.subsMu.Unlock()
}

// LanURL returns the local URL for the page.
func (s *Server) LanURL() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lanURL
}

// SetRemoteURL records the public tunnel URL (or "" to clear it).
func (s *Server) SetRemoteURL(u string) {
	s.mu.Lock()
	s.remote = u
	s.mu.Unlock()
}

// RemoteURL returns the public tunnel URL, or "" when no tunnel is running.
func (s *Server) RemoteURL() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.remote
}

// ShareURL is the URL that should be encoded in the QR code: the tunnel when
// one is up, otherwise the LAN address.
func (s *Server) ShareURL() string {
	if r := s.RemoteURL(); r != "" {
		return r
	}
	return s.LanURL()
}

// Port returns the bound TCP port.
func (s *Server) Port() string {
	if s.listener == nil {
		return ""
	}
	_, port, _ := net.SplitHostPort(s.listener.Addr().String())
	return port
}

// TokenPath is the path segment browsers must hit, reused by the tunnel URL.
func (s *Server) TokenPath() string { return "/t/" + s.token }

// Close shuts the server down.
func (s *Server) Close() error {
	if s.srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.srv.Shutdown(ctx)
}

func (s *Server) handlePage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The page is fully self-contained, so a strict CSP costs nothing.
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src *; img-src data:")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(pageHTML)
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// The page may be served through a tunnel on a different host, so we
		// accept any Origin. The connection is read-only either way.
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer func() { _ = c.CloseNow() }()

	ctx := r.Context()
	updates := make(chan struct{}, 1)
	s.subsMu.Lock()
	s.subs[updates] = struct{}{}
	s.subsMu.Unlock()
	defer func() {
		s.subsMu.Lock()
		delete(s.subs, updates)
		s.subsMu.Unlock()
	}()

	send := func() error {
		writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return wsjson.Write(writeCtx, c, s.store.get())
	}

	// Send current state immediately so the page paints without waiting.
	if err := send(); err != nil {
		return
	}

	// Coalesce updates: at most one frame per tick, and a heartbeat so idle
	// connections (and tunnels) stay alive.
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()

	dirty := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-updates:
			dirty = true
		case <-ticker.C:
			if dirty {
				dirty = false
				if err := send(); err != nil {
					return
				}
			}
		case <-heartbeat.C:
			if err := send(); err != nil {
				return
			}
		}
	}
}

// lanIP picks the most likely LAN address for this machine, preferring a
// private IPv4 on an up, non-loopback interface.
func lanIP() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "localhost"
	}
	var fallback string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		// Skip the usual virtual/VPN suspects, which often hand out an address
		// the phone cannot reach.
		name := strings.ToLower(iface.Name)
		virtual := strings.HasPrefix(name, "utun") || strings.HasPrefix(name, "tun") ||
			strings.HasPrefix(name, "tap") || strings.HasPrefix(name, "docker") ||
			strings.HasPrefix(name, "br-") || strings.HasPrefix(name, "veth")

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipnet.IP.To4()
			if ip == nil || !ip.IsPrivate() {
				continue
			}
			if virtual {
				if fallback == "" {
					fallback = ip.String()
				}
				continue
			}
			return ip.String()
		}
	}
	if fallback != "" {
		return fallback
	}
	return "localhost"
}

// ErrNoServer is returned when an operation needs a running server.
var ErrNoServer = errors.New("web server not running")
