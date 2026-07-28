package web

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func startTestServer(t *testing.T) *Server {
	t.Helper()
	s, err := New("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// baseURL is the loopback address of the running server (LanURL may point at a
// LAN interface that the test host cannot dial reliably).
func baseURL(s *Server) string {
	return "http://127.0.0.1:" + s.Port() + s.TokenPath()
}

func TestServesPageOnTokenPath(t *testing.T) {
	s := startTestServer(t)

	resp, err := http.Get(baseURL(s))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	page := string(body)
	for _, want := range []string{"TIMBERLAKE", "WebSocket", "<marquee"} {
		if !strings.Contains(page, want) {
			t.Errorf("page missing %q", want)
		}
	}
	// The page must be self-contained: no external asset references.
	for _, bad := range []string{"http://cdn", "https://cdn", "unpkg.com", "googleapis.com"} {
		if strings.Contains(page, bad) {
			t.Errorf("page references external asset %q", bad)
		}
	}
}

func TestUnknownPathsAre404(t *testing.T) {
	s := startTestServer(t)

	for _, path := range []string{"/", "/t/wrong-token", "/admin"} {
		resp, err := http.Get("http://127.0.0.1:" + s.Port() + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", path, resp.StatusCode)
		}
	}
}

func TestWebSocketStreamsSnapshots(t *testing.T) {
	s := startTestServer(t)
	s.Publish(Snapshot{Phase: "uploading", TotalFiles: 10, Uploaded: 3, Source: "/src", Destination: "s3://b/p"})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	wsURL := "ws://127.0.0.1:" + s.Port() + s.TokenPath() + "/ws"
	c, resp, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	defer func() { _ = c.CloseNow() }()

	// First frame should arrive immediately with current state.
	var got Snapshot
	if err := wsjson.Read(ctx, c, &got); err != nil {
		t.Fatalf("read first frame: %v", err)
	}
	if got.Phase != "uploading" || got.Uploaded != 3 {
		t.Errorf("first frame = %+v, want phase=uploading uploaded=3", got)
	}
	if got.Source != "/src" || got.Destination != "s3://b/p" {
		t.Errorf("endpoints not propagated: %+v", got)
	}

	// A later publish should be pushed to the open connection.
	s.Publish(Snapshot{Phase: "done", TotalFiles: 10, Uploaded: 10})
	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := wsjson.Read(ctx, c, &got); err != nil {
			t.Fatalf("read update: %v", err)
		}
		if got.Phase == "done" && got.Uploaded == 10 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("did not receive updated snapshot; last = %+v", got)
		}
	}
}

func TestShareURLPrefersTunnel(t *testing.T) {
	s := startTestServer(t)

	if s.ShareURL() != s.LanURL() {
		t.Error("with no tunnel, ShareURL should be the LAN URL")
	}
	s.SetRemoteURL("https://example.trycloudflare.com" + s.TokenPath())
	if s.ShareURL() != "https://example.trycloudflare.com"+s.TokenPath() {
		t.Errorf("ShareURL = %q, want the tunnel URL", s.ShareURL())
	}
	s.SetRemoteURL("")
	if s.ShareURL() != s.LanURL() {
		t.Error("clearing the tunnel should fall back to the LAN URL")
	}
}

func TestTokenIsUnguessableAndStable(t *testing.T) {
	a, err := New("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b, err := New("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if a.TokenPath() == b.TokenPath() {
		t.Error("two servers must not share a token")
	}
	if len(a.TokenPath()) < 20 {
		t.Errorf("token path %q looks too short to be unguessable", a.TokenPath())
	}
	first := a.TokenPath()
	if second := a.TokenPath(); first != second {
		t.Errorf("token must be stable for the life of the server: %q then %q", first, second)
	}
}

func TestTunnelReportsMissingBinaryCleanly(t *testing.T) {
	// cloudflared is not expected in the test environment; if it happens to be
	// installed, skip rather than launching a real tunnel.
	if TunnelAvailable() {
		t.Skip("cloudflared present; skipping missing-binary check")
	}
	var tun Tunnel
	err := tun.Start("1234", "/t/abc", nil)
	if err == nil {
		t.Fatal("expected an error when cloudflared is absent")
	}
	state, url, serr := tun.State()
	if state != TunnelFailed || url != "" || serr == nil {
		t.Errorf("state=%v url=%q err=%v, want failed/empty/non-nil", state, url, serr)
	}
}
