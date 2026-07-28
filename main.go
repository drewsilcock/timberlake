package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"timberlake/backends"
	"timberlake/config"
	"timberlake/ui"
	"timberlake/web"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
)

// version is overridable at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	var (
		jobs         int
		uploadJobs   int
		partSize     int64
		endpoint     string
		accessKey    string
		secretKey    string
		noSSL        bool
		dryRun       bool
		verifyOnly   bool
		s3cfg        string
		sftpPassword string
		sftpKey      string
		sftpInsecure bool
		outOfSync    bool
		webEnable    bool
		webAddr      string
	)

	// Detect --out-of-sync early so the help text can drop the flavour too.
	plain := false
	for _, a := range os.Args[1:] {
		if a == "--out-of-sync" {
			plain = true
		}
	}
	pick := func(themed, boring string) string {
		if plain {
			return boring
		}
		return themed
	}

	defaultS3Cfg := os.Getenv("S3CMD_CONFIG")
	if defaultS3Cfg == "" {
		if home, err := os.UserHomeDir(); err == nil {
			defaultS3Cfg = filepath.Join(home, ".s3cfg")
		}
	}

	cmd := &cobra.Command{
		Use:     "timberlake [flags] SOURCE DEST [JOBS]",
		Short:   pick("'N SYNC-powered, resumable, parallel file sync", "Resumable, parallel file sync"),
		Version: version,
		Long: `Timberlake syncs files between local paths, S3-compatible object stores, and
SFTP servers, in any direction, with parallel transfers and per-file resume.

SOURCE and DEST may each be:
  /path/to/dir                       local filesystem
  s3://bucket/prefix                 S3 / Ceph RGW
  sftp://[user@]host[:port]/path     SFTP (over an existing SSH server)` +
			pick("\n\nAin't no lie, baby, Bye Bye Bye!", ""),
		Example: `  timberlake /data/scan s3://my-bucket/scans/site-001 24
  timberlake /data/scan sftp://user@host/backup/scan
  timberlake s3://my-bucket/scans/site-001 /restore/site-001`,
		Args:          cobra.RangeArgs(2, 3),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, args []string) error {
			sourceURI, destURI := args[0], args[1]
			if len(args) == 3 {
				if j, err := strconv.Atoi(args[2]); err == nil && j > 0 {
					jobs = j
				}
			}

			appCfg := &config.AppConfig{
				SourceDir:    sourceURI,
				Destination:  destURI,
				Jobs:         jobs,
				UploadJobs:   uploadJobs,
				PartSizeMB:   partSize,
				EndpointURL:  endpoint,
				AccessKey:    accessKey,
				SecretKey:    secretKey,
				UseSSL:       !noSSL,
				S3CfgPath:    s3cfg,
				DryRun:       dryRun,
				VerifyOnly:   verifyOnly,
				OutOfSync:    outOfSync,
				SFTPPassword: sftpPassword,
				SFTPKeyPath:  sftpKey,
				SFTPInsecure: sftpInsecure,
			}
			if _, err := os.Stat(s3cfg); err == nil {
				_ = config.ParseS3Cfg(s3cfg, appCfg)
			}
			if appCfg.AccessKey == "" {
				appCfg.AccessKey = os.Getenv("AWS_ACCESS_KEY_ID")
			}
			if appCfg.SecretKey == "" {
				appCfg.SecretKey = os.Getenv("AWS_SECRET_ACCESS_KEY")
			}
			if appCfg.EndpointURL == "" {
				appCfg.EndpointURL = os.Getenv("AWS_ENDPOINT_URL")
			}

			return run(appCfg, sourceURI, destURI, webEnable, webAddr)
		},
	}

	f := cmd.Flags()
	f.IntVarP(&jobs, "jobs", "j", 16, "number of parallel worker jobs (used for destination checks and transfers)")
	f.IntVarP(&uploadJobs, "upload-jobs", "u", 0, "max concurrent transfers; workers above this keep checking/skipping (0 = same as --jobs)")
	f.Int64VarP(&partSize, "part-size", "p", 16, "multipart chunk size in MiB (smaller uploads faster over high-latency links)")
	f.StringVar(&endpoint, "endpoint-url", os.Getenv("AWS_ENDPOINT_URL"), "S3 / Ceph RGW endpoint URL")
	f.StringVar(&accessKey, "access-key", os.Getenv("AWS_ACCESS_KEY_ID"), "S3 access key ID")
	f.StringVar(&secretKey, "secret-key", os.Getenv("AWS_SECRET_ACCESS_KEY"), "S3 secret access key")
	f.BoolVar(&noSSL, "no-ssl", false, "disable HTTPS for the S3 endpoint")
	f.BoolVarP(&dryRun, "dry-run", "n", false, "report what would transfer without writing")
	f.BoolVar(&verifyOnly, "verify-only", false, "check destination presence/size without writing")
	f.StringVar(&s3cfg, "s3cfg", defaultS3Cfg, "path to an s3cmd config file")
	f.StringVar(&sftpPassword, "sftp-password", "", "password for sftp:// endpoints")
	f.StringVar(&sftpKey, "sftp-key", "", "path to a private key for sftp:// endpoints")
	f.BoolVar(&sftpInsecure, "sftp-insecure", false, "skip SSH known_hosts verification for sftp://")
	f.BoolVar(&outOfSync, "out-of-sync", false, "remove all 'N SYNC references from the UI")
	f.BoolVar(&webEnable, "web", true, "serve a read-only progress page on the LAN and show a QR code (--web=false to disable)")
	f.StringVar(&webAddr, "web-addr", ":8765", "address for the progress page (with --web)")

	return cmd
}

// run builds the backends and drives the TUI, returning a non-nil error only for
// setup failures or a failed verification (so scripts get a non-zero exit).
func run(appCfg *config.AppConfig, sourceURI, destURI string, webEnable bool, webAddr string) error {
	ctx := context.Background()
	source, err := backends.NewSource(ctx, sourceURI, appCfg)
	if err != nil {
		return fmt.Errorf("opening source %q: %w", sourceURI, err)
	}
	defer func() { _ = source.Close() }()

	dest, err := backends.NewDestination(ctx, destURI, appCfg)
	if err != nil {
		return fmt.Errorf("opening destination %q: %w", destURI, err)
	}
	defer func() { _ = dest.Close() }()

	model := ui.InitialModel(appCfg, source, dest)

	if webEnable {
		// The progress page is a convenience, not the job: if the port is busy
		// or the listener fails, warn and sync anyway.
		srv, err := web.New(webAddr)
		if err == nil {
			_, err = srv.Start()
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: progress page disabled: %v\n", err)
		} else {
			defer func() { _ = srv.Close() }()
			tunnel := &web.Tunnel{}
			defer tunnel.Stop()

			model.Web = srv
			model.Tunnel = tunnel
			model.Installer = &web.Installer{}
			model.ShowQR = true
		}
	}

	p := tea.NewProgram(
		model,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	finalModel, err := p.Run()
	if err != nil {
		return fmt.Errorf("UI error: %w", err)
	}

	m, ok := finalModel.(ui.Model)
	if !ok {
		return nil
	}
	ui.PrintFinalSummary(m)

	if path, werr := ui.WriteErrorLog(m); werr != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not write error log: %v\n", werr)
	} else if path != "" {
		label := "failed"
		if appCfg.VerifyOnly {
			label = "discrepant"
		}
		fmt.Fprintf(os.Stderr, "\n⚠  %d file(s) %s — details written to:\n   %s\n", len(m.Errors), label, path)
		limit := min(len(m.Errors), 10)
		for _, e := range m.Errors[:limit] {
			fmt.Fprintf(os.Stderr, "   • %s: %s\n", e.RelativePath, e.Message)
		}
		if len(m.Errors) > 10 {
			fmt.Fprintf(os.Stderr, "   … and %d more (see log file)\n", len(m.Errors)-10)
		}
	}

	if appCfg.VerifyOnly && len(m.Errors) > 0 {
		return errVerificationFailed
	}
	return nil
}

var errVerificationFailed = errors.New("verification found discrepancies")
