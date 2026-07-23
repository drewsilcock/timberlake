package s3client

import (
	"io"
)

// ProgressReader wraps an io.Reader and reports byte read updates via a callback.
type ProgressReader struct {
	reader io.Reader
	onRead func(n int)
}

// NewProgressReader constructs a ProgressReader.
func NewProgressReader(r io.Reader, onRead func(n int)) *ProgressReader {
	return &ProgressReader{
		reader: r,
		onRead: onRead,
	}
}

// Read implements io.Reader interface.
func (pr *ProgressReader) Read(p []byte) (n int, err error) {
	n, err = pr.reader.Read(p)
	if n > 0 && pr.onRead != nil {
		pr.onRead(n)
	}
	return n, err
}

// ProgressReaderAt wraps an io.ReaderAt for seekable/part-based reads.
type ProgressReaderAt struct {
	readerAt io.ReaderAt
	onRead   func(n int)
}

func NewProgressReaderAt(r io.ReaderAt, onRead func(n int)) *ProgressReaderAt {
	return &ProgressReaderAt{
		readerAt: r,
		onRead:   onRead,
	}
}

func (pra *ProgressReaderAt) ReadAt(p []byte, off int64) (n int, err error) {
	n, err = pra.readerAt.ReadAt(p, off)
	if n > 0 && pra.onRead != nil {
		pra.onRead(n)
	}
	return n, err
}
