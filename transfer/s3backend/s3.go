// Package s3backend implements an S3/Ceph-RGW transfer.Source and
// transfer.Destination, including per-file multipart resume.
package s3backend

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"timberlake/config"
	"timberlake/transfer"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// S3 is an S3-compatible backend bound to a bucket and key prefix.
type S3 struct {
	client      *s3.Client
	uploader    *manager.Uploader
	bucket      string
	prefix      string
	partSize    int64
	concurrency int
}

// New builds an S3 backend for the given endpoint, mirroring the client tuning
// (checksum handling, path style, wire-progress transport) the tool relies on.
func New(ctx context.Context, ep transfer.Endpoint, cfg *config.AppConfig) (*S3, error) {
	partSize := int64(16) << 20
	if cfg.PartSizeMB > 0 {
		partSize = cfg.PartSizeMB << 20
	}
	const concurrency = 4

	options := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion("eu-west-2"),
		// Many Ceph RGW versions hang on the CRC32 checksums aws-sdk-go-v2 adds
		// by default; only send them when explicitly required.
		awsconfig.WithRequestChecksumCalculation(aws.RequestChecksumCalculationWhenRequired),
		awsconfig.WithResponseChecksumValidation(aws.ResponseChecksumValidationWhenRequired),
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 256
	transport.MaxIdleConnsPerHost = 256
	transport.IdleConnTimeout = 90 * time.Second
	options = append(options, awsconfig.WithHTTPClient(&http.Client{
		Transport: countingTransport{base: transport},
	}))

	if cfg.AccessKey != "" && cfg.SecretKey != "" {
		options = append(options, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	baseEndpoint := cfg.EndpointURL
	if baseEndpoint != "" && !strings.HasPrefix(baseEndpoint, "http://") && !strings.HasPrefix(baseEndpoint, "https://") {
		if cfg.UseSSL {
			baseEndpoint = "https://" + baseEndpoint
		} else {
			baseEndpoint = "http://" + baseEndpoint
		}
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.UsePathStyle = true
		if baseEndpoint != "" {
			o.BaseEndpoint = aws.String(baseEndpoint)
		}
	})

	uploader := manager.NewUploader(client, func(u *manager.Uploader) {
		u.PartSize = partSize
		u.Concurrency = concurrency
		u.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		// Leave parts on error so an interrupted multipart upload survives for
		// the next run to resume rather than being aborted.
		u.LeavePartsOnError = true
	})

	return &S3{
		client:      client,
		uploader:    uploader,
		bucket:      ep.Bucket,
		prefix:      ep.Prefix,
		partSize:    partSize,
		concurrency: concurrency,
	}, nil
}

func (s *S3) Describe() string { return fmt.Sprintf("s3://%s/%s", s.bucket, s.prefix) }
func (s *S3) Close() error     { return nil }

// key maps an item's relative path to a full object key under the prefix.
func (s *S3) key(item transfer.Item) string { return BuildKey(s.prefix, item.RelativePath) }

// BuildKey joins a prefix and a relative path into an S3 object key.
func BuildKey(prefix, relPath string) string {
	clean := strings.TrimPrefix(strings.ReplaceAll(relPath, "\\", "/"), "/")
	if prefix == "" {
		return clean
	}
	return strings.Trim(prefix, "/") + "/" + clean
}

// --- Destination ---

// Stat reports whether a complete object exists and its size.
func (s *S3) Stat(ctx context.Context, item transfer.Item) (bool, int64, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.key(item)),
	})
	if err != nil {
		var notFound *types.NotFound
		var noSuchKey *types.NoSuchKey
		if errors.As(err, &notFound) || errors.As(err, &noSuchKey) || strings.Contains(err.Error(), "404") {
			return false, 0, nil
		}
		return false, 0, err
	}
	if out.ContentLength == nil {
		return true, 0, nil
	}
	return true, *out.ContentLength, nil
}

// Put uploads the item, resuming an interrupted multipart upload if one exists.
func (s *S3) Put(ctx context.Context, item transfer.Item, open transfer.OpenFunc, size int64, progress transfer.Progress) error {
	key := s.key(item)

	var counters uploadCounters
	var buffered atomic.Int64
	ctx = context.WithValue(ctx, uploadCounterKey, &counters)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	if progress != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTicker(150 * time.Millisecond)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					progress(counters.committed.Load(), counters.wire.Load(), buffered.Load())
				}
			}
		}()
	}
	finish := func(err error) error {
		close(stop)
		wg.Wait()
		if err == nil && progress != nil {
			progress(size, size, size)
		}
		return err
	}

	// Look for an in-progress multipart upload to resume; only multipart-sized
	// files (> partSize) ever leave one behind.
	var uploadErr error
	if size > s.partSize {
		if uploadID, err := s.findInProgressUpload(ctx, key); err != nil {
			uploadErr = err
		} else if uploadID != "" {
			return finish(s.resumeMultipart(ctx, key, uploadID, open, size, &counters, &buffered))
		}
	}
	if uploadErr != nil {
		return finish(fmt.Errorf("checking for resumable upload of %s: %w", key, uploadErr))
	}

	// Fresh upload via the managed uploader (LeavePartsOnError keeps parts for a
	// future resume if this is interrupted).
	r, err := open(0)
	if err != nil {
		return finish(fmt.Errorf("open source: %w", err))
	}
	defer func() { _ = r.Close() }()
	body := readCounter{r: r, ctr: &buffered}

	_, err = s.uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          body,
		ContentLength: aws.Int64(size),
	})
	if err != nil {
		return finish(fmt.Errorf("upload s3://%s/%s: %w", s.bucket, key, err))
	}
	return finish(nil)
}

// findInProgressUpload returns the UploadId of an incomplete multipart upload
// for the exact key, or "" if none.
func (s *S3) findInProgressUpload(ctx context.Context, key string) (string, error) {
	out, err := s.client.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(key),
	})
	if err != nil {
		return "", err
	}
	for _, u := range out.Uploads {
		if u.Key != nil && *u.Key == key && u.UploadId != nil {
			return *u.UploadId, nil
		}
	}
	return "", nil
}

// resumeMultipart continues an interrupted multipart upload: it keeps the parts
// already stored that still match the local data, uploads the rest, and
// completes the object.
func (s *S3) resumeMultipart(ctx context.Context, key, uploadID string, open transfer.OpenFunc, size int64, counters *uploadCounters, buffered *atomic.Int64) error {
	existing, ok, err := s.validExistingParts(ctx, key, uploadID, open, size)
	if err != nil {
		return err
	}
	if !ok {
		// Parts don't align with our part size / data — start clean.
		_, _ = s.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
			Bucket: aws.String(s.bucket), Key: aws.String(key), UploadId: aws.String(uploadID),
		})
		return s.freshMultipart(ctx, key, open, size, counters, buffered)
	}

	totalParts := int32((size + s.partSize - 1) / s.partSize)
	completed := make([]types.CompletedPart, totalParts)
	var mu sync.Mutex
	var doneBytes int64

	// Seed progress with the parts we're keeping.
	for n, etag := range existing {
		completed[n-1] = types.CompletedPart{PartNumber: aws.Int32(n), ETag: aws.String(etag)}
		pb := s.partBytes(n, size)
		doneBytes += pb
		counters.committed.Add(pb)
		counters.wire.Add(pb)
		buffered.Add(pb)
	}

	if err := s.uploadMissingParts(ctx, key, uploadID, open, size, totalParts, existing, completed, &mu, buffered); err != nil {
		return err // leave the upload in place for another resume
	}

	sortCompleted(completed)
	_, err = s.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:          aws.String(s.bucket),
		Key:             aws.String(key),
		UploadId:        aws.String(uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{Parts: completed},
	})
	if err != nil {
		return fmt.Errorf("complete resumed upload %s: %w", key, err)
	}
	return nil
}

// freshMultipart performs a manual multipart upload from scratch (used after we
// abort a misaligned resume). Kept simple by delegating to the managed uploader.
func (s *S3) freshMultipart(ctx context.Context, key string, open transfer.OpenFunc, size int64, _ *uploadCounters, buffered *atomic.Int64) error {
	r, err := open(0)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer func() { _ = r.Close() }()
	_, err = s.uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          readCounter{r: r, ctr: buffered},
		ContentLength: aws.Int64(size),
	})
	if err != nil {
		return fmt.Errorf("fresh upload %s: %w", key, err)
	}
	return nil
}

// validExistingParts lists stored parts and verifies each against local data by
// MD5/ETag. It returns the map of part number -> ETag for parts to keep, and ok
// = false if the stored parts don't align with our part size (caller restarts).
func (s *S3) validExistingParts(ctx context.Context, key, uploadID string, open transfer.OpenFunc, size int64) (map[int32]string, bool, error) {
	out, err := s.client.ListParts(ctx, &s3.ListPartsInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), UploadId: aws.String(uploadID),
	})
	if err != nil {
		return nil, false, fmt.Errorf("list parts for %s: %w", key, err)
	}
	valid := make(map[int32]string)
	totalParts := int32((size + s.partSize - 1) / s.partSize)
	for _, p := range out.Parts {
		if p.PartNumber == nil || p.ETag == nil || p.Size == nil {
			continue
		}
		n := *p.PartNumber
		if n < 1 || n > totalParts {
			return nil, false, nil // misaligned
		}
		if *p.Size != s.partBytes(n, size) {
			return nil, false, nil // wrong part size -> misaligned
		}
		matches, err := s.partMD5Matches(open, n, size, strings.Trim(*p.ETag, "\""))
		if err != nil {
			return nil, false, err
		}
		if matches {
			valid[n] = *p.ETag
		}
	}
	return valid, true, nil
}

// partBytes returns the byte length of part n (1-indexed) for a file of size.
func (s *S3) partBytes(n int32, size int64) int64 {
	start := int64(n-1) * s.partSize
	if start+s.partSize > size {
		return size - start
	}
	return s.partSize
}
