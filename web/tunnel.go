package web

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// trycloudflareRe matches the ephemeral hostname cloudflared prints on stderr.
var trycloudflareRe = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

// Tunnel manages an optional Cloudflare quick tunnel fronting the local server.
//
// Quick tunnels need no account: cloudflared dials out to Cloudflare's edge and
// is handed an ephemeral hostname, which also means the URL changes every run
// and Cloudflare offers no uptime guarantee. We use WebSockets rather than SSE
// precisely because quick tunnels buffer text/event-stream responses.
type Tunnel struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	cancel context.CancelFunc
	url    string
	err    error
	state  TunnelState
}

// TunnelState is the lifecycle of the quick tunnel.
type TunnelState int

const (
	TunnelOff TunnelState = iota
	TunnelStarting
	TunnelOn
	TunnelFailed
)

// cloudflaredPath returns the cloudflared to use: the checksum-verified copy we
// installed ourselves if present, otherwise whatever is on $PATH.
func cloudflaredPath() string {
	if ManagedInstalled() {
		return ManagedPath()
	}
	if p, err := exec.LookPath("cloudflared"); err == nil {
		return p
	}
	return ""
}

// TunnelAvailable reports whether a cloudflared binary can be found.
func TunnelAvailable() bool { return cloudflaredPath() != "" }

// State returns the current state, public URL and last error.
func (t *Tunnel) State() (TunnelState, string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state, t.url, t.err
}

// Start launches `cloudflared tunnel --url http://127.0.0.1:<port>` and resolves
// the assigned public hostname. tokenPath is appended so the returned URL points
// straight at the progress page.
func (t *Tunnel) Start(port, tokenPath string, onUpdate func()) error {
	t.mu.Lock()
	if t.state == TunnelStarting || t.state == TunnelOn {
		t.mu.Unlock()
		return nil
	}
	bin := cloudflaredPath()
	if bin == "" {
		t.state, t.err = TunnelFailed, fmt.Errorf("cloudflared not found")
		t.mu.Unlock()
		return t.err
	}
	t.state, t.err, t.url = TunnelStarting, nil, ""
	t.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin, "tunnel", "--no-autoupdate",
		"--url", "http://127.0.0.1:"+port)

	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		t.set(TunnelFailed, "", err)
		return err
	}

	t.mu.Lock()
	t.cmd, t.cancel = cmd, cancel
	t.mu.Unlock()

	go t.watch(stderr, tokenPath, onUpdate)
	go func() {
		_ = cmd.Wait()
		if s, _, _ := t.State(); s != TunnelOff {
			t.set(TunnelFailed, "", fmt.Errorf("cloudflared exited"))
			if onUpdate != nil {
				onUpdate()
			}
		}
	}()
	return nil
}

// watch scrapes cloudflared's stderr for the assigned hostname.
func (t *Tunnel) watch(stderr io.Reader, tokenPath string, onUpdate func()) {
	scanner := bufio.NewScanner(stderr)
	deadline := time.Now().Add(45 * time.Second)
	for scanner.Scan() {
		line := scanner.Text()
		if m := trycloudflareRe.FindString(line); m != "" {
			t.set(TunnelOn, strings.TrimRight(m, "/")+tokenPath, nil)
			if onUpdate != nil {
				onUpdate()
			}
			return
		}
		if time.Now().After(deadline) {
			break
		}
	}
	if s, _, _ := t.State(); s == TunnelStarting {
		t.set(TunnelFailed, "", fmt.Errorf("timed out waiting for a tunnel URL"))
		if onUpdate != nil {
			onUpdate()
		}
	}
}

func (t *Tunnel) set(state TunnelState, url string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.state, t.url, t.err = state, url, err
}

// Stop terminates the tunnel if running.
func (t *Tunnel) Stop() {
	t.mu.Lock()
	cancel := t.cancel
	t.cancel, t.cmd = nil, nil
	t.state, t.url, t.err = TunnelOff, "", nil
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
