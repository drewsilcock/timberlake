package s3client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

// NewS3Client initializes the AWS S3 client.
func NewS3Client(ctx context.Context, appCfg *config.AppConfig) (*S3Client, error) {
	var options []func(*awsconfig.LoadOptions) error

	// Region default for Ceph RGW
	options = append(options, awsconfig.WithRegion("eu-west-2"))

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
			u.PartSize = 256 * 1024 * 1024 // 256 MiB default
		}
		u.Concurrency = 4
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

// UploadFile uploads a local file to S3 with progress tracking callback.
func (c *S3Client) UploadFile(ctx context.Context, localPath, bucket, key string, onProgress func(bytesRead int)) error {
	file, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("failed to open local file %s: %w", localPath, err)
	}
	defer func() { _ = file.Close() }()

	fileInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat file %s: %w", localPath, err)
	}

	reader := NewProgressReader(file, onProgress)

	_, err = c.Uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(bucket),
		Key:           aws.String(key),
		Body:          reader,
		ContentLength: aws.Int64(fileInfo.Size()),
	})

	if err != nil {
		return fmt.Errorf("failed to upload object s3://%s/%s: %w", bucket, key, err)
	}

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
