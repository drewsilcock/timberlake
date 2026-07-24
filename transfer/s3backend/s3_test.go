package s3backend_test

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // matches S3 ETag semantics for the test
	"crypto/sha256"
	"fmt"
	"io"
	"math/rand"
	"os"
	"testing"
	"time"

	"timberlake/config"
	"timberlake/transfer"
	"timberlake/transfer/localfs"
	"timberlake/transfer/s3backend"
	"timberlake/transfer/transfertest"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// These tests need an S3-compatible endpoint. Set:
//
//	TL_S3_ENDPOINT=http://localhost:9000 TL_S3_BUCKET=tlbucket
//	AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY
func s3Env(t *testing.T) (endpoint, bucket string) {
	t.Helper()
	endpoint = os.Getenv("TL_S3_ENDPOINT")
	bucket = os.Getenv("TL_S3_BUCKET")
	if endpoint == "" || bucket == "" {
		t.Skip("set TL_S3_ENDPOINT and TL_S3_BUCKET to run S3 integration tests")
	}
	return endpoint, bucket
}

func newBackend(t *testing.T, ctx context.Context, endpoint, bucket, prefix string, partMB int64) *s3backend.S3 {
	t.Helper()
	cfg := &config.AppConfig{
		EndpointURL: endpoint,
		AccessKey:   os.Getenv("AWS_ACCESS_KEY_ID"),
		SecretKey:   os.Getenv("AWS_SECRET_ACCESS_KEY"),
		PartSizeMB:  partMB,
	}
	ep := transfer.Endpoint{Scheme: transfer.SchemeS3, Bucket: bucket, Prefix: prefix}
	b, err := s3backend.New(ctx, ep, cfg)
	if err != nil {
		t.Fatalf("new s3 backend: %v", err)
	}
	return b
}

func rawClient(t *testing.T, ctx context.Context, endpoint string) *s3.Client {
	t.Helper()
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("eu-west-2"),
		awsconfig.WithRequestChecksumCalculation(aws.RequestChecksumCalculationWhenRequired),
		awsconfig.WithResponseChecksumValidation(aws.ResponseChecksumValidationWhenRequired),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY"), "")),
	)
	if err != nil {
		t.Fatal(err)
	}
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = true
		o.BaseEndpoint = aws.String(endpoint)
	})
}

func uniquePrefix() string {
	return fmt.Sprintf("itest-%d-%d", time.Now().UnixNano(), rand.Intn(1_000_000)) //nolint:gosec
}

func TestS3RoundTrip(t *testing.T) {
	endpoint, bucket := s3Env(t)
	ctx := context.Background()
	prefix := uniquePrefix()

	srcDir := t.TempDir()
	want := transfertest.MakeTree(t, srcDir, 10, 100, 40_000)

	src, _ := localfs.New(srcDir)
	s3dst := newBackend(t, ctx, endpoint, bucket, prefix, 8)
	if err := transfertest.Sync(ctx, src, s3dst); err != nil {
		t.Fatalf("local->s3 sync: %v", err)
	}

	// Verify by reading back through the S3 backend as a source.
	s3src := newBackend(t, ctx, endpoint, bucket, prefix, 8)
	transfertest.VerifyDest(t, ctx, s3src, want)

	// And a full s3 -> local round trip.
	backDir := t.TempDir()
	localDst, _ := localfs.New(backDir)
	if err := transfertest.Sync(ctx, s3src, localDst); err != nil {
		t.Fatalf("s3->local sync: %v", err)
	}
	verify, _ := localfs.New(backDir)
	transfertest.VerifyDest(t, ctx, verify, want)

	cleanupPrefix(t, ctx, rawClient(t, ctx, endpoint), bucket, prefix)
}

func TestS3ResumeAfterInterrupt(t *testing.T) {
	endpoint, bucket := s3Env(t)
	ctx := context.Background()
	prefix := uniquePrefix()
	raw := rawClient(t, ctx, endpoint)
	defer cleanupPrefix(t, ctx, raw, bucket, prefix)

	const partMB = 8
	data := randomBytes(t, 40<<20) // 40 MiB -> 5 parts at 8 MiB
	item := transfer.Item{RelativePath: "resume/big.bin", Size: int64(len(data))}
	key := s3backend.BuildKey(prefix, item.RelativePath)

	// First attempt: interrupt partway by cancelling the context mid-upload.
	ictx, cancel := context.WithCancel(ctx)
	dst := newBackend(t, ictx, endpoint, bucket, prefix, partMB)
	slowOpen := interruptingOpen(data, int64(len(data))/3, cancel)
	err := dst.Put(ictx, item, slowOpen, item.Size, nil)
	if err == nil {
		t.Fatal("expected the interrupted upload to fail")
	}

	// The interrupted multipart upload should still be present for resume.
	if n := countInProgress(t, ctx, raw, bucket, key); n == 0 {
		t.Fatal("expected an in-progress multipart upload after interrupt")
	}

	// Second attempt: fresh context, should resume and complete.
	dst2 := newBackend(t, ctx, endpoint, bucket, prefix, partMB)
	if err := dst2.Put(ctx, item, plainOpen(data), item.Size, nil); err != nil {
		t.Fatalf("resume put: %v", err)
	}
	if n := countInProgress(t, ctx, raw, bucket, key); n != 0 {
		t.Errorf("expected no in-progress uploads after resume, got %d", n)
	}
	assertObjectMatches(t, ctx, raw, bucket, key, data)
}

func TestS3ResumeReuploadsCorruptPart(t *testing.T) {
	endpoint, bucket := s3Env(t)
	ctx := context.Background()
	prefix := uniquePrefix()
	raw := rawClient(t, ctx, endpoint)
	defer cleanupPrefix(t, ctx, raw, bucket, prefix)

	const partSize = 8 << 20
	data := randomBytes(t, 20<<20) // 20 MiB -> parts: 8,8,4
	item := transfer.Item{RelativePath: "corrupt/big.bin", Size: int64(len(data))}
	key := s3backend.BuildKey(prefix, item.RelativePath)

	// Hand-craft an interrupted multipart upload: part 1 CORRECT, part 2 CORRUPT.
	create, err := raw.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
	})
	if err != nil {
		t.Fatal(err)
	}
	uploadPart(t, ctx, raw, bucket, key, *create.UploadId, 1, data[0:partSize]) // correct
	corrupt := append([]byte(nil), data[partSize:2*partSize]...)
	corrupt[0] ^= 0xFF
	uploadPart(t, ctx, raw, bucket, key, *create.UploadId, 2, corrupt) // corrupt

	// Put should verify parts by MD5, discard the corrupt part 2, and finish
	// with correct content.
	dst := newBackend(t, ctx, endpoint, bucket, prefix, 8)
	if err := dst.Put(ctx, item, plainOpen(data), item.Size, nil); err != nil {
		t.Fatalf("resume put: %v", err)
	}
	assertObjectMatches(t, ctx, raw, bucket, key, data)
}

// --- helpers ---

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	rng := rand.New(rand.NewSource(42)) //nolint:gosec
	rng.Read(b)
	return b
}

func plainOpen(data []byte) transfer.OpenFunc {
	return func(offset int64) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data[offset:])), nil
	}
}

// interruptingOpen returns an OpenFunc whose reader is throttled and calls cancel
// once `at` bytes have been read, to interrupt an in-flight upload mid-stream.
func interruptingOpen(data []byte, at int64, cancel context.CancelFunc) transfer.OpenFunc {
	var total int64
	return func(offset int64) (io.ReadCloser, error) {
		return io.NopCloser(readerFunc(func(p []byte) (int, error) {
			if len(p) > 64<<10 {
				p = p[:64<<10]
			}
			time.Sleep(5 * time.Millisecond) // throttle so cancel lands mid-upload
			r := bytes.NewReader(data[offset:])
			if _, err := r.Seek(total-offset, io.SeekStart); err != nil {
				return 0, err
			}
			n, err := r.Read(p)
			total += int64(n)
			if total >= at {
				cancel()
			}
			return n, err
		})), nil
	}
}

type readerFunc func(p []byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

func countInProgress(t *testing.T, ctx context.Context, c *s3.Client, bucket, key string) int {
	t.Helper()
	out, err := c.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{
		Bucket: aws.String(bucket), Prefix: aws.String(key),
	})
	if err != nil {
		t.Fatalf("list multipart uploads: %v", err)
	}
	n := 0
	for _, u := range out.Uploads {
		if aws.ToString(u.Key) == key {
			n++
		}
	}
	return n
}

func uploadPart(t *testing.T, ctx context.Context, c *s3.Client, bucket, key, uploadID string, num int32, body []byte) {
	t.Helper()
	_, err := c.UploadPart(ctx, &s3.UploadPartInput{
		Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(uploadID),
		PartNumber: aws.Int32(num), Body: bytes.NewReader(body), ContentLength: aws.Int64(int64(len(body))),
	})
	if err != nil {
		t.Fatalf("seed part %d: %v", num, err)
	}
}

func assertObjectMatches(t *testing.T, ctx context.Context, c *s3.Client, bucket, key string, want []byte) {
	t.Helper()
	out, err := c.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		t.Fatalf("get object: %v", err)
	}
	defer func() { _ = out.Body.Close() }()
	got, err := io.ReadAll(out.Body)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(got) != sha256.Sum256(want) {
		t.Errorf("final object mismatch: got %d bytes want %d bytes", len(got), len(want))
	}
	_ = md5.Sum // keep import used across build tags
}

func cleanupPrefix(t *testing.T, ctx context.Context, c *s3.Client, bucket, prefix string) {
	t.Helper()
	// Abort any lingering multipart uploads.
	if lu, err := c.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{Bucket: aws.String(bucket), Prefix: aws.String(prefix)}); err == nil {
		for _, u := range lu.Uploads {
			_, _ = c.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
				Bucket: aws.String(bucket), Key: u.Key, UploadId: u.UploadId,
			})
		}
	}
	// Delete objects.
	p := s3.NewListObjectsV2Paginator(c, &s3.ListObjectsV2Input{Bucket: aws.String(bucket), Prefix: aws.String(prefix)})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return
		}
		for _, o := range page.Contents {
			_, _ = c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: o.Key})
		}
	}
	_ = types.CompletedPart{}
}
