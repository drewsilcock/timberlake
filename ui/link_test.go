package ui

import (
	"strings"
	"testing"
)

func TestHyperlinkWrapsWithOSC8(t *testing.T) {
	got := hyperlink("http://192.168.1.3:8765/t/abc", "click me")
	if !strings.HasPrefix(got, "\x1b]8;;http://192.168.1.3:8765/t/abc\x1b\\") {
		t.Errorf("missing OSC 8 opener: %q", got)
	}
	if !strings.HasSuffix(got, "\x1b]8;;\x1b\\") {
		t.Errorf("missing OSC 8 terminator: %q", got)
	}
	if !strings.Contains(got, "click me") {
		t.Error("link text should be preserved")
	}
}

func TestHyperlinkRefusesNonHTTP(t *testing.T) {
	for _, bad := range []string{
		"file:///etc/passwd",
		"javascript:alert(1)",
		"ftp://example.com",
		"not a url",
		"",
	} {
		got := hyperlink(bad, "text")
		if strings.Contains(got, "\x1b]8") {
			t.Errorf("%q should not be turned into a hyperlink, got %q", bad, got)
		}
		if got != "text" {
			t.Errorf("%q: want plain text fallback, got %q", bad, got)
		}
	}
}

func TestOpenBrowserRefusesNonHTTP(t *testing.T) {
	for _, bad := range []string{"file:///etc/passwd", "javascript:alert(1)", "", "; rm -rf /"} {
		if err := openBrowser(bad); err == nil {
			t.Errorf("openBrowser(%q) should refuse", bad)
		}
	}
}

func TestSafeURLAcceptsOurLinks(t *testing.T) {
	for _, good := range []string{
		"http://192.168.1.3:8765/t/deadbeef",
		"https://sunny-cat-42.trycloudflare.com/t/deadbeef",
	} {
		if !safeURL(good) {
			t.Errorf("safeURL(%q) = false, want true", good)
		}
	}
}
