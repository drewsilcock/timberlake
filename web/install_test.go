package web

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPinnedAssetsAreWellFormed(t *testing.T) {
	if len(cfAssets) == 0 {
		t.Fatal("no pinned cloudflared assets")
	}
	for platform, a := range cfAssets {
		if !strings.Contains(platform, "/") {
			t.Errorf("platform key %q should be GOOS/GOARCH", platform)
		}
		if len(a.sha256) != 64 {
			t.Errorf("%s: sha256 %q is not 64 hex chars", platform, a.sha256)
		}
		if _, err := hex.DecodeString(a.sha256); err != nil {
			t.Errorf("%s: sha256 is not valid hex: %v", platform, err)
		}
		if a.name == "" {
			t.Errorf("%s: missing asset name", platform)
		}
		if a.tgz != strings.HasSuffix(a.name, ".tgz") {
			t.Errorf("%s: tgz flag (%v) disagrees with asset name %q", platform, a.tgz, a.name)
		}
	}
	// Every pinned hash must be distinct — a copy/paste slip would otherwise
	// let us install the wrong platform's binary.
	seen := map[string]string{}
	for platform, a := range cfAssets {
		if other, dup := seen[a.sha256]; dup {
			t.Errorf("%s and %s share a checksum", platform, other)
		}
		seen[a.sha256] = platform
	}
}

func TestInstallSupportedMatchesAssetTable(t *testing.T) {
	_, ok := cfAssets[runtime.GOOS+"/"+runtime.GOARCH]
	if got := InstallSupported(); got != ok {
		t.Errorf("InstallSupported() = %v, want %v for %s/%s", got, ok, runtime.GOOS, runtime.GOARCH)
	}
	if ok && InstallSizeMB() <= 0 {
		t.Error("supported platform should advertise a download size for the consent prompt")
	}
}

func TestManagedPathIsInCacheNotOnPath(t *testing.T) {
	p := ManagedPath()
	if p == "" {
		t.Skip("no user cache dir in this environment")
	}
	if !strings.Contains(p, "timberlake") {
		t.Errorf("managed path %q should be namespaced under timberlake", p)
	}
	// It must not land in a directory that is on $PATH.
	dir := filepath.Dir(p)
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		if entry != "" && filepath.Clean(entry) == filepath.Clean(dir) {
			t.Errorf("managed install dir %q is on $PATH; it must not be", dir)
		}
	}
}

func TestExtractTgzBinary(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "a.tgz")
	payload := []byte("#!/bin/sh\necho hi\n")

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: "cloudflared", Mode: 0o755, Size: int64(len(payload)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatal(err)
	}
	_ = tw.Close()
	_ = gz.Close()
	if err := os.WriteFile(archive, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(dir, "out", "cloudflared")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := extractTgzBinary(archive, dest); err != nil {
		t.Fatalf("extract: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Error("extracted binary does not match the archived content")
	}
}

func TestExtractTgzRejectsArchiveWithoutBinary(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "a.tgz")

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "README", Mode: 0o644, Size: 2, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("hi"))
	_ = tw.Close()
	_ = gz.Close()
	if err := os.WriteFile(archive, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	err := extractTgzBinary(archive, filepath.Join(dir, "cloudflared"))
	if err == nil {
		t.Fatal("expected an error for an archive with no cloudflared binary")
	}
	if !strings.Contains(err.Error(), "did not contain") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestRealInstall downloads the pinned cloudflared for real and verifies it.
// Opt-in (network + tens of MB): TL_INSTALL_TEST=1 go test ./web -run TestRealInstall
func TestRealInstall(t *testing.T) {
	if os.Getenv("TL_INSTALL_TEST") == "" {
		t.Skip("set TL_INSTALL_TEST=1 to exercise the real cloudflared download")
	}
	if !InstallSupported() {
		t.Skip("no pinned asset for this platform")
	}

	// Install into a temp cache so the developer's real cache is untouched.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if runtime.GOOS == "darwin" {
		t.Skip("macOS UserCacheDir ignores XDG_CACHE_HOME; run manually if desired")
	}

	var inst Installer
	done := make(chan struct{})
	inst.Install(func() {
		if s, _, _, _ := inst.State(); s == InstallDone || s == InstallFailed {
			select {
			case <-done:
			default:
				close(done)
			}
		}
	})
	<-done

	state, _, _, err := inst.State()
	if state != InstallDone {
		t.Fatalf("install state = %v, err = %v", state, err)
	}
	if !ManagedInstalled() {
		t.Fatal("installer reported success but no binary is present")
	}
}

// TestChecksumMismatchIsDetected verifies the comparison the installer relies
// on: a single flipped byte must not match the pinned hash.
func TestChecksumMismatchIsDetected(t *testing.T) {
	good := []byte("pretend this is cloudflared")
	sum := sha256.Sum256(good)
	pinned := hex.EncodeToString(sum[:])

	tampered := append([]byte(nil), good...)
	tampered[0] ^= 0xFF
	got := sha256.Sum256(tampered)

	if hex.EncodeToString(got[:]) == pinned {
		t.Fatal("tampered payload must not match the pinned checksum")
	}
}
