package backends_test

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"

	"timberlake/backends"
	"timberlake/config"
	"timberlake/transfer/sftptest"
	"timberlake/transfer/transfertest"
)

// kind describes one backend under test, with factories that (a) produce a
// SOURCE URI already populated with `files`, and (b) produce a fresh DEST URI
// plus a verify URI to read the destination back through.
type kind struct {
	name       string
	sourceWith func(t *testing.T, ctx context.Context, files map[string][]byte) string
	dest       func(t *testing.T, ctx context.Context) (destURI, verifyURI string)
}

// testConfig carries credentials/settings for every backend; the factory picks
// the relevant fields per URI scheme. A small part size forces S3 multipart.
func testConfig() *config.AppConfig {
	return &config.AppConfig{
		EndpointURL:  os.Getenv("TL_S3_ENDPOINT"),
		AccessKey:    os.Getenv("AWS_ACCESS_KEY_ID"),
		SecretKey:    os.Getenv("AWS_SECRET_ACCESS_KEY"),
		PartSizeMB:   5, // S3 minimum; the 12 MiB edge file spans 3 parts
		SFTPPassword: "test",
		SFTPInsecure: true,
	}
}

func localKind() kind {
	return kind{
		name: "local",
		sourceWith: func(t *testing.T, _ context.Context, files map[string][]byte) string {
			dir := t.TempDir()
			transfertest.WriteFileTree(t, dir, files)
			return dir
		},
		dest: func(t *testing.T, _ context.Context) (string, string) {
			dir := t.TempDir()
			return dir, dir
		},
	}
}

func sftpKind() kind {
	start := func(t *testing.T) *sftptest.Server {
		t.Helper()
		srv, err := sftptest.Start()
		if err != nil {
			t.Fatalf("start sftp: %v", err)
		}
		t.Cleanup(func() { _ = srv.Close() })
		return srv
	}
	uri := func(srv *sftptest.Server) string {
		return fmt.Sprintf("sftp://%s@%s:%s%s", srv.User, srv.Host, srv.Port, srv.Root)
	}
	return kind{
		name: "sftp",
		sourceWith: func(t *testing.T, _ context.Context, files map[string][]byte) string {
			srv := start(t)
			transfertest.WriteFileTree(t, srv.Root, files)
			return uri(srv)
		},
		dest: func(t *testing.T, _ context.Context) (string, string) {
			srv := start(t)
			return uri(srv), uri(srv)
		},
	}
}

func s3Kind(t *testing.T) (kind, bool) {
	if os.Getenv("TL_S3_ENDPOINT") == "" || os.Getenv("TL_S3_BUCKET") == "" {
		return kind{}, false
	}
	bucket := os.Getenv("TL_S3_BUCKET")
	uniq := func() string {
		return fmt.Sprintf("s3://%s/matrix-%d-%d", bucket, time.Now().UnixNano(), rand.Intn(1_000_000)) //nolint:gosec
	}
	return kind{
		name: "s3",
		sourceWith: func(t *testing.T, ctx context.Context, files map[string][]byte) string {
			// Populate S3 by syncing a local tree into a fresh prefix.
			local := t.TempDir()
			transfertest.WriteFileTree(t, local, files)
			uri := uniq()
			src, err := backends.NewSource(ctx, local, testConfig())
			if err != nil {
				t.Fatal(err)
			}
			dst, err := backends.NewDestination(ctx, uri, testConfig())
			if err != nil {
				t.Fatal(err)
			}
			if err := transfertest.Sync(ctx, src, dst); err != nil {
				t.Fatalf("seed s3 source: %v", err)
			}
			return uri
		},
		dest: func(t *testing.T, _ context.Context) (string, string) {
			uri := uniq()
			return uri, uri
		},
	}, true
}

func TestBackendMatrix(t *testing.T) {
	ctx := context.Background()
	kinds := []kind{localKind(), sftpKind()}
	if k, ok := s3Kind(t); ok {
		kinds = append(kinds, k)
	} else {
		t.Log("S3 rows skipped (set TL_S3_ENDPOINT/TL_S3_BUCKET to include them)")
	}

	files := transfertest.EdgeCaseFiles()
	want := transfertest.WriteFileTree(t, t.TempDir(), files) // checksum reference

	for _, src := range kinds {
		for _, dst := range kinds {
			src, dst := src, dst
			t.Run(src.name+"->"+dst.name, func(t *testing.T) {
				t.Parallel()
				sourceURI := src.sourceWith(t, ctx, files)
				destURI, verifyURI := dst.dest(t, ctx)

				source, err := backends.NewSource(ctx, sourceURI, testConfig())
				if err != nil {
					t.Fatalf("open source: %v", err)
				}
				defer func() { _ = source.Close() }()
				destination, err := backends.NewDestination(ctx, destURI, testConfig())
				if err != nil {
					t.Fatalf("open dest: %v", err)
				}
				defer func() { _ = destination.Close() }()

				if err := transfertest.ConcurrentSync(ctx, source, destination, 8); err != nil {
					t.Fatalf("sync: %v", err)
				}

				verify, err := backends.NewSource(ctx, verifyURI, testConfig())
				if err != nil {
					t.Fatalf("open verify: %v", err)
				}
				defer func() { _ = verify.Close() }()
				transfertest.VerifyDest(t, ctx, verify, want)
			})
		}
	}
}
