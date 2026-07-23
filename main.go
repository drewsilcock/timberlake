package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"timberlake/config"
	"timberlake/ui"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	var (
		s3cfgFlag     string
		jobsFlag      int
		partSizeFlag  int64
		endpointFlag  string
		accessKeyFlag string
		secretKeyFlag string
		noSSLFlag     bool
		dryRunFlag    bool
		verifyFlag    bool
	)

	defaultS3Cfg := os.Getenv("S3CMD_CONFIG")
	if defaultS3Cfg == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			defaultS3Cfg = filepath.Join(home, ".s3cfg")
		}
	}

	flag.StringVar(&s3cfgFlag, "s3cfg", defaultS3Cfg, "Path to s3cmd config file")
	flag.IntVar(&jobsFlag, "j", 16, "Number of parallel Space Cowboy worker jobs")
	flag.IntVar(&jobsFlag, "jobs", 16, "Number of parallel Space Cowboy worker jobs")
	flag.Int64Var(&partSizeFlag, "part-size", 16, "Multipart upload chunk size in MiB (smaller parts upload far faster over high-latency links)")
	flag.StringVar(&endpointFlag, "endpoint-url", os.Getenv("AWS_ENDPOINT_URL"), "S3 / Ceph RGW endpoint URL (e.g. http://rgw.local:8080)")
	flag.StringVar(&accessKeyFlag, "access-key", os.Getenv("AWS_ACCESS_KEY_ID"), "S3 Access Key ID")
	flag.StringVar(&secretKeyFlag, "secret-key", os.Getenv("AWS_SECRET_ACCESS_KEY"), "S3 Secret Access Key")
	flag.BoolVar(&noSSLFlag, "no-ssl", false, "Disable HTTPS/SSL for S3 endpoint (No Strings Attached)")
	flag.BoolVar(&dryRunFlag, "dry-run", false, "Perform dry run scan without uploading")
	flag.BoolVar(&verifyFlag, "verify-only", false, "Perform non-writing MD5/size check")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [options] SOURCE_DIR s3://BUCKET/PREFIX [JOBS]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Timberlake — 'N SYNC-powered, resumable, parallel local -> Ceph RGW (S3-compatible) upload tool in Go.\n")
		fmt.Fprintf(os.Stderr, "Ain't no lie, baby, Bye Bye Bye!\n\n")
		fmt.Fprintf(os.Stderr, "Example:\n")
		fmt.Fprintf(os.Stderr, "  %s /data/scan s3://photogrammetry/scans/site-001 24\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
	}

	flag.Parse()

	args := flag.Args()
	if len(args) < 2 {
		flag.Usage()
		os.Exit(2)
	}

	sourceDir := args[0]
	destination := args[1]

	if len(args) >= 3 {
		if j, err := strconv.Atoi(args[2]); err == nil && j > 0 {
			jobsFlag = j
		}
	}

	bucket, prefix, err := config.ParseDestination(destination)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(2)
	}

	appCfg := &config.AppConfig{
		SourceDir:   sourceDir,
		Destination: destination,
		Bucket:      bucket,
		Prefix:      prefix,
		Jobs:        jobsFlag,
		PartSizeMB:  partSizeFlag,
		EndpointURL: endpointFlag,
		AccessKey:   accessKeyFlag,
		SecretKey:   secretKeyFlag,
		UseSSL:      !noSSLFlag,
		S3CfgPath:   s3cfgFlag,
		DryRun:      dryRunFlag,
		VerifyOnly:  verifyFlag,
	}

	// Try reading ~/.s3cfg if available
	if _, err := os.Stat(s3cfgFlag); err == nil {
		_ = config.ParseS3Cfg(s3cfgFlag, appCfg)
	}

	// Environment variable fallback overrides
	if appCfg.AccessKey == "" {
		appCfg.AccessKey = os.Getenv("AWS_ACCESS_KEY_ID")
	}
	if appCfg.SecretKey == "" {
		appCfg.SecretKey = os.Getenv("AWS_SECRET_ACCESS_KEY")
	}
	if appCfg.EndpointURL == "" {
		appCfg.EndpointURL = os.Getenv("AWS_ENDPOINT_URL")
	}

	// Run Bubbletea TUI Program
	p := tea.NewProgram(
		ui.InitialModel(appCfg),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Fatal UI error: %v\n", err)
		os.Exit(1)
	}

	// Print final summary report upon exit
	if m, ok := finalModel.(ui.Model); ok {
		ui.PrintFinalSummary(m)

		// Persist individual upload errors so they can be reviewed after exit.
		if path, werr := ui.WriteErrorLog(m); werr != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not write error log: %v\n", werr)
		} else if path != "" {
			fmt.Fprintf(os.Stderr, "\n⚠  %d file(s) failed — details written to:\n   %s\n", len(m.Errors), path)
			max := len(m.Errors)
			if max > 10 {
				max = 10
			}
			for _, e := range m.Errors[:max] {
				fmt.Fprintf(os.Stderr, "   • %s: %s\n", e.RelativePath, e.Message)
			}
			if len(m.Errors) > 10 {
				fmt.Fprintf(os.Stderr, "   … and %d more (see log file)\n", len(m.Errors)-10)
			}
		}
	}
}
