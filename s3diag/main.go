// Command s3diag performs one real multipart upload against the configured
// S3/Ceph endpoint with full SDK wire logging, so we can see exactly what the
// server does. It generates a temp file, uploads it to a scratch key, reports
// the outcome, then deletes the remote object and the temp file.
//
// Run it with your credentials in the environment, e.g. via a profile:
//
//	go run ./s3diag -bucket my-bucket -size 200 -part-size 16 -concurrency 16
//
// Credentials and endpoint_url resolve from the profile (or AWS_* env vars).
// Only server responses are logged, never the request auth header, so the
// output is safe to paste directly.
package main

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"os"
	"time"

	"timberlake/transfer/s3backend"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func main() {
	bucket := flag.String("bucket", "", "bucket to upload the test object into")
	prefix := flag.String("prefix", "s3diag-scratch", "key prefix for the test object")
	sizeMB := flag.Int64("size", 600, "size of the generated test file in MiB (>256 forces multipart)")
	partMB := flag.Int64("part-size", 256, "multipart part size in MiB")
	concurrency := flag.Int("concurrency", 4, "multipart upload concurrency")
	timeout := flag.Duration("timeout", 3*time.Minute, "hard timeout for the upload")
	flag.Parse()

	if *bucket == "" {
		fmt.Fprintln(os.Stderr, "error: -bucket is required")
		os.Exit(2)
	}

	endpoint := os.Getenv("AWS_ENDPOINT_URL")
	fmt.Printf("endpoint      : %s\n", endpoint)
	fmt.Printf("bucket/prefix : %s / %s\n", *bucket, *prefix)
	fmt.Printf("file size     : %d MiB   part size: %d MiB (multipart=%v)\n",
		*sizeMB, *partMB, *sizeMB > *partMB)
	fmt.Println("SDK checksum  : RequestChecksumCalculationWhenRequired (matches timberlake)")
	fmt.Println()

	// Write a temp file of random data.
	tmp, err := os.CreateTemp("", "s3diag-*.bin")
	if err != nil {
		fmt.Println("temp file error:", err)
		os.Exit(1)
	}
	defer os.Remove(tmp.Name())
	fmt.Printf("generating %d MiB test file at %s ...\n", *sizeMB, tmp.Name())
	if _, err := copyN(tmp, *sizeMB<<20); err != nil {
		fmt.Println("write error:", err)
		os.Exit(1)
	}
	tmp.Close()

	// Build an S3 client that mirrors timberlake's config, plus wire logging.
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRequestChecksumCalculation(aws.RequestChecksumCalculationWhenRequired),
		awsconfig.WithResponseChecksumValidation(aws.ResponseChecksumValidationWhenRequired),
		// Log server responses (status + any XML error body) and retries. We
		// deliberately do NOT log requests, whose Authorization header carries
		// your access key ID — so this output is safe to paste as-is.
		awsconfig.WithClientLogMode(aws.LogResponseWithBody | aws.LogRetries),
	}
	// Prefer static env credentials if present; otherwise fall back to the
	// resolved profile (AWS_PROFILE), which also supplies endpoint_url.
	if os.Getenv("AWS_ACCESS_KEY_ID") != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(
				os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY"), "")))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		fmt.Println("config error:", err)
		os.Exit(1)
	}
	fmt.Printf("AWS_PROFILE   : %s\n", os.Getenv("AWS_PROFILE"))
	fmt.Printf("resolved region: %s\n", awsCfg.Region)
	if awsCfg.BaseEndpoint != nil {
		fmt.Printf("resolved endpoint (from profile): %s\n", *awsCfg.BaseEndpoint)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.UsePathStyle = true
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
	})
	uploader := manager.NewUploader(client, func(u *manager.Uploader) {
		u.PartSize = *partMB << 20
		u.Concurrency = *concurrency
		u.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	})

	key := s3backend.BuildKey(*prefix, "s3diag-testfile.bin")

	f, err := os.Open(tmp.Name())
	if err != nil {
		fmt.Println("open error:", err)
		os.Exit(1)
	}
	defer f.Close()
	info, _ := f.Stat()

	fmt.Printf("\n==== uploading s3://%s/%s (%d bytes) ====\n\n", *bucket, key, info.Size())
	start := time.Now()
	_, err = uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(*bucket),
		Key:           aws.String(key),
		Body:          f,
		ContentLength: aws.Int64(info.Size()),
	})
	elapsed := time.Since(start)

	fmt.Printf("\n==== RESULT ====\n")
	if err != nil {
		fmt.Printf("FAILED after %v\nerror: %v\n", elapsed, err)
	} else {
		fmt.Printf("SUCCESS in %v (%.1f MB/s)\n", elapsed,
			float64(info.Size())/elapsed.Seconds()/1e6)
	}

	// Best-effort cleanup of the remote object.
	delCtx, delCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer delCancel()
	if _, derr := client.DeleteObject(delCtx, &s3.DeleteObjectInput{
		Bucket: aws.String(*bucket), Key: aws.String(key),
	}); derr != nil {
		fmt.Printf("(cleanup: could not delete remote test object: %v)\n", derr)
	} else {
		fmt.Println("(cleanup: remote test object deleted)")
	}

	if err != nil {
		os.Exit(1)
	}
}

// copyN writes n random bytes to f.
func copyN(f *os.File, n int64) (int64, error) {
	const chunk = 4 << 20
	buf := make([]byte, chunk)
	var written int64
	for written < n {
		toWrite := int64(chunk)
		if rem := n - written; rem < toWrite {
			toWrite = rem
		}
		if _, err := rand.Read(buf[:toWrite]); err != nil {
			return written, err
		}
		w, err := f.Write(buf[:toWrite])
		written += int64(w)
		if err != nil {
			return written, err
		}
	}
	return written, nil
}
