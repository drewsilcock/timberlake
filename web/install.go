package web

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// CloudflaredVersion is the pinned release we are willing to install.
//
// Cloudflare does not publish checksum files with their releases, so the hashes
// below were computed from the release assets by hand. Both the version and the
// hashes must be updated together — see `task update-cloudflared`.
const CloudflaredVersion = "2026.7.3"

// cfAsset describes one platform's release asset.
type cfAsset struct {
	name   string // release asset filename
	sha256 string // hash of the downloaded asset, verified before use
	tgz    bool   // asset is a .tgz containing a single `cloudflared` binary
}

// cfAssets maps GOOS/GOARCH to its pinned asset. Platforms absent here cannot be
// auto-installed; the user is asked to install cloudflared themselves.
var cfAssets = map[string]cfAsset{
	"darwin/arm64":  {"cloudflared-darwin-arm64.tgz", "90c5a4f914d705fd70c135dba6d80b1791d254b08d6d4136301941f88330dd09", true},
	"darwin/amd64":  {"cloudflared-darwin-amd64.tgz", "70d1c8684fa6d14b5843787ec8d1ea8e18b23650e424f4ea43d849a506487c3b", true},
	"linux/amd64":   {"cloudflared-linux-amd64", "9d71c677db00134c1bd4144b7783486b654ad281b1ea62b4972098d19f770f17", false},
	"linux/arm64":   {"cloudflared-linux-arm64", "65259e652a7bea08bf5df603233ab22b8bf3116af8df9f9206209af6a1b955c0", false},
	"windows/amd64": {"cloudflared-windows-amd64.exe", "8635da433b6df8194746e88ed9d2589566c20e38bfc2a80e431a348b7c765841", false},
}

// InstallState is the lifecycle of the assisted download.
type InstallState int

const (
	InstallIdle InstallState = iota
	InstallDownloading
	InstallVerifying
	InstallDone
	InstallFailed
)

// Installer downloads a pinned, checksum-verified cloudflared into the user's
// cache directory. It never writes to $PATH and never runs anything it has not
// verified against the hash pinned above.
type Installer struct {
	mu       sync.Mutex
	state    InstallState
	err      error
	done     int64
	total    int64
	assetTag string
}

// InstallSupported reports whether this platform has a pinned asset.
func InstallSupported() bool {
	_, ok := cfAssets[runtime.GOOS+"/"+runtime.GOARCH]
	return ok
}

// InstallSizeMB is the approximate download size for this platform, for the
// consent prompt. Returns 0 when unsupported.
func InstallSizeMB() int {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "darwin/arm64":
		return 19
	case "darwin/amd64":
		return 21
	case "linux/amd64":
		return 39
	case "linux/arm64":
		return 37
	case "windows/amd64":
		return 54
	}
	return 0
}

// ManagedPath is where an assisted-install cloudflared lives. It is deliberately
// inside the user's cache dir rather than anywhere on $PATH.
func ManagedPath() string {
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	name := "cloudflared"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(base, "timberlake", "bin", name)
}

// ManagedInstalled reports whether we have already installed a copy.
func ManagedInstalled() bool {
	p := ManagedPath()
	if p == "" {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// State returns progress for the UI.
func (i *Installer) State() (state InstallState, done, total int64, err error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.state, i.done, i.total, i.err
}

func (i *Installer) set(state InstallState, err error) {
	i.mu.Lock()
	i.state, i.err = state, err
	i.mu.Unlock()
}

// Install downloads, verifies and installs cloudflared, calling onUpdate as
// progress changes. It is a no-op if an install is already running.
func (i *Installer) Install(onUpdate func()) {
	i.mu.Lock()
	if i.state == InstallDownloading || i.state == InstallVerifying {
		i.mu.Unlock()
		return
	}
	i.state, i.err, i.done, i.total = InstallDownloading, nil, 0, 0
	i.mu.Unlock()

	go func() {
		err := i.run(onUpdate)
		if err != nil {
			i.set(InstallFailed, err)
		} else {
			i.set(InstallDone, nil)
		}
		if onUpdate != nil {
			onUpdate()
		}
	}()
}

func (i *Installer) run(onUpdate func()) error {
	asset, ok := cfAssets[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		return fmt.Errorf("no pinned cloudflared build for %s/%s — please install it yourself",
			runtime.GOOS, runtime.GOARCH)
	}
	dest := ManagedPath()
	if dest == "" {
		return fmt.Errorf("cannot determine a cache directory")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(dest), err)
	}

	url := fmt.Sprintf("https://github.com/cloudflare/cloudflared/releases/download/%s/%s",
		CloudflaredVersion, asset.name)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("downloading cloudflared: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading cloudflared: HTTP %d", resp.StatusCode)
	}

	i.mu.Lock()
	i.total = resp.ContentLength
	i.assetTag = asset.name
	i.mu.Unlock()

	tmp, err := os.CreateTemp(filepath.Dir(dest), "cloudflared-download-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	// Stream to disk while hashing, so we never hold 40 MB in memory and never
	// touch the bytes twice.
	hasher := sha256.New()
	counter := &progressWriter{inst: i, onUpdate: onUpdate}
	if _, err := io.Copy(io.MultiWriter(tmp, hasher, counter), resp.Body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("downloading cloudflared: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	i.set(InstallVerifying, nil)
	if onUpdate != nil {
		onUpdate()
	}
	if got := hex.EncodeToString(hasher.Sum(nil)); got != asset.sha256 {
		return fmt.Errorf("checksum mismatch for %s: got %s, expected %s (refusing to install)",
			asset.name, got, asset.sha256)
	}

	// Only now, with the hash verified, do we put anything in place.
	if asset.tgz {
		if err := extractTgzBinary(tmpName, dest); err != nil {
			return err
		}
	} else {
		if err := os.Rename(tmpName, dest); err != nil {
			return fmt.Errorf("installing to %s: %w", dest, err)
		}
	}
	if err := os.Chmod(dest, 0o755); err != nil {
		return fmt.Errorf("making %s executable: %w", dest, err)
	}
	return nil
}

// extractTgzBinary pulls the single `cloudflared` entry out of a .tgz.
func extractTgzBinary(archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("reading archive: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("archive did not contain a cloudflared binary")
		}
		if err != nil {
			return fmt.Errorf("reading archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != "cloudflared" {
			continue
		}
		out, err := os.CreateTemp(filepath.Dir(dest), "cloudflared-extract-*")
		if err != nil {
			return err
		}
		// Bounded copy: the binary is tens of MB, so cap well above that rather
		// than trusting the archive's declared size.
		if _, err := io.CopyN(out, tr, 512<<20); err != nil && err != io.EOF {
			_ = out.Close()
			_ = os.Remove(out.Name())
			return fmt.Errorf("extracting cloudflared: %w", err)
		}
		if err := out.Close(); err != nil {
			return err
		}
		return os.Rename(out.Name(), dest)
	}
}

// progressWriter counts downloaded bytes for the UI.
type progressWriter struct {
	inst     *Installer
	onUpdate func()
	lastPing time.Time
}

func (w *progressWriter) Write(p []byte) (int, error) {
	w.inst.mu.Lock()
	w.inst.done += int64(len(p))
	w.inst.mu.Unlock()
	// Throttle UI wakeups; the download is tens of thousands of chunks.
	if w.onUpdate != nil && time.Since(w.lastPing) > 200*time.Millisecond {
		w.lastPing = time.Now()
		w.onUpdate()
	}
	return len(p), nil
}
