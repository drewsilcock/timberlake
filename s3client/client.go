package s3client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"timberlake/config"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// S3Client manages S3 interactions for Ceph RGW.
type S3Client struct {
	Client   *s3.Client
	Uploader *manager.Uploader
	Config   *config.AppConfig
}

// uploadCounterKey tags a context with the per-file upload counters.
type ctxKey int

const uploadCounterKey ctxKey = iota

// uploadCounters tracks, for a single file upload:
//   - wire:      bytes streamed onto the socket so far (continuous, sub-part)
//   - committed: bytes belonging to parts the server has acknowledged (2xx)
//
// wire >= committed always: the difference is the part(s) currently uploading.
type uploadCounters struct {
	wire      atomic.Int64
	committed atomic.Int64
}

// countingTransport wraps an HTTP transport. For upload PUTs carrying counters
// in their context it (a) counts request-body bytes as they stream to the wire,
// giving continuous within-part progress, and (b) on a 2xx response marks that
// part's bytes committed — so the UI can colour finished parts differently from
// the part currently in flight.
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
	wire *atomic.Int64 // shared across the file's parts
	sent atomic.Int64  // this request's bytes, promoted to committed on success
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

// NewS3Client initializes the AWS S3 client.
func NewS3Client(ctx context.Context, appCfg *config.AppConfig) (*S3Client, error) {
	var options []func(*awsconfig.LoadOptions) error

	// Region default for Ceph RGW
	options = append(options, awsconfig.WithRegion("eu-west-2"))

	// Since aws-sdk-go-v2 defaulted RequestChecksumCalculation to
	// "when_supported" (early 2025), every upload part gets a CRC32 checksum
	// header. Many Ceph RGW versions don't implement these and silently hang on
	// multipart PUTs. Only send checksums when the caller explicitly asks for
	// them, restoring compatibility with S3-compatible stores.
	options = append(options,
		awsconfig.WithRequestChecksumCalculation(aws.RequestChecksumCalculationWhenRequired),
		awsconfig.WithResponseChecksumValidation(aws.ResponseChecksumValidationWhenRequired),
	)

	// Custom HTTP client: counts uploaded bytes on the wire (for true upload
	// progress) and keeps plenty of idle connections warm for the many parallel
	// part uploads a high-concurrency sync creates.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 256
	transport.MaxIdleConnsPerHost = 256
	transport.IdleConnTimeout = 90 * time.Second
	httpClient := &http.Client{Transport: countingTransport{base: transport}}
	options = append(options, awsconfig.WithHTTPClient(httpClient))

	// Static credentials if access/secret keys provided
	if appCfg.AccessKey != "" && appCfg.SecretKey != "" {
		options = append(options, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(appCfg.AccessKey, appCfg.SecretKey, ""),
		))
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	// Resolve custom endpoint (e.g. Ceph RGW) if set.
	var baseEndpoint string
	if appCfg.EndpointURL != "" {
		baseEndpoint = appCfg.EndpointURL
		if !strings.HasPrefix(baseEndpoint, "http://") && !strings.HasPrefix(baseEndpoint, "https://") {
			if appCfg.UseSSL {
				baseEndpoint = "https://" + baseEndpoint
			} else {
				baseEndpoint = "http://" + baseEndpoint
			}
		}
	}

	// Ceph RGW requires PathStyle addressing (UsePathStyle: true)
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = true
		if baseEndpoint != "" {
			o.BaseEndpoint = aws.String(baseEndpoint)
		}
	})

	uploader := manager.NewUploader(client, func(u *manager.Uploader) {
		if appCfg.PartSizeMB > 0 {
			u.PartSize = appCfg.PartSizeMB * 1024 * 1024
		} else {
			u.PartSize = 16 * 1024 * 1024 // 16 MiB default
		}
		u.Concurrency = 4
		// The Uploader keeps its own checksum setting (defaulting to
		// "when_supported"), independent of the client config above, and will
		// otherwise stamp CRC32 checksums onto every multipart part — which
		// hangs incompatible Ceph RGW versions. Turn it off here too.
		u.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	})

	return &S3Client{
		Client:   client,
		Uploader: uploader,
		Config:   appCfg,
	}, nil
}

// CheckObjectExists performs a HeadObject request to check if file already exists with matching size.
func (c *S3Client) CheckObjectExists(ctx context.Context, bucket, key string, localSize int64) (exists bool, err error) {
	headOutput, err := c.Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})

	if err != nil {
		var notFound *types.NotFound
		var noSuchKey *types.NoSuchKey
		if errors.As(err, &notFound) || errors.As(err, &noSuchKey) || strings.Contains(err.Error(), "404") {
			return false, nil
		}
		return false, err
	}

	if headOutput.ContentLength != nil && *headOutput.ContentLength == localSize {
		return true, nil
	}

	return false, nil
}

// UploadFile uploads a local file to S3, reporting live progress via onProgress
// with three nested figures (committed <= uploaded <= buffered):
//
//   - committed: bytes in parts the server has acknowledged (finished chunks)
//   - uploaded:  bytes streamed to the wire, incl. the part(s) mid-flight
//   - buffered:  bytes read from local disk into part buffers (read-ahead)
//
// The UI colours these as three bar segments, so you can watch an individual
// chunk fill in real time and then "lock in" as committed. Progress is driven by
// a ticker so it keeps updating smoothly regardless of read/part boundaries.
func (c *S3Client) UploadFile(ctx context.Context, localPath, bucket, key string, onProgress func(committed, uploaded, buffered int64)) error {
	file, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("failed to open local file %s: %w", localPath, err)
	}
	defer func() { _ = file.Close() }()

	fileInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat file %s: %w", localPath, err)
	}

	var counters uploadCounters
	var buffered atomic.Int64

	// Counters are keyed on this file's context, so the counting transport
	// attributes every part's bytes to this upload.
	ctx = context.WithValue(ctx, uploadCounterKey, &counters)

	reader := NewProgressReader(file, func(n int) { buffered.Add(int64(n)) })

	// Emit progress on a steady tick, independent of read/part activity.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(150 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				onProgress(counters.committed.Load(), counters.wire.Load(), buffered.Load())
			}
		}
	}()

	_, err = c.Uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(bucket),
		Key:           aws.String(key),
		Body:          reader,
		ContentLength: aws.Int64(fileInfo.Size()),
	})

	close(stop)
	wg.Wait()

	if err != nil {
		return fmt.Errorf("failed to upload object s3://%s/%s: %w", bucket, key, err)
	}

	// Final exact reading: the whole object is now committed.
	size := fileInfo.Size()
	onProgress(size, size, size)
	return nil
}

// BuildKey constructs the S3 object key combining prefix and relative file path.
func BuildKey(prefix, relPath string) string {
	cleanRel := filepath.ToSlash(relPath)
	cleanRel = strings.TrimPrefix(cleanRel, "/")

	if prefix == "" {
		return cleanRel
	}

	cleanPrefix := strings.Trim(prefix, "/")
	return cleanPrefix + "/" + cleanRel
}
