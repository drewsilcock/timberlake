package s3backend

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // S3 ETags are MD5; used only to match parts, not for security
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"timberlake/transfer"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// --- wire-progress transport (committed vs in-flight) ---

type ctxKey int

const uploadCounterKey ctxKey = iota

type uploadCounters struct {
	wire      atomic.Int64
	committed atomic.Int64
}

type countingTransport struct{ base http.RoundTripper }

func (t countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	counters, _ := req.Context().Value(uploadCounterKey).(*uploadCounters)
	var body *countingReadCloser
	if counters != nil && req.Body != nil && req.Method == http.MethodPut {
		body = &countingReadCloser{rc: req.Body, wire: &counters.wire}
		req.Body = body
	}
	resp, err := t.base.RoundTrip(req)
	if body != nil && err == nil && resp != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		counters.committed.Add(body.sent.Load())
	}
	return resp, err
}

type countingReadCloser struct {
	rc   io.ReadCloser
	wire *atomic.Int64
	sent atomic.Int64
}

func (c *countingReadCloser) Read(p []byte) (int, error) {
	n, err := c.rc.Read(p)
	if n > 0 {
		c.wire.Add(int64(n))
		c.sent.Add(int64(n))
	}
	return n, err
}

func (c *countingReadCloser) Close() error { return c.rc.Close() }

// readCounter wraps a reader, counting bytes read (for the buffered figure).
type readCounter struct {
	r   io.Reader
	ctr *atomic.Int64
}

func (rc readCounter) Read(p []byte) (int, error) {
	n, err := rc.r.Read(p)
	if n > 0 {
		rc.ctr.Add(int64(n))
	}
	return n, err
}

// --- resume helpers ---

// partMD5Matches reads part n from the source and compares its MD5 to wantETag.
func (s *S3) partMD5Matches(open transfer.OpenFunc, n int32, size int64, wantETag string) (bool, error) {
	// Multipart ETags for multi-part objects have a "-N" suffix and aren't a
	// plain MD5; but per-part ETags from ListParts are the part's MD5.
	r, err := open(int64(n-1) * s.partSize)
	if err != nil {
		return false, err
	}
	defer func() { _ = r.Close() }()

	h := md5.New() //nolint:gosec
	if _, err := io.CopyN(h, r, s.partBytes(n, size)); err != nil && err != io.EOF {
		return false, err
	}
	return hex.EncodeToString(h.Sum(nil)) == wantETag, nil
}

// uploadMissingParts uploads every part not already present, with bounded
// concurrency, recording ETags into completed[n-1].
func (s *S3) uploadMissingParts(ctx context.Context, key, uploadID string, open transfer.OpenFunc, size int64, totalParts int32, existing map[int32]string, completed []types.CompletedPart, mu *sync.Mutex, buffered *atomic.Int64) error {
	sem := make(chan struct{}, s.concurrency)
	var wg sync.WaitGroup
	var firstErr error
	var errOnce sync.Once

	for n := int32(1); n <= totalParts; n++ {
		if _, ok := existing[n]; ok {
			continue
		}
		select {
		case <-ctx.Done():
			errOnce.Do(func() { firstErr = ctx.Err() })
		default:
		}
		if firstErr != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(part int32) {
			defer wg.Done()
			defer func() { <-sem }()
			etag, err := s.uploadPart(ctx, key, uploadID, open, part, size, buffered)
			if err != nil {
				errOnce.Do(func() { firstErr = err })
				return
			}
			mu.Lock()
			completed[part-1] = types.CompletedPart{PartNumber: aws.Int32(part), ETag: aws.String(etag)}
			mu.Unlock()
		}(n)
	}
	wg.Wait()
	return firstErr
}

// uploadPart reads a single part fully into memory (so the request body is
// seekable for signing/retries) and uploads it, returning its ETag.
func (s *S3) uploadPart(ctx context.Context, key, uploadID string, open transfer.OpenFunc, n int32, size int64, buffered *atomic.Int64) (string, error) {
	r, err := open(int64(n-1) * s.partSize)
	if err != nil {
		return "", fmt.Errorf("open part %d: %w", n, err)
	}
	defer func() { _ = r.Close() }()

	length := s.partBytes(n, size)
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", fmt.Errorf("read part %d: %w", n, err)
	}
	if buffered != nil {
		buffered.Add(length)
	}

	out, err := s.client.UploadPart(ctx, &s3.UploadPartInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		UploadId:      aws.String(uploadID),
		PartNumber:    aws.Int32(n),
		Body:          bytes.NewReader(buf),
		ContentLength: aws.Int64(length),
	})
	if err != nil {
		return "", fmt.Errorf("upload part %d: %w", n, err)
	}
	if out.ETag == nil {
		return "", fmt.Errorf("upload part %d: no ETag returned", n)
	}
	return aws.ToString(out.ETag), nil
}

func sortCompleted(parts []types.CompletedPart) {
	sort.Slice(parts, func(i, j int) bool {
		return aws.ToInt32(parts[i].PartNumber) < aws.ToInt32(parts[j].PartNumber)
	})
}

// --- Source (list + ranged get) ---

// Scan lists all objects under the prefix.
func (s *S3) Scan(ctx context.Context, progress transfer.ScanProgress) ([]transfer.Item, error) {
	var items []transfer.Item
	var files, bytes int64
	prefix := ""
	if s.prefix != "" {
		prefix = strings.Trim(s.prefix, "/") + "/"
	}
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(prefix),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list s3://%s/%s: %w", s.bucket, prefix, err)
		}
		for _, obj := range page.Contents {
			if obj.Key == nil {
				continue
			}
			rel := strings.TrimPrefix(*obj.Key, prefix)
			if rel == "" {
				continue
			}
			items = append(items, transfer.Item{RelativePath: rel, Size: aws.ToInt64(obj.Size)})
			files++
			bytes += aws.ToInt64(obj.Size)
			if progress != nil && files%100 == 0 {
				progress(files, bytes)
			}
		}
	}
	if progress != nil {
		progress(files, bytes)
	}
	return items, nil
}

// Open returns an opener that ranged-GETs the object from any offset.
func (s *S3) Open(ctx context.Context, item transfer.Item) (transfer.OpenFunc, error) {
	key := s.key(item)
	return func(offset int64) (io.ReadCloser, error) {
		in := &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)}
		if offset > 0 {
			in.Range = aws.String(fmt.Sprintf("bytes=%d-", offset))
		}
		out, err := s.client.GetObject(ctx, in)
		if err != nil {
			return nil, err
		}
		return out.Body, nil
	}, nil
}
