// Package transfer defines the backend-agnostic Source and Destination
// abstractions Timberlake copies between, plus endpoint URI parsing. Concrete
// backends (local filesystem, S3, SFTP) live in sub-packages and implement these
// interfaces; the worker pool pipes any Source into any Destination.
package transfer

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
)

// Item is one file to transfer, identified by a path relative to the root.
type Item struct {
	RelativePath string // forward-slash separated, relative to the root
	Size         int64
}

// ScanProgress reports scan progress: files and bytes discovered so far.
type ScanProgress func(files, bytes int64)

// Progress reports transfer progress for the current item as nested byte counts
// where committed <= uploaded <= buffered <= total:
//
//   - committed: durably stored on the destination (acked parts / flushed bytes)
//   - uploaded:  handed to the destination transport (possibly still in flight)
//   - buffered:  read from the source into memory (read-ahead)
//
// Streaming backends collapse the three onto a single value.
type Progress func(committed, uploaded, buffered int64)

// OpenFunc opens the item's content starting at the given byte offset. Each call
// returns an independent reader that the caller must Close, enabling concurrent
// ranged reads and resume-from-offset.
type OpenFunc func(offset int64) (io.ReadCloser, error)

// Source enumerates and reads items from a location.
type Source interface {
	// Scan returns every item under the source root, reporting progress.
	Scan(ctx context.Context, progress ScanProgress) ([]Item, error)
	// Open returns an OpenFunc bound to the given item.
	Open(ctx context.Context, item Item) (OpenFunc, error)
	// Describe returns a human-readable label for the UI/summary.
	Describe() string
	// Close releases any underlying connection.
	Close() error
}

// Destination stores items at a location.
type Destination interface {
	// Stat reports whether the destination already holds a complete copy of the
	// item and its current size, for the skip check (exists && size == want).
	Stat(ctx context.Context, item Item) (exists bool, size int64, err error)
	// Put transfers the item's content (obtained via open) to the destination,
	// resuming from partial state where the backend supports it.
	Put(ctx context.Context, item Item, open OpenFunc, size int64, progress Progress) error
	// Describe returns a human-readable label for the UI/summary.
	Describe() string
	// Close releases any underlying connection.
	Close() error
}

// Scheme identifies a backend kind.
type Scheme string

const (
	SchemeLocal Scheme = "local"
	SchemeS3    Scheme = "s3"
	SchemeSFTP  Scheme = "sftp"
)

// Endpoint is a parsed source/destination URI.
type Endpoint struct {
	Scheme Scheme
	Raw    string

	// Local
	Root string

	// S3 (s3://bucket/prefix)
	Bucket string
	Prefix string

	// SFTP (sftp://[user@]host[:port]/path)
	User string
	Host string
	Port string
	Path string
}

// ParseEndpoint classifies a source/destination argument by URI scheme. A value
// with no recognised scheme (or file://) is treated as a local filesystem path.
func ParseEndpoint(raw string) (Endpoint, error) {
	switch {
	case strings.HasPrefix(raw, "s3://"):
		u, err := url.Parse(raw)
		if err != nil {
			return Endpoint{}, fmt.Errorf("invalid s3 URI %q: %w", raw, err)
		}
		if u.Host == "" {
			return Endpoint{}, fmt.Errorf("invalid s3 URI %q: missing bucket", raw)
		}
		return Endpoint{
			Scheme: SchemeS3,
			Raw:    raw,
			Bucket: u.Host,
			Prefix: strings.Trim(u.Path, "/"),
		}, nil

	case strings.HasPrefix(raw, "sftp://"):
		u, err := url.Parse(raw)
		if err != nil {
			return Endpoint{}, fmt.Errorf("invalid sftp URI %q: %w", raw, err)
		}
		if u.Hostname() == "" {
			return Endpoint{}, fmt.Errorf("invalid sftp URI %q: missing host", raw)
		}
		port := u.Port()
		if port == "" {
			port = "22"
		}
		user := ""
		if u.User != nil {
			user = u.User.Username()
		}
		path := u.Path
		if path == "" {
			path = "."
		}
		return Endpoint{
			Scheme: SchemeSFTP,
			Raw:    raw,
			User:   user,
			Host:   u.Hostname(),
			Port:   port,
			Path:   path,
		}, nil

	default:
		root := strings.TrimPrefix(raw, "file://")
		return Endpoint{Scheme: SchemeLocal, Raw: raw, Root: root}, nil
	}
}
