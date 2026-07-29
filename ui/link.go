package ui

import (
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
)

// hyperlink wraps text in an OSC 8 terminal hyperlink, so terminals that
// support it (iTerm2, Ghostty, WezTerm, kitty, Windows Terminal, modern
// GNOME Terminal, …) render it as a clickable link. Terminals that don't
// understand OSC 8 simply show the text, and lipgloss measures the width
// correctly either way.
func hyperlink(target, text string) string {
	if !safeURL(target) {
		return text
	}
	return "\x1b]8;;" + target + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// safeURL only permits the http(s) links we generate ourselves, so a malformed
// or hostile string can never be handed to the OS opener.
func safeURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// openBrowser opens a URL in the user's default browser.
func openBrowser(target string) error {
	if !safeURL(target) {
		return fmt.Errorf("refusing to open %q", target)
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	return cmd.Start()
}
