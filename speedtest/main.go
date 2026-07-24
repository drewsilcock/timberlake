// Command speedtest measures upload and download throughput and round-trip
// latency against an S3-compatible endpoint (e.g. Ceph RGW), across a
// range of concurrency levels, and prints a report suitable for sending to the
// storage team.
//
// Run with credentials in the environment (a profile works):
//
//	go run ./speedtest -bucket my-bucket
//
// It uploads a temporary object, downloads it back, reports the numbers, and
// deletes the object afterwards. No credentials are printed.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func main() {
	bucket := flag.String("bucket", "", "bucket to test against (required)")
	prefix := flag.String("prefix", "speedtest-scratch", "key prefix for test objects")
	sizeMB := flag.Int64("size", 256, "test object size in MiB")
	partMB := flag.Int64("part-size", 16, "multipart part size in MiB")
	concList := flag.String("concurrency", "1,4,8,16", "comma-separated concurrency levels to test")
	pings := flag.Int("pings", 10, "number of latency probes")
	flag.Parse()

	if *bucket == "" {
		fmt.Fprintln(os.Stderr, "error: -bucket is required")
		os.Exit(2)
	}
	concs := parseInts(*concList)

	ctx := context.Background()
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRequestChecksumCalculation(aws.RequestChecksumCalculationWhenRequired),
		awsconfig.WithResponseChecksumValidation(aws.ResponseChecksumValidationWhenRequired),
	)
	if err != nil {
		fmt.Println("config error:", err)
		os.Exit(1)
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) { o.UsePathStyle = true })

	endpoint := "(default AWS)"
	if cfg.BaseEndpoint != nil {
		endpoint = *cfg.BaseEndpoint
	}

	sizeBytes := *sizeMB << 20
	payload := make([]byte, sizeBytes)
	rand.Read(payload) //nolint:staticcheck // speed of fill doesn't need crypto rand

	// Header
	fmt.Println(strings.Repeat("=", 68))
	fmt.Println("  Timberlake S3 network speed test")
	fmt.Println(strings.Repeat("=", 68))
	fmt.Printf("  timestamp    : %s\n", time.Now().Format(time.RFC3339))
	fmt.Printf("  endpoint     : %s\n", endpoint)
	fmt.Printf("  region       : %s\n", cfg.Region)
	fmt.Printf("  bucket       : %s\n", *bucket)
	fmt.Printf("  object size  : %d MiB\n", *sizeMB)
	fmt.Printf("  part size    : %d MiB (multipart above this)\n", *partMB)
	fmt.Println()

	// --- Latency ---
	fmt.Println("  Latency (HeadBucket round-trip):")
	lat := measureLatency(ctx, client, *bucket, *pings)
	if len(lat) > 0 {
		fmt.Printf("    min %.1f ms   median %.1f ms   max %.1f ms   (n=%d)\n\n",
			lat[0], lat[len(lat)/2], lat[len(lat)-1], len(lat))
	} else {
		fmt.Print("    (no successful probes)\n\n")
	}

	uploader := manager.NewUploader(client, func(u *manager.Uploader) {
		u.PartSize = *partMB << 20
		u.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	})
	downloader := manager.NewDownloader(client, func(d *manager.Downloader) {
		d.PartSize = *partMB << 20
	})

	key := fmt.Sprintf("%s/speedtest-%d.bin", *prefix, time.Now().Unix())

	// --- Upload ---
	fmt.Println("  Upload throughput:")
	fmt.Printf("    %-12s %-12s %-12s\n", "concurrency", "seconds", "MB/s")
	var bestUp float64
	for _, c := range concs {
		secs, mbps, err := timeUpload(ctx, uploader, *bucket, key, payload, c)
		if err != nil {
			fmt.Printf("    %-12d FAILED: %v\n", c, err)
			continue
		}
		fmt.Printf("    %-12d %-12.1f %-12.1f\n", c, secs, mbps)
		if mbps > bestUp {
			bestUp = mbps
		}
	}
	fmt.Println()

	// --- Download ---
	fmt.Println("  Download throughput:")
	fmt.Printf("    %-12s %-12s %-12s\n", "concurrency", "seconds", "MB/s")
	var bestDown float64
	for _, c := range concs {
		secs, mbps, err := timeDownload(ctx, downloader, *bucket, key, sizeBytes, c)
		if err != nil {
			fmt.Printf("    %-12d FAILED: %v\n", c, err)
			continue
		}
		fmt.Printf("    %-12d %-12.1f %-12.1f\n", c, secs, mbps)
		if mbps > bestDown {
			bestDown = mbps
		}
	}
	fmt.Println()

	// Cleanup
	if _, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(*bucket), Key: aws.String(key),
	}); err != nil {
		fmt.Printf("  (cleanup: could not delete %s: %v)\n", key, err)
	} else {
		fmt.Printf("  (cleanup: deleted s3://%s/%s)\n", *bucket, key)
	}

	fmt.Println(strings.Repeat("-", 68))
	fmt.Printf("  SUMMARY: peak upload %.1f MB/s (%.0f Mbps), peak download %.1f MB/s (%.0f Mbps)\n",
		bestUp, bestUp*8, bestDown, bestDown*8)
	fmt.Println(strings.Repeat("=", 68))
}

func timeUpload(ctx context.Context, up *manager.Uploader, bucket, key string, payload []byte, conc int) (float64, float64, error) {
	uploader := *up
	uploader.Concurrency = conc
	start := time.Now()
	_, err := uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(payload),
	})
	if err != nil {
		return 0, 0, err
	}
	secs := time.Since(start).Seconds()
	return secs, float64(len(payload)) / secs / 1e6, nil
}

func timeDownload(ctx context.Context, down *manager.Downloader, bucket, key string, size int64, conc int) (float64, float64, error) {
	downloader := *down
	downloader.Concurrency = conc
	w := &countingWriterAt{}
	start := time.Now()
	n, err := downloader.Download(ctx, w, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return 0, 0, err
	}
	secs := time.Since(start).Seconds()
	return secs, float64(n) / secs / 1e6, nil
}

func measureLatency(ctx context.Context, client *s3.Client, bucket string, n int) []float64 {
	var samples []float64
	for i := 0; i < n; i++ {
		start := time.Now()
		_, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
		if err != nil {
			continue
		}
		samples = append(samples, float64(time.Since(start).Microseconds())/1000.0)
	}
	sort.Float64s(samples)
	return samples
}

// countingWriterAt discards downloaded data while counting bytes, so download
// throughput can be measured without touching disk.
type countingWriterAt struct{ n int64 }

func (c *countingWriterAt) WriteAt(p []byte, _ int64) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

func parseInts(s string) []int {
	var out []int
	for _, part := range strings.Split(s, ",") {
		if v, err := strconv.Atoi(strings.TrimSpace(part)); err == nil && v > 0 {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		out = []int{1, 4, 8, 16}
	}
	return out
}
