package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AppConfig holds all runtime settings for timberlake.
type AppConfig struct {
	SourceDir   string
	Destination string
	Bucket      string
	Prefix      string
	Jobs        int
	UploadJobs  int // max concurrent transfers; 0 or >= Jobs means "no extra limit"
	PartSizeMB  int64
	MaxRetries  int
	EndpointURL string
	AccessKey   string
	SecretKey   string
	UseSSL      bool
	S3CfgPath   string
	VerifyOnly  bool
	DryRun      bool
	OutOfSync   bool // strip all 'N SYNC references from the UI

	// SFTP options (used when a source/destination is an sftp:// endpoint).
	SFTPPassword string
	SFTPKeyPath  string
	SFTPInsecure bool // skip known_hosts host-key verification
}

// ParseS3Cfg reads an s3cmd config file (e.g. ~/.s3cfg) and extracts relevant endpoint & auth fields.
func ParseS3Cfg(path string, cfg *AppConfig) error {
	if path == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			path = filepath.Join(home, ".s3cfg")
		}
	}

	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("could not open s3cfg file at %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.ToLower(strings.TrimSpace(parts[0]))
		val := strings.TrimSpace(parts[1])

		switch key {
		case "access_key":
			if cfg.AccessKey == "" {
				cfg.AccessKey = val
			}
		case "secret_key":
			if cfg.SecretKey == "" {
				cfg.SecretKey = val
			}
		case "host_base":
			if cfg.EndpointURL == "" {
				if !strings.HasPrefix(val, "http://") && !strings.HasPrefix(val, "https://") {
					scheme := "https://"
					if !cfg.UseSSL {
						scheme = "http://"
					}
					cfg.EndpointURL = scheme + val
				} else {
					cfg.EndpointURL = val
				}
			}
		case "use_https":
			valLower := strings.ToLower(val)
			cfg.UseSSL = valLower == "true" || valLower == "1" || valLower == "yes"
			if cfg.EndpointURL != "" && strings.HasPrefix(cfg.EndpointURL, "http://") && cfg.UseSSL {
				cfg.EndpointURL = strings.Replace(cfg.EndpointURL, "http://", "https://", 1)
			}
		}
	}

	return scanner.Err()
}

// ParseDestination splits s3://bucket/prefix into bucket and relative prefix.
func ParseDestination(dest string) (bucket, prefix string, err error) {
	if !strings.HasPrefix(dest, "s3://") {
		return "", "", fmt.Errorf("destination must start with s3:// (got %s)", dest)
	}

	trimmed := strings.TrimPrefix(dest, "s3://")
	parts := strings.SplitN(trimmed, "/", 2)
	bucket = parts[0]
	if bucket == "" {
		return "", "", fmt.Errorf("invalid destination s3:// URI: missing bucket name")
	}

	if len(parts) > 1 {
		prefix = strings.TrimPrefix(parts[1], "/")
	}
	return bucket, prefix, nil
}
