// Package localfs implements a local-filesystem transfer.Source and
// transfer.Destination.
package localfs

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"

	"timberlake/transfer"
)

// Local is a local-filesystem backend rooted at a directory.
type Local struct {
	root string
}

// New returns a Local backend rooted at the given directory.
func New(root string) (*Local, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("invalid local path %s: %w", root, err)
	}
	return &Local{root: abs}, nil
}

func (l *Local) Describe() string { return l.root }
func (l *Local) Close() error     { return nil }

// abs resolves an item's relative path against the root.
func (l *Local) abs(item transfer.Item) string {
	return filepath.Join(l.root, filepath.FromSlash(item.RelativePath))
}

// --- Source ---

// Scan walks the source directory tree, collecting regular files.
func (l *Local) Scan(_ context.Context, progress transfer.ScanProgress) ([]transfer.Item, error) {
	info, err := os.Stat(l.root)
	if err != nil {
		return nil, fmt.Errorf("cannot stat source %s: %w", l.root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("source path is not a directory: %s", l.root)
	}

	var items []transfer.Item
	var files, bytes int64
	err = filepath.WalkDir(l.root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return nil //nolint:nilerr // skip unreadable paths, keep walking
		}
		rel, err := filepath.Rel(l.root, path)
		if err != nil {
			return nil //nolint:nilerr
		}
		fi, err := d.Info()
		if err != nil {
			return nil //nolint:nilerr
		}
		items = append(items, transfer.Item{
			RelativePath: filepath.ToSlash(rel),
			Size:         fi.Size(),
		})
		files++
		bytes += fi.Size()
		if progress != nil && files%100 == 0 {
			progress(files, bytes)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("error walking %s: %w", l.root, err)
	}
	if progress != nil {
		progress(files, bytes)
	}
	return items, nil
}

// Open returns an opener that reads the item from any offset.
func (l *Local) Open(_ context.Context, item transfer.Item) (transfer.OpenFunc, error) {
	path := l.abs(item)
	return func(offset int64) (io.ReadCloser, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		if offset > 0 {
			if _, err := f.Seek(offset, io.SeekStart); err != nil {
				_ = f.Close()
				return nil, err
			}
		}
		return f, nil
	}, nil
}

// --- Destination ---

// Stat reports the destination file's existence and size.
func (l *Local) Stat(_ context.Context, item transfer.Item) (bool, int64, error) {
	fi, err := os.Stat(l.abs(item))
	if os.IsNotExist(err) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, err
	}
	return true, fi.Size(), nil
}

// Put writes the item to the destination, resuming from a partial file if one
// exists (append semantics), and reports progress.
func (l *Local) Put(_ context.Context, item transfer.Item, open transfer.OpenFunc, size int64, progress transfer.Progress) error {
	dst := l.abs(item)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("mkdir for %s: %w", dst, err)
	}

	// Resume: if a shorter partial file exists, append from its length.
	var start int64
	if fi, err := os.Stat(dst); err == nil && fi.Size() > 0 && fi.Size() < size {
		start = fi.Size()
	}

	flags := os.O_CREATE | os.O_WRONLY
	if start > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	out, err := os.OpenFile(dst, flags, 0o644)
	if err != nil {
		return fmt.Errorf("open dest %s: %w", dst, err)
	}
	defer func() { _ = out.Close() }()

	src, err := open(start)
	if err != nil {
		return fmt.Errorf("open source at %d: %w", start, err)
	}
	defer func() { _ = src.Close() }()

	var written atomic.Int64
	written.Store(start)
	counting := io.TeeReader(src, writerFunc(func(p []byte) (int, error) {
		written.Add(int64(len(p)))
		if progress != nil {
			n := written.Load()
			progress(n, n, n)
		}
		return len(p), nil
	}))

	if _, err := io.Copy(out, counting); err != nil {
		return fmt.Errorf("copy to %s: %w", dst, err)
	}
	if progress != nil {
		progress(size, size, size)
	}
	return nil
}

// writerFunc adapts a function to io.Writer.
type writerFunc func(p []byte) (int, error)

func (w writerFunc) Write(p []byte) (int, error) { return w(p) }
